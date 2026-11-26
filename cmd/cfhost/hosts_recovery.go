package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"time"
)

type HostsRecoveryBackup struct {
	Mappings map[string]string `json:"mappings"`
	AppliedAt string `json:"appliedAt"`
}

func newHostsBackup(mappings map[string]string) *HostsRecoveryBackup {
	return &HostsRecoveryBackup{Mappings: copyMappings(mappings), AppliedAt: time.Now().Format(time.RFC3339Nano)}
}

// The committed state is authoritative; this named persistent backup mirrors
// it, rather than copying obsolete NAS hostname/network configuration.
func (a *App) mirrorHostsBackup() {
	a.mu.RLock()
	backup := a.state.HostsBackup
	a.mu.RUnlock()
	if backup == nil { return }
	path := filepath.Join(a.dataDir, "hosts-last-applied.json")
	if old, err := os.ReadFile(path); err == nil {
		var saved HostsRecoveryBackup
		if json.Unmarshal(old, &saved) == nil && saved.AppliedAt == backup.AppliedAt && maps.Equal(saved.Mappings, backup.Mappings) { return }
	}
	if err := writeJSON(path, backup); err != nil { a.appendLog("Hosts persistent backup failed: %v", err) }
}

func recoveryMappings(cfg Config, backup *HostsRecoveryBackup) map[string]string {
	mappings := map[string]string{}
	if backup == nil { return mappings }
	for _, d := range cfg.Domains {
		if d.Enabled {
			if ip := backup.Mappings[d.Host]; ip != "" { mappings[d.Host] = ip }
		}
	}
	return mappings
}

func (a *App) recoverHosts() {
	// Same lock order as clear; avoid racing a saved autoApply change.
	a.configMu.Lock()
	defer a.configMu.Unlock()
	if !a.beginHostsOperation("hosts-recovery") { return }
	defer func(){
		a.mu.Lock()
		a.state.Running = false
		a.state.CurrentJob = ""
		a.state.Progress = JobProgress{}
		a.mu.Unlock()
	}()
	cfg := a.snapshotConfig()
	a.mu.RLock()
	backup, cleared := a.state.HostsBackup, a.state.HostsClearedAt != ""
	previous := copyMappings(a.state.Mappings)
	a.mu.RUnlock()
	if cleared && backup != nil && len(backup.Mappings) == 0 {
		a.syncContainerHosts(cfg, map[string]string{})
		return
	}
	if !cfg.AutoApply || cleared { a.refreshContainerInventory(); return }
	if _, err := hostsBindingStatus(cfg.HostsPath); err != nil { a.refreshContainerInventory(); return }
	current, err := os.ReadFile(cfg.HostsPath)
	if err != nil { return }
	// Upgrade only from mappings that actually exist in Hosts. An unapplied
	// candidate or a previous manually cleared state must not be resurrected.
	if backup == nil && len(previous) > 0 {
		configured := map[string]bool{}
		for host := range previous { configured[host] = true }
		found, parseErr := mappingsFromManagedBlocks(string(current), configured)
		if parseErr == nil && maps.Equal(found, previous) {
			backup = newHostsBackup(previous)
			a.mu.Lock(); a.state.HostsBackup = backup; a.mu.Unlock()
			if err := a.persistStateStrict(); err != nil {
				a.mu.Lock(); a.state.HostsBackup = nil; a.mu.Unlock()
				a.appendLog("Hosts backup migration failed: %v", err)
				return
			}
		}
	}
	if backup == nil { a.refreshContainerInventory(); return }
	a.mirrorHostsBackup()
	mappings := recoveryMappings(cfg, backup)
	// Check the exact descriptor on restore, including the unchanged path.
	// Do not use the backup's inode: it belongs to a different file.
	stage, err := a.stageHostsUpdate(cfg, func(current string) (string, error) {
		// Recovery only touches CFHost's marker, preserving system records.
		return renderContainerHosts(current, mappings)
	})
	if err != nil { a.appendLog("Hosts recovery failed: %v", err); return }
	if stage.Changed { a.appendLog("Hosts recovered from persistent backup: %d mappings; live inode preserved", len(mappings)) }
	if err := a.preflightMaintenanceHosts(cfg, time.Now(), true); err != nil { return }
	a.syncContainerHosts(cfg, mappings)
}
