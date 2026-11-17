package main

import (
	"context"
	"errors"
	"sort"
	"time"
)

// CFST owns no resolution/Hosts job slot. It only publishes candidate evidence;
// the ordinary serialized maintenance job applies mappings afterwards.
type CandidateRefreshRuntime struct {
	Requested     bool     `json:"requested"`
	Running       bool     `json:"running"`
	Optimize      bool     `json:"optimize,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Targets       []string `json:"targets,omitempty"`
	ReadyTargets  []string `json:"readyTargets,omitempty"`
	ReadyOptimize bool     `json:"readyOptimize,omitempty"`
	RequestedAt   string   `json:"requestedAt,omitempty"`
	StartedAt     string   `json:"startedAt,omitempty"`
	FinishedAt    string   `json:"finishedAt,omitempty"`
	LastError     string   `json:"lastError,omitempty"`
}

type cfstTriggerKey struct{}

func (a *App) requestCandidateRefresh(reason string, targets []string, optimize bool) error {
	a.mu.Lock()
	r := a.state.CandidateRefresh
	if !r.Requested {
		r.RequestedAt = time.Now().Format(time.RFC3339Nano)
		r.LastError = ""
		r.Reason = reason
	}
	r.Requested = true
	r.Optimize = r.Optimize || optimize
	seen := map[string]bool{}
	for _, host := range append(append([]string(nil), r.Targets...), targets...) {
		seen[host] = true
	}
	r.Targets = nil
	for host := range seen {
		r.Targets = append(r.Targets, host)
	}
	sort.Strings(r.Targets)
	a.state.CandidateRefresh = r
	a.mu.Unlock()
	return a.persistStateStrict()
}

func (a *App) requestAutomaticOptimize(cfg Config) error {
	if err := a.preflightMaintenanceHosts(cfg, time.Now(), true); err != nil {
		return err
	}
	a.mu.RLock()
	next := cfstNextAllowed(a.state, cfg)
	active := []string{}
	for _, d := range cfg.Domains {
		if d.Enabled && !isFollowDomain(d) && !maintenancePaused(d, a.state.DomainHealth) && domainCheckDue(a.state.DomainHealth[d.Host], time.Now()) {
			active = append(active, d.Host)
		}
	}
	a.mu.RUnlock()
	a.mu.Lock()
	a.state.LastOptimizeAttempt = time.Now().Format(time.RFC3339)
	a.mu.Unlock()
	if len(active) == 0 {
		a.appendLog("scheduled optimize skipped: no active independent domains")
		return nil
	}
	if time.Now().Before(next) {
		return &cfstDeferredError{Until: next}
	}
	if err := a.requestCandidateRefresh("scheduled full optimize", active, true); err != nil {
		return err
	}
	a.appendLog("scheduled optimize: candidate refresh queued; verification will continue in automatic repair slices")
	return nil
}

func (a *App) startCandidateRefresh(now time.Time) bool {
	cfg := a.snapshotConfig()
	a.mu.RLock()
	r := a.state.CandidateRefresh
	busy := a.state.Running
	a.mu.RUnlock()
	if !r.Requested || r.Running || busy || (!cfg.AutoRepair && !(r.Optimize && cfg.Optimize.ScheduledFull)) {
		return false
	}
	if err := a.preflightMaintenanceHosts(cfg, now, false); err != nil {
		return false
	}
	a.mu.Lock()
	r = a.state.CandidateRefresh
	active := map[string]bool{}
	for _, d := range cfg.Domains {
		if d.Enabled && !isFollowDomain(d) && !maintenancePaused(d, a.state.DomainHealth) {
			active[d.Host] = true
		}
	}
	filtered := []string{}
	for _, host := range r.Targets {
		if active[host] {
			filtered = append(filtered, host)
		}
	}
	if len(active) == 0 || (len(r.Targets) > 0 && len(filtered) == 0) {
		r.Requested = false
		r.Optimize = false
		r.Targets = nil
		a.state.CandidateRefresh = r
		a.mu.Unlock()
		a.persistState()
		return false
	}
	r.Targets = filtered
	if !cfg.Optimize.ScheduledFull {
		r.Optimize = false
	}
	if !r.Requested || r.Running || a.state.Running || now.Before(cfstNextAllowedAt(a.state, cfg, now)) {
		a.mu.Unlock()
		return false
	}
	r.Running = true
	r.StartedAt = now.Format(time.RFC3339Nano)
	a.state.CandidateRefresh = r
	a.mu.Unlock()
	go a.finishCandidateRefresh(cfg, r)
	return true
}

func (a *App) finishCandidateRefresh(cfg Config, request CandidateRefreshRuntime) {
	started := time.Now()
	trigger := "automatic candidates"
	if request.Optimize {
		trigger = "scheduled optimize candidates"
	}
	ctx := context.WithValue(context.Background(), cfstTriggerKey{}, trigger)
	candidates, err := a.runCFSTFor(ctx, cfg, request.Reason)
	if err == nil {
		a.storeCandidates(candidates, cfg)
	}
	finished := time.Now()
	a.mu.Lock()
	r := a.state.CandidateRefresh
	r.FinishedAt = finished.Format(time.RFC3339Nano)
	optimizationDisabled := r.Optimize && !a.config.Optimize.ScheduledFull
	completedOptimize := r.Optimize && !optimizationDisabled
	var deferred *cfstDeferredError
	if !errors.As(err, &deferred) {
		r.Requested = false
		r.Optimize = false
	}
	if err != nil {
		r.LastError = err.Error()
	} else {
		r.LastError = ""
	}
	if err == nil {
		ready := map[string]bool{}
		for _, host := range r.ReadyTargets {
			ready[host] = true
		}
		for _, host := range r.Targets {
			if optimizationDisabled && a.state.DomainHealth[host].FailureStreak == 0 {
				continue
			}
			d, ok := configuredDomain(a.config, host)
			if !ok || !d.Enabled || isFollowDomain(d) || maintenancePaused(d, a.state.DomainHealth) {
				continue
			}
			// A manual/config change made while CFST ran must not be undone by a
			// stale automatic request. Candidate evidence itself remains reusable.
			oldDomain, existed := configuredDomain(cfg, host)
			ip := a.state.Mappings[host]
			if !existed || automaticWorkKey(cfg, oldDomain, ip) != automaticWorkKey(a.config, d, ip) {
				continue
			}
			lastSuccess, _ := time.Parse(time.RFC3339Nano, a.state.DomainHealth[host].LastSuccess)
			requestedAt, _ := time.Parse(time.RFC3339Nano, request.RequestedAt)
			if !completedOptimize && lastSuccess.After(requestedAt) {
				continue
			}
			ready[host] = true
		}
		r.ReadyTargets = nil
		for host := range ready {
			r.ReadyTargets = append(r.ReadyTargets, host)
		}
		sort.Strings(r.ReadyTargets)
		if completedOptimize && len(r.ReadyTargets) > 0 {
			r.ReadyOptimize = true
		}
	}
	if !r.Requested {
		r.Targets = nil
	}
	a.state.CandidateRefresh = r
	record := RunRecord{Kind: "reserve", StartedAt: started.Format(time.RFC3339), FinishedAt: finished.Format(time.RFC3339), DurationMS: finished.Sub(started).Milliseconds(), Success: err == nil, FullRefresh: true, MappingsBefore: len(a.state.Mappings), MappingsAfter: len(a.state.Mappings), CandidateCount: len(a.state.Candidates), Stages: map[string]int64{"cfst": finished.Sub(started).Milliseconds()}}
	if err != nil {
		record.Error = err.Error()
	}
	a.state.History = append(a.state.History, record)
	if len(a.state.History) > 200 {
		a.state.History = a.state.History[len(a.state.History)-200:]
	}
	a.mu.Unlock()
	if err != nil {
		a.appendLog("candidate refresh ended: %v", err)
	} else {
		a.appendLog("candidate refresh completed: %d observations; domain repair may continue; Hosts unchanged", len(candidates))
	}
	if persistErr := a.persistStateStrict(); persistErr != nil {
		a.appendLog("candidate refresh persist failed: %v", persistErr)
	}
	a.mu.Lock()
	a.state.CandidateRefresh.Running = false
	a.mu.Unlock()
}

func (a *App) cancelAutomaticWork(hosts map[string]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for host := range hosts {
		delete(a.state.AutomaticRepair.Pending, host)
	}
	if len(a.state.AutomaticRepair.Pending) == 0 {
		a.state.AutomaticRepair.NextCheck = ""
		a.state.AutomaticRepair.Optimize = false
	}
	targets := []string{}
	for _, host := range a.state.CandidateRefresh.Targets {
		if !hosts[host] {
			targets = append(targets, host)
		}
	}
	a.state.CandidateRefresh.Targets = targets
	ready := []string{}
	for _, host := range a.state.CandidateRefresh.ReadyTargets {
		if !hosts[host] {
			ready = append(ready, host)
		}
	}
	a.state.CandidateRefresh.ReadyTargets = ready
	if len(ready) == 0 {
		a.state.CandidateRefresh.ReadyOptimize = false
	}
	if len(targets) == 0 {
		a.state.CandidateRefresh.Optimize = false
		if !a.state.CandidateRefresh.Running {
			a.state.CandidateRefresh.Requested = false
		}
	}
}
