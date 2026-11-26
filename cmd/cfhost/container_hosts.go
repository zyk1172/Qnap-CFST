package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type ContainerHostsStatus struct {
	Name string `json:"name"`
	ID string `json:"id"`
	State string `json:"state"`
	Status string `json:"status,omitempty"`
}

var containerNameRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")
var containerIDRE = regexp.MustCompile("^[0-9a-f]{64}$")
func validContainerName(name string) bool { return containerNameRE.MatchString(name) }
func dockerHostsEnabled() bool { return os.Getenv("CFHOST_DOCKER_TARGETS") == "1" }

func dockerHostsCommand(ctx context.Context, input string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil { return nil, fmt.Errorf("Docker %s failed: %w", args[0], err) }
	return out, nil
}

func listContainerHosts(ctx context.Context) ([]ContainerHostsStatus, error) {
	out, err := dockerHostsCommand(ctx, "", "ps", "-a", "--no-trunc", "--format", "{{.ID}}\t{{.Names}}\t{{.State}}")
	if err != nil { return nil, err }
	self, _ := os.Hostname()
	result := []ContainerHostsStatus{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !containerIDRE.MatchString(f[0]) || !validContainerName(f[1]) { continue }
		if f[1] == getenv("CFHOST_CONTAINER_NAME", "cfhost") || f[1] == self || (self != "" && strings.HasPrefix(f[0], self)) { continue }
		result = append(result, ContainerHostsStatus{ID:f[0], Name:f[1], State:f[2]})
	}
	return result, nil
}

func (a *App) setContainerInventory(list []ContainerHostsStatus, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil { a.state.ContainerHostsError = err.Error(); a.state.ContainerHosts = nil; return }
	a.state.ContainerHostsError = ""
	a.state.ContainerHosts = list
}

func (a *App) refreshContainerInventory() {
	if !dockerHostsEnabled() {
		a.setContainerInventory(nil, fmt.Errorf("Docker Hosts targets disabled; enable CFHOST_DOCKER_TARGETS and mount Docker socket"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := listContainerHosts(ctx)
	a.setContainerInventory(list, err)
}

func (a *App) syncContainerHosts(cfg Config, mappings map[string]string) {
	if !dockerHostsEnabled() || len(cfg.Hosts.ContainerTargets) == 0 { a.refreshContainerInventory(); return }
	// Read current config too: a user who disabled a target during an ongoing
	// network job must not receive a late write from its old configuration.
	current := a.snapshotConfig()
	allowed := map[string]bool{}
	for _, name := range current.Hosts.ContainerTargets { allowed[name] = true }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	list, err := listContainerHosts(ctx)
	cancel()
	if err != nil { a.setContainerInventory(nil, err); return }
	selected := map[string]bool{}
	for _, name := range cfg.Hosts.ContainerTargets { if allowed[name] { selected[name] = true } }
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i := range list {
		if !selected[list[i].Name] { continue }
		delete(selected, list[i].Name)
		if list[i].State != "running" { list[i].Status = "waiting for container start"; continue }
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			limit <- struct{}{}
			defer func(){ <-limit }()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if err := updateContainerHosts(ctx, list[i].ID, mappings); err != nil {
				list[i].Status = err.Error()
			} else { list[i].Status = "synchronized" }
		}(i)
	}
	wg.Wait()
	for name := range selected { list = append(list, ContainerHostsStatus{Name:name, State:"missing", Status:"waiting for container"}) }
	a.setContainerInventory(list, nil)
}

type inspectedContainerMount struct { Destination string }

func hasExplicitHostsMount(mounts []inspectedContainerMount) bool {
	for _, mount := range mounts {
		dest := strings.TrimRight(mount.Destination, "/")
		if dest == "" || dest == "/etc/hosts" || dest == "/etc" || strings.HasPrefix("/etc/hosts", dest+"/") { return true }
	}
	return false
}

func renderContainerHosts(current string, mappings map[string]string) (string, error) {
	if err := validateManagedMarkers(current); err != nil { return "", err }
	base, _, err := clearCFHostBlocks(current)
	if err != nil { return "", err }
	if len(mappings) == 0 { return base, nil }
	block, err := renderHosts("", mappings)
	if err != nil { return "", err }
	start := strings.Index(block, hostsBegin)
	if start < 0 { return "", fmt.Errorf("managed block missing") }
	if len(base)>0 && base[len(base)-1]!='\n' { base += "\n" }
	return base + block[start:], nil
}

func updateContainerHosts(ctx context.Context, id string, mappings map[string]string) error {
	if !containerIDRE.MatchString(id) { return fmt.Errorf("invalid Docker container ID") }
	out, err := dockerHostsCommand(ctx, "", "inspect", "--format", "{{json .Mounts}}", id)
	if err != nil { return err }
	var mounts []inspectedContainerMount
	if err := json.Unmarshal(out, &mounts); err != nil { return fmt.Errorf("invalid Docker mount metadata") }
	if hasExplicitHostsMount(mounts) { return fmt.Errorf("explicit Hosts or parent mount; remove it before enabling target") }
	out, err = dockerHostsCommand(ctx, "", "exec", "--user", "0", id, "sh", "-c", "test -f /etc/hosts && test ! -L /etc/hosts && cat /etc/hosts")
	if err != nil { return err }
	if len(out)>1024*1024 { return fmt.Errorf("container Hosts exceeds size limit") }
	desired, err := renderContainerHosts(string(out), mappings)
	if err != nil { return err }
	if desired == string(out) { return nil }
	// Fixed script, with data in stdin and no untrusted shell interpolation.
	input := base64.StdEncoding.EncodeToString(out)+"\n"+base64.StdEncoding.EncodeToString([]byte(desired))+"\n"
	_, err = dockerHostsCommand(ctx, input, "exec", "-i", "--user", "0", id, "sh", "-c", containerHostsWriteScript)
	return err
}

const containerHostsWriteScript = `set -eu
test -f /etc/hosts && test ! -L /etc/hosts
d=$(mktemp -d)
trap 'rm -rf "$d"' EXIT
IFS= read -r old
IFS= read -r new
printf '%s' "$old" | base64 -d > "$d/old"
printf '%s' "$new" | base64 -d > "$d/new"
inode=$(stat -Lc '%d:%i' /etc/hosts)
cmp -s "$d/old" /etc/hosts
test "$inode" = "$(stat -Lc '%d:%i' /etc/hosts)"
if cat "$d/new" > /etc/hosts && test "$inode" = "$(stat -Lc '%d:%i' /etc/hosts)" && cmp -s "$d/new" /etc/hosts; then
  exit 0
fi
if test "$inode" = "$(stat -Lc '%d:%i' /etc/hosts)"; then
  cat "$d/old" > /etc/hosts || true
fi
exit 1`
