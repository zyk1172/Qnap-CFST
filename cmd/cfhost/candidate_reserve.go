package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

type CandidateReserveStatus struct {
	Status        string `json:"status"`
	FreshCount    int    `json:"freshCount"`
	UsableCount   int    `json:"usableCount"`
	MinimumCount  int    `json:"minimumCount"`
	NextAllowedAt string `json:"nextAllowedAt,omitempty"`
}

func nextScheduledJob(now time.Time, cfg Config, s RuntimeState) string {
	if s.Running {
		return ""
	}
	keepalive := copyTrackerKeepalive(s.TrackerKeepalive)
	for _, d := range cfg.Domains {
		if maintenancePaused(d, s.DomainHealth) {
			delete(keepalive, d.Host)
		}
	}
	if trackerKeepaliveCheckDue(now, keepalive) {
		return "repair"
	}
	if optimizeDue(now, s.LastOptimize, s.LastOptimizeAttempt, s.LastRefresh, cfg.Optimize) {
		return "optimize"
	}
	if !cfg.AutoRepair {
		return ""
	}
	last, _ := time.Parse(time.RFC3339, s.LastRun)
	if last.IsZero() || now.Sub(last) >= time.Duration(cfg.RepairIntervalMinutes)*time.Minute {
		return "repair"
	}
	if candidateReserveStatus(s, cfg, now).Status == "ready" {
		return "reserve"
	}
	return ""
}

// Only timestamped, unexpired evidence counts toward the reserve. Existing
// mappings remain verification fallbacks, never a substitute for fresh scans.
// The least supplied active strategy controls replenishment: a large latency
// pool must not conceal an empty bandwidth/normal pool.
func candidateReserveStatus(s RuntimeState, cfg Config, now time.Time) CandidateReserveStatus {
	view := CandidateReserveStatus{Status: "disabled", MinimumCount: cfg.Repair.CandidateMinCount}
	fresh := []Candidate{}
	seen := map[string]bool{}
	for _, c := range freshCandidates(s.Candidates, time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute, now) {
		ip := net.ParseIP(c.IP)
		if ip == nil || seen[ip.String()] || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		seen[ip.String()] = true
		fresh = append(fresh, c)
	}
	view.FreshCount = len(fresh)
	active := false
	for _, d := range cfg.Domains {
		if !d.Enabled || isFollowDomain(d) || maintenancePaused(d, s.DomainHealth) {
			continue
		}
		count := 0
		for _, c := range fresh {
			if candidateEligible(c, d, cfg) {
				count++
			}
		}
		if !active || count < view.UsableCount {
			view.UsableCount = count
		}
		active = true
	}
	if !cfg.AutoRepair || cfg.Repair.CandidateMinCount <= 0 || !active {
		return view
	}
	if view.UsableCount >= cfg.Repair.CandidateMinCount {
		view.Status = "sufficient"
		return view
	}
	// Every real attempt, including a failed or manual scan, counts toward the
	// reserve interval. LastRefresh is persisted by the shared admission gate,
	// so restarts and sparse/failed results cannot produce a refill loop.
	next := cfstNextAllowedAt(s, cfg, now)
	if last, err := time.Parse(time.RFC3339, s.LastRefresh); err == nil {
		interval := time.Duration(cfg.Repair.CandidateRefreshMinutes) * time.Minute
		if interval <= 0 {
			interval = 6 * time.Hour
		}
		at := last.Add(interval + scheduleJitter(s.LastRefresh, interval))
		if at.After(next) {
			next = at
		}
	}
	view.Status = "ready"
	if !next.IsZero() {
		view.NextAllowedAt = next.Format(time.RFC3339)
		if now.Before(next) {
			view.Status = "waiting"
		}
	}
	return view
}

func (a *App) replenishCandidateReserve(ctx context.Context, cfg Config) error {
	a.mu.RLock()
	view := candidateReserveStatus(a.state, cfg, time.Now())
	a.mu.RUnlock()
	// Recheck after dispatch: a changed config or pool must not cause an
	// unnecessary scan merely because the scheduler queued this job earlier.
	if view.Status != "ready" {
		return nil
	}
	a.setJobProgress("补充候选", fmt.Sprintf("有效储备 %d/%d", view.UsableCount, view.MinimumCount), 1, 3, 1, 1)
	candidates, err := a.runCFSTFor(ctx, cfg, fmt.Sprintf("candidate reserve low: %d/%d", view.UsableCount, view.MinimumCount))
	if err != nil {
		return err
	}
	a.setJobProgress("保存候选", fmt.Sprintf("合并 %d 个候选；保留域名映射", len(candidates)), 2, 3, 1, 1)
	a.storeCandidates(candidates, cfg)
	if err := a.persistStateStrict(); err != nil {
		return fmt.Errorf("persist candidate reserve: %w", err)
	}
	a.appendLog("candidate reserve refreshed: %d observations; mappings and Hosts unchanged", len(candidates))
	a.completeJobProgress("完成", "候选储备已补充")
	return nil
}
