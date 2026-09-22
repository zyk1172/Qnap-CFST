package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

type resolutionSnapshot struct {
	Mappings     map[string]string
	DomainStatus map[string]string
	DomainHealth map[string]DomainHealth
	NextRefresh  string
	LastOptimize string
}

func (a *App) snapshotResolution() resolutionSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	statuses := make(map[string]string, len(a.state.DomainStatus))
	for k,v := range a.state.DomainStatus { statuses[k]=v }
	return resolutionSnapshot{
		Mappings: copyMappings(a.state.Mappings),
		DomainStatus: statuses,
		DomainHealth: copyHealth(a.state.DomainHealth),
		NextRefresh: a.state.NextRefresh,
		LastOptimize: a.state.LastOptimize,
	}
}

func (a *App) restoreResolution(s resolutionSnapshot) {
	a.mu.Lock()
	a.state.Mappings = s.Mappings
	a.state.DomainStatus = s.DomainStatus
	a.state.DomainHealth = s.DomainHealth
	a.state.NextRefresh = s.NextRefresh
	a.state.LastOptimize = s.LastOptimize
	a.mu.Unlock()
}

func (a *App) persistStateStrict() error {
	a.mu.RLock()
	s := a.state
	s.Running = false
	s.CurrentJob = ""
	s.CurrentDomain = ""
	a.mu.RUnlock()
	return writeJSON(filepath.Join(a.dataDir, "state.json"), s)
}

func (a *App) commitResolution(ctx context.Context, cfg Config, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth, nextRefresh string, optimized bool) error {
	old := a.snapshotResolution()
	a.mu.Lock()
	a.state.Mappings = mappings
	a.state.DomainStatus = statuses
	a.state.DomainHealth = health
	a.state.NextRefresh = nextRefresh
	if optimized { a.state.LastOptimize = time.Now().Format(time.RFC3339) }
	a.mu.Unlock()

	stage := hostsStage{Rollback:func() error{return nil}}
	var err error
	if cfg.AutoApply {
		stage, err = a.stageApplyMappings(cfg, mappings)
		if err != nil {
			a.restoreResolution(old)
			return err
		}
	}
	if err := a.persistStateStrict(); err != nil {
		rollbackErr := stage.Rollback()
		a.restoreResolution(old)
		if rollbackErr != nil {
			return fmt.Errorf("state write failed: %v; Hosts rollback also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("state write failed; Hosts restored: %w", err)
	}
	if cfg.AutoApply && cfg.Sync.Enabled {
		if err := a.publishSync(ctx, cfg, false); err != nil { a.appendLog("github sync failed: %v", err) }
	}
	return nil
}
