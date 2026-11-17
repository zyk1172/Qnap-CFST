package main

import (
	"context"
	"sync"
	"time"
)

type manualOperationKey struct{}
type manualOperation struct {
	mu        sync.Mutex
	announced map[int]bool
}

func withManualOperation(ctx context.Context) context.Context {
	return context.WithValue(ctx, manualOperationKey{}, &manualOperation{announced: map[int]bool{}})
}

func isManualOperation(ctx context.Context) bool {
	_, ok := ctx.Value(manualOperationKey{}).(*manualOperation)
	return ok
}

func manualConfig(ctx context.Context, cfg Config) Config {
	if isManualOperation(ctx) {
		cfg.Verify.CandidateLimit = 0
		cfg.Repair.FailureThreshold = 1
	}
	return cfg
}

func jobMaintenancePaused(ctx context.Context, d Domain, health map[string]DomainHealth) bool {
	return !isManualOperation(ctx) && maintenancePaused(d, health)
}

func jobDomainCheckDue(ctx context.Context, h DomainHealth, now time.Time) bool {
	return isManualOperation(ctx) || domainCheckDue(h, now)
}

func (a *App) jobMaintenanceConfig(ctx context.Context, cfg Config) Config {
	if isManualOperation(ctx) {
		return cfg
	}
	return a.maintenanceConfig(cfg)
}

func (a *App) jobDueConfig(ctx context.Context, cfg Config, now time.Time) Config {
	if isManualOperation(ctx) {
		return cfg
	}
	return a.retryDueConfig(cfg, now)
}

func (a *App) resetManualSweeps(ctx context.Context, cfg Config) {
	if !isManualOperation(ctx) {
		return
	}
	hosts:=map[string]bool{};for _,d:=range cfg.Domains{hosts[d.Host]=true};a.cancelAutomaticWork(hosts)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, d := range cfg.Domains {
		delete(a.state.CandidateSweeps, d.Host)
	}
}

// One user action may bypass persisted announce throttles, but must not issue
// duplicate RPCs for a torrent through multiple maintenance exit paths.
func manualAnnounceIDs(ctx context.Context, ids []int) []int {
	op, ok := ctx.Value(manualOperationKey{}).(*manualOperation)
	if !ok {
		return ids
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	unique := []int{}
	for _, id := range ids {
		if !op.announced[id] {
			op.announced[id] = true
			unique = append(unique, id)
		}
	}
	return unique
}

func manualAlreadyAnnounced(ctx context.Context, ids []int) bool {
	op, ok := ctx.Value(manualOperationKey{}).(*manualOperation)
	if !ok || len(ids) == 0 {
		return false
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	for _, id := range ids {
		if !op.announced[id] {
			return false
		}
	}
	return true
}
