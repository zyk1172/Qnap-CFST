package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"time"
)

// Store cleanup intent in the same config transaction that disables a target.
// A stopped/missing container remains pending across CFHost/NAS restarts.
// Reselecting a target cancels its pending cleanup.
func reconcileContainerCleanup(old HostsConfig, selected []string) []string {
	pending := map[string]bool{}
	for _, name := range old.ContainerCleanup { pending[name] = true }
	for _, name := range old.ContainerTargets { pending[name] = true }
	for _, name := range selected { delete(pending, name) }
	result := make([]string, 0, len(pending))
	for name := range pending { result = append(result, name) }
	sort.Strings(result)
	return result
}

func removeContainerTarget(cfg *Config, name string) {
	old := cfg.Hosts
	selected := make([]string, 0, len(old.ContainerTargets))
	for _, target := range old.ContainerTargets { if target != name { selected = append(selected, target) } }
	cfg.Hosts.ContainerTargets = selected
	cfg.Hosts.ContainerCleanup = reconcileContainerCleanup(old, selected)
	if !slices.Contains(cfg.Hosts.ContainerCleanup, name) {
		cfg.Hosts.ContainerCleanup = append(cfg.Hosts.ContainerCleanup, name)
		sort.Strings(cfg.Hosts.ContainerCleanup)
	}
}

func (a *App) handleClearContainerHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
	if !dockerHostsEnabled() { http.Error(w, "Docker Hosts targets disabled", http.StatusConflict); return }
	var req struct { Name string `json:"name"` }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || !validContainerName(req.Name) {
		http.Error(w, "invalid container name", http.StatusBadRequest); return
	}
	a.configMu.Lock()
	defer a.configMu.Unlock()
	a.containerHostsMu.Lock()
	defer a.containerHostsMu.Unlock()
	cfg := a.snapshotConfig()
	// Accept known containers or existing cleanup/selection entries only.
	known := slices.Contains(cfg.Hosts.ContainerTargets, req.Name) || slices.Contains(cfg.Hosts.ContainerCleanup, req.Name)
	a.mu.RLock()
	for _, item := range a.state.ContainerHosts { if item.Name == req.Name { known = true; break } }
	a.mu.RUnlock()
	if !known || req.Name == getenv("CFHOST_CONTAINER_NAME", "cfhost") { http.Error(w, "unknown or excluded container", http.StatusBadRequest); return }
	removeContainerTarget(&cfg, req.Name)
	if err := saveConfig(a.dataDir, cfg); err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }
	a.mu.Lock(); a.config = cfg; a.mu.Unlock()
	a.appendLog("container Hosts cleanup queued: %s", req.Name)
	w.WriteHeader(http.StatusAccepted)
	writeResponse(w, map[string]string{"status":"pending", "name":req.Name})
	go a.retryContainerCleanup()
}

// This is independent of host binding, autoApply and CFST/Tracker scheduling.
// Serialize with container synchronization and config saves to prevent a late
// sync from repopulating a just-cleared container.
func (a *App) retryContainerCleanup() {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	a.containerHostsMu.Lock()
	defer a.containerHostsMu.Unlock()
	cfg := a.snapshotConfig()
	if !dockerHostsEnabled() || len(cfg.Hosts.ContainerCleanup)==0 { return }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	list, err := listContainerHosts(ctx)
	cancel()
	if err != nil { a.setContainerInventory(nil, err); return }
	pending := map[string]bool{}
	for _, name := range cfg.Hosts.ContainerCleanup { pending[name] = true }
	// Bound each pass; a large queue cannot monopolize the maintenance slot.
	attempts := 0
	for i := range list {
		name := list[i].Name
		if !pending[name] { continue }
		list[i].Status = "cleanup pending"
		if list[i].State != "running" || attempts >= 4 { continue }
		attempts++
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := updateContainerHosts(ctx, list[i].ID, nil)
		cancel()
		if err != nil {
			list[i].Status = "cleanup pending: " + err.Error()
			continue
		}
		delete(pending,name)
		list[i].Status = "CFHost records cleared"
		a.appendLog("container Hosts cleared: %s", name)
	}
	remaining := make([]string,0,len(pending))
	for name := range pending { remaining = append(remaining,name) }
	sort.Strings(remaining)
	if !slices.Equal(remaining,cfg.Hosts.ContainerCleanup) {
		cfg.Hosts.ContainerCleanup = remaining
		if err := saveConfig(a.dataDir,cfg); err != nil {
			a.appendLog("container Hosts cleanup result persistence failed: %v",err)
			return // retry idempotently after persistence becomes available
		}
		a.mu.Lock(); a.config = cfg; a.mu.Unlock()
	}
	a.setContainerInventory(list,nil)
}
