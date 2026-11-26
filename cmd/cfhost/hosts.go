package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	hostsBegin = "# >>> CFHOST MANAGED >>>"
	hostsEnd   = "# <<< CFHOST MANAGED <<<"
	legacyLatencyBegin = "# CF-YX-LATENCY-BEGIN"
	legacyLatencyEnd   = "# CF-YX-LATENCY-END"
	legacyBandwidthBegin = "# CF-YX-BANDWIDTH-BEGIN"
	legacyBandwidthEnd   = "# CF-YX-BANDWIDTH-END"
)

type hostsStage struct {
	Changed  bool
	Backup   string
	Rollback func() error
}

func managedMarkerPairs() map[string]string {
	return map[string]string{
		hostsBegin: hostsEnd,
		legacyLatencyBegin: legacyLatencyEnd,
		legacyBandwidthBegin: legacyBandwidthEnd,
	}
}

func validateManagedMarkers(content string) error {
	pairs := managedMarkerPairs()
	endToBegin := make(map[string]string, len(pairs))
	for b, e := range pairs { endToBegin[e] = b }
	countBegin := make(map[string]int)
	countEnd := make(map[string]int)
	active := ""
	for lineNo, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		s := strings.TrimSpace(line)
		if end, ok := pairs[s]; ok {
			countBegin[s]++
			if countBegin[s] > 1 { return fmt.Errorf("duplicate Hosts marker %q", s) }
			if active != "" { return fmt.Errorf("nested Hosts marker at line %d", lineNo+1) }
			active = end
			continue
		}
		if begin, ok := endToBegin[s]; ok {
			countEnd[s]++
			if countEnd[s] > 1 { return fmt.Errorf("duplicate Hosts marker %q", s) }
			if active == "" || active != s { return fmt.Errorf("unmatched Hosts marker %q at line %d (begin %q)", s, lineNo+1, begin) }
			active = ""
		}
	}
	if active != "" { return fmt.Errorf("missing Hosts end marker %q", active) }
	for b, e := range pairs {
		if countBegin[b] != countEnd[e] { return fmt.Errorf("incomplete Hosts marker pair %q / %q", b, e) }
	}
	return nil
}

func stripManagedBlocks(content string) (string, error) {
	if err := validateManagedMarkers(content); err != nil { return "", err }
	pairs := managedMarkerPairs()
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	activeEnd := ""
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if activeEnd != "" {
			if s == activeEnd { activeEnd = "" }
			continue
		}
		if end, ok := pairs[s]; ok {
			activeEnd = end
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), nil
}

func canonicalUnmanaged(content string) (string, error) {
	s, err := stripManagedBlocks(content)
	if err != nil { return "", err }
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" { lines = lines[:len(lines)-1] }
	return strings.Join(lines, "\n"), nil
}

func renderHosts(existing string, mappings map[string]string) (string, error) {
	base, err := stripManagedBlocks(existing)
	if err != nil { return "", err }
	out := strings.Split(base, "\n")
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" { out = out[:len(out)-1] }
	if len(mappings) > 0 {
		out = append(out, "", hostsBegin)
		hosts := make([]string, 0, len(mappings))
		for host, ip := range mappings {
			if err := validateDomainHost(host); err != nil { return "", err }
			if net.ParseIP(ip) == nil { return "", fmt.Errorf("invalid mapping IP for %s: %s", host, ip) }
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		for _, host := range hosts { out = append(out, mappings[host]+" "+host) }
		out = append(out, hostsEnd)
	}
	out = append(out, "")
	return strings.Join(out, "\n"), nil
}

// The comparison path must come from a directory bind: a second file bind can
// itself remain attached to the same stale inode after the host replaces it.
func hostsBindingStatus(path string) (string, error) {
	check := strings.TrimSpace(os.Getenv("HOSTS_VERIFY_PATH"))
	if check == "" {
		if filepath.Clean(path) == "/host/etc/hosts" {
			return "unverified", errors.New("HOSTS_VERIFY_PATH is required for the host Hosts file")
		}
		return "unverified", nil
	}
	bound, err := os.Stat(path)
	if err != nil { return "error", fmt.Errorf("stat Hosts binding: %w", err) }
	actual, err := os.Stat(check)
	if err != nil { return "error", fmt.Errorf("stat host Hosts path: %w", err) }
	if !os.SameFile(bound, actual) {
		return "diverged", errors.New("Hosts binding differs from the current host file; refusing apply or sync")
	}
	return "verified", nil
}

func (a *App) stageApplyMappings(cfg Config, mappings map[string]string) (hostsStage, error) {
	stage, err := a.stageHostsUpdate(cfg, func(current string) (string, error) {
		rendered, err := renderHosts(current, mappings)
		if err != nil { return "", err }
		before, err := canonicalUnmanaged(current)
		if err != nil { return "", err }
		after, err := canonicalUnmanaged(rendered)
		if err != nil { return "", err }
		if before != after { return "", errors.New("unmanaged Hosts content changed") }
		return rendered, nil
	})
	if err == nil && stage.Changed { a.appendLog("hosts applied: %d mappings", len(mappings)) }
	return stage, err
}

func (a *App) stageHostsUpdate(cfg Config, render func(string) (string, error)) (hostsStage, error) {
	if _, err := hostsBindingStatus(cfg.HostsPath); err != nil { return hostsStage{}, err }
	hostsPath := cfg.HostsPath
	info, err := os.Lstat(hostsPath)
	if err != nil { return hostsStage{}, fmt.Errorf("read hosts metadata: %w", err) }
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() { return hostsStage{}, errors.New("Hosts path must be a regular file") }
	current, err := readHostsSnapshot(hostsPath, info)
	if err != nil { return hostsStage{}, fmt.Errorf("read hosts: %w", err) }
	rendered, err := render(string(current))
	if err != nil { return hostsStage{}, err }
	if rendered == string(current) {
		if err := verifyHostsSnapshot(hostsPath, info, current); err != nil { return hostsStage{}, err }
		return hostsStage{Changed:false, Rollback:func() error{return nil}}, nil
	}

	backup := filepath.Join(a.dataDir, "hosts-backup-"+time.Now().Format("20060102-150405.000000000"))
	if err := os.WriteFile(backup, current, info.Mode().Perm()); err != nil { return hostsStage{}, fmt.Errorf("backup hosts: %w", err) }
	restore := func() error {
		// Do not restore a stale snapshot over a newly replaced file or an
		// external update that occurred after our successful write.
		if err := writeHostsInPlaceExpected(hostsPath, []byte(rendered), current, info); err != nil { return err }
		return verifyHostsSnapshot(hostsPath, info, current)
	}
	if err := writeHostsInPlaceExpected(hostsPath, current, []byte(rendered), info); err != nil {
		return hostsStage{}, fmt.Errorf("write hosts: %w", err)
	}
	if err := verifyHostsSnapshot(hostsPath, info, []byte(rendered)); err != nil {
		if rollbackErr := restore(); rollbackErr != nil {
			return hostsStage{}, fmt.Errorf("%v; rollback refused or failed: %w", err, rollbackErr)
		}
		return hostsStage{}, err
	}
	a.pruneHostsBackups(cfg.Hosts.BackupRetention)
	return hostsStage{Changed:true, Backup:backup, Rollback:restore}, nil
}

// Compare the descriptor, the current pathname, and the independent host
// directory binding. Checking two pathnames alone is insufficient: both may
// point at a new inode after a NAS network or startup event.
func checkHostsIdentity(path string, f *os.File, expected os.FileInfo) error {
	actual, err := f.Stat()
	if err != nil { return err }
	named, err := os.Lstat(path)
	if err != nil { return err }
	if !actual.Mode().IsRegular() || !named.Mode().IsRegular() || !os.SameFile(expected, actual) || !os.SameFile(actual, named) {
		return errors.New("Hosts inode changed; refusing to write or restore")
	}
	if _, err := hostsBindingStatus(path); err != nil { return err }
	return nil
}

func readHostsSnapshot(path string, expected os.FileInfo) ([]byte,error) {
	f,err:=os.Open(path)
	if err!=nil { return nil,err }
	defer f.Close()
	if err:=checkHostsIdentity(path,f,expected); err!=nil { return nil,err }
	b,err:=io.ReadAll(f)
	if err!=nil { return nil,err }
	if err:=checkHostsIdentity(path,f,expected); err!=nil { return nil,err }
	return b,nil
}

func verifyHostsSnapshot(path string, expected os.FileInfo, content []byte) error {
	got,err:=readHostsSnapshot(path,expected)
	if err!=nil { return err }
	if !bytes.Equal(got,content) { return errors.New("Hosts contents changed; refusing stale snapshot") }
	return nil
}

// Never rename/unlink/create the live Hosts. Open once without O_TRUNC,
// validate the snapshot, then truncate/write/fsync that same descriptor.
// Any rollback uses the same inode, never the new pathname if QNAP replaced it.
func writeHostsInPlaceExpected(path string, before, content []byte, expected os.FileInfo) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil { return err }
	defer f.Close()
	if err:=checkHostsIdentity(path,f,expected); err!=nil { return err }
	got,err:=io.ReadAll(f)
	if err!=nil { return err }
	if !bytes.Equal(got,before) { return errors.New("Hosts contents changed before write") }
	if err:=checkHostsIdentity(path,f,expected); err!=nil { return err }
	write := func(b []byte) error {
		if _,err:=f.Seek(0,io.SeekStart); err!=nil { return err }
		if err:=f.Truncate(0); err!=nil { return err }
		if n,err:=f.Write(b); err!=nil { return err } else if n!=len(b) { return io.ErrShortWrite }
		if err:=f.Sync(); err!=nil { return err }
		if _,err:=f.Seek(0,io.SeekStart); err!=nil { return err }
		actual,err:=io.ReadAll(f)
		if err!=nil { return err }
		if !bytes.Equal(actual,b) { return errors.New("Hosts write verification failed") }
		return nil
	}
	if err:=write(content); err!=nil {
		if identityErr:=checkHostsIdentity(path,f,expected); identityErr!=nil {
			return fmt.Errorf("%v; rollback refused: %w",err,identityErr)
		}
		if rollbackErr:=write(before); rollbackErr!=nil {
			return fmt.Errorf("%v; rollback failed: %w",err,rollbackErr)
		}
		if identityErr:=checkHostsIdentity(path,f,expected); identityErr!=nil {
			return fmt.Errorf("%v; rollback identity check failed: %w",err,identityErr)
		}
		return err
	}
	if err:=checkHostsIdentity(path,f,expected); err!=nil { return err }
	return f.Close()
}

func writeHostsInPlace(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil { return err }
	if _, err := f.Write(content); err != nil { _ = f.Close(); return err }
	if err := f.Sync(); err != nil { _ = f.Close(); return err }
	return f.Close()
}

func (a *App) pruneHostsBackups(keep int) {
	if keep < 1 { return }
	matches, _ := filepath.Glob(filepath.Join(a.dataDir, "hosts-backup-*"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for i := keep; i < len(matches); i++ { _ = os.Remove(matches[i]) }
}

func (a *App) applyMappings(cfg Config, mappings map[string]string) error {
	_, err := a.stageApplyMappings(cfg, mappings)
	return err
}
