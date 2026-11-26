package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

type resolutionSnapshot struct {
	Mappings         map[string]string
	DomainStatus     map[string]string
	DomainHealth     map[string]DomainHealth
	TrackerKeepalive map[string]TrackerKeepaliveRuntime
	NextRefresh      string
	LastOptimize     string
	ResolutionAttempt ResolutionAttempt
	HostsClearedAt string
	HostsBackup *HostsRecoveryBackup
}

// Validation evidence is separate from the mappings actually applied to Hosts.
type ResolutionAttempt struct {
	CheckedAt string `json:"checkedAt,omitempty"`
	Applied bool `json:"applied"`
	Error string `json:"error,omitempty"`
	DomainStatus map[string]string `json:"domainStatus,omitempty"`
}

// Every maintenance exit reconciles downloader recovery only after committing
// the mappings. This includes retained-current and single-domain success.
func (a *App) commitMaintenanceResolution(ctx context.Context, cfg Config, samples map[string]TrackerSample, before, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth, nextRefresh string, optimized bool) error {
	if err := a.commitResolution(ctx, cfg, mappings, statuses, health, nextRefresh, optimized); err != nil { return err }
	a.reconcileTransmissionTrackerKeepaliveAfterRepair(ctx, cfg, samples, before, mappings)
	return nil
}

func (a *App) snapshotResolution() resolutionSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	statuses := make(map[string]string, len(a.state.DomainStatus))
	for k, v := range a.state.DomainStatus {
		statuses[k] = v
	}
	return resolutionSnapshot{
		Mappings:         copyMappings(a.state.Mappings),
		DomainStatus:     statuses,
		DomainHealth:     copyHealth(a.state.DomainHealth),
		TrackerKeepalive: copyTrackerKeepalive(a.state.TrackerKeepalive),
		NextRefresh:      a.state.NextRefresh,
		LastOptimize:     a.state.LastOptimize,
		ResolutionAttempt: a.state.ResolutionAttempt,
		HostsClearedAt: a.state.HostsClearedAt,
		HostsBackup: a.state.HostsBackup,
	}
}

func (a *App) restoreResolution(s resolutionSnapshot) {
	a.mu.Lock()
	a.state.Mappings = s.Mappings
	a.state.DomainStatus = s.DomainStatus
	a.state.DomainHealth = s.DomainHealth
	a.state.TrackerKeepalive = s.TrackerKeepalive
	a.state.NextRefresh = s.NextRefresh
	a.state.LastOptimize = s.LastOptimize
	a.state.ResolutionAttempt = s.ResolutionAttempt
	a.state.HostsClearedAt = s.HostsClearedAt
	a.state.HostsBackup = s.HostsBackup
	a.mu.Unlock()
}

func (a *App) persistStateStrict() error {
	// Serialize writers before taking a snapshot so an older snapshot cannot
	// overwrite a newer one. Encode under the read lock: maps and slices in the
	// struct copy still share storage with the live state.
	a.persistMu.Lock()
	defer a.persistMu.Unlock()
	a.mu.RLock()
	s := a.state
	s.Running = false
	s.CurrentJob = ""
	s.CurrentDomain = ""
	s.Progress = JobProgress{}
	b, err := json.MarshalIndent(s, "", "  ")
	a.mu.RUnlock()
	if err != nil {
		return err
	}
	return writeJSONBytes(filepath.Join(a.dataDir, "state.json"), b)
}

func (a *App) commitResolution(ctx context.Context, cfg Config, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth, nextRefresh string, optimized bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("resolution commit blocked by expired job context: %w", err)
	}
	for _, d := range cfg.Domains {
		if d.Enabled && maintenancePaused(d, health) {
			statuses[d.Host] = maintenancePauseStatus(health[d.Host])
		}
	}
	old := a.snapshotResolution()
	a.mu.Lock()
	a.state.Mappings = mappings
	a.state.DomainStatus = statuses
	a.state.DomainHealth = health
	a.state.NextRefresh = nextRefresh
	for host, keepalive := range a.state.TrackerKeepalive {
		if d, ok := configuredDomain(cfg, host); ok && maintenancePaused(d, health) {
			keepalive.Status = "paused"
			keepalive.NextCheck = ""
			keepalive.NextReannounce = ""
			keepalive.LastEvent = maintenancePauseStatus(health[host])
			a.state.TrackerKeepalive[host] = keepalive
			continue
		}
		if keepalive.RejectedIP == "" {
			continue
		}
		if newIP := mappings[host]; newIP != "" && newIP != keepalive.RejectedIP {
			oldRejected := keepalive.RejectedIP
			a.state.TrackerKeepalive[host] = TrackerKeepaliveRuntime{
				SampleTestAt:     keepalive.SampleTestAt,
				MappingIP:        newIP,
				MappingChangedAt: time.Now().Format(time.RFC3339),
				Status:           "healthy",
				LastEvent:        fmt.Sprintf("mapping changed from rejected %s to %s; rejected IP cleared", oldRejected, newIP),
			}
		}
	}
	if optimized {
		a.state.LastOptimize = time.Now().Format(time.RFC3339)
	}
	a.mu.Unlock()

	stage := hostsStage{Rollback: func() error { return nil }}
	var err error
	if cfg.AutoApply {
		stage, err = a.stageApplyMappings(cfg, mappings)
		if err != nil {
			a.restoreResolution(old)
			a.retainFailedApplyObservations(old, cfg, mappings, statuses, health, nextRefresh, err)
			a.blockHostsMaintenance(err, time.Now())
			if saveErr := a.persistStateStrict(); saveErr != nil {
				return fmt.Errorf("%v; failed to persist maintenance pause and observations: %w", err, saveErr)
			}
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		rollbackErr := stage.Rollback()
		a.restoreResolution(old)
		if rollbackErr != nil {
			return fmt.Errorf("job context expired before state commit: %v; Hosts rollback also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("job context expired before state commit; Hosts restored: %w", err)
	}
	a.mu.Lock()
	a.state.ResolutionAttempt = ResolutionAttempt{CheckedAt: time.Now().Format(time.RFC3339), Applied: true}
	if cfg.AutoApply {
		a.state.HostsClearedAt = ""
		a.state.HostsBackup = newHostsBackup(mappings)
	}
	a.mu.Unlock()
	if err := a.persistStateStrict(); err != nil {
		rollbackErr := stage.Rollback()
		a.restoreResolution(old)
		if rollbackErr != nil {
			return fmt.Errorf("state write failed: %v; Hosts rollback also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("state write failed; Hosts restored: %w", err)
	}
	if cfg.AutoApply { a.mirrorHostsBackup() }
	if cfg.AutoApply && cfg.Sync.Enabled {
		syncCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := a.publishSync(syncCtx, cfg, false); err != nil {
			a.appendLog("github sync failed: %v", err)
		}
		cancel()
	}
	return nil
}

func (a *App) retainFailedApplyObservations(old resolutionSnapshot, cfg Config, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth, nextRefresh string, applyErr error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.ResolutionAttempt = ResolutionAttempt{CheckedAt: time.Now().Format(time.RFC3339), Error: applyErr.Error(), DomainStatus: copyStatuses(statuses)}
	for host, h := range health {
		previous := old.DomainHealth[host]
		// A successful probe of an unapplied replacement must not mark the old
		// mapping healthy or clear its downloader rejection. Failures and checks
		// of unchanged mappings remain valid observations even if apply fails.
		if mappings[host] == old.Mappings[host] || (h.LastFailure != "" && h.LastFailure != previous.LastFailure) {
			a.state.DomainHealth[host] = h
		}
	}
	for _, d := range cfg.Domains {
		if !d.Enabled || maintenancePaused(d, a.state.DomainHealth) { continue }
		a.state.DomainStatus[d.Host] = "unresolved · Hosts apply failed · " + statuses[d.Host]
	}
	a.state.NextRefresh = nextRefresh
}
