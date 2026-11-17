package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"
)

type CFSTScheduleRuntime struct {
	Source        string `json:"source,omitempty"`
	LastAttempt   string `json:"lastAttempt,omitempty"`
	NextAllowedAt string `json:"nextAllowedAt,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// Positive, stable jitter survives restarts and never shortens Retry-After.
func scheduleJitter(key string, base time.Duration) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum32()%1001) * (base / 10) / 1000
}

func effectiveRateLimit(s CFSTRateLimitRuntime, now time.Time) CFSTRateLimitRuntime {
	until, err := time.Parse(time.RFC3339, s.Until)
	if s.Active && err == nil && !now.Before(until) {
		s.Active = false
		s.Mode = "ready"
		s.LastEvent = "cooldown expired; waiting for a recovery probe"
	}
	return s
}

func cfstNextAllowed(s RuntimeState, cfg Config) time.Time {
	return cfstNextAllowedAt(s, cfg, time.Now())
}

func cfstNextAllowedAt(s RuntimeState, cfg Config, now time.Time) time.Time {
	var next time.Time
	if last, err := time.Parse(time.RFC3339, s.LastRefresh); err == nil {
		base := time.Duration(cfg.Repair.RefreshCooldownMinutes) * time.Minute
		if base <= 0 {
			base = time.Hour
		}
		next = last.Add(base + scheduleJitter(s.LastRefresh, base))
	}
	_, available := selectSource(s, cfg, now)
	if available.After(next) {
		next = available
	}
	return next
}

func (a *App) recordCFSTRateLimit(now time.Time, cfg Config, signal cfstRateLimitSignal) time.Time {
	base := time.Duration(cfg.CFST.RateLimitFallbackMinutes) * time.Minute
	until, seconds := retryAfterDeadline(now, signal.RetryAfter, base)
	a.mu.Lock()
	origin := sourceOrigin(cfg.CFST.DownloadURL)
	old := sourceRuntime(a.state, origin).RateLimit
	actual := sourceOrigin(signal.Origin)
	if actual == "" {
		actual = sourceRuntime(a.state, origin).ResolvedOrigin
	}
	if actual != "" {
		other := sourceRuntime(a.state, actual).RateLimit
		otherAt, _ := time.Parse(time.RFC3339, other.Until)
		oldAt, _ := time.Parse(time.RFC3339, old.Until)
		if otherAt.After(oldAt) {
			old = other
		}
	}
	count := old.Consecutive + 1
	if base <= 0 {
		base = 15 * time.Minute
	}
	for i := 1; i < count && base < 24*time.Hour; i++ {
		base *= 2
	}
	if base > 24*time.Hour {
		base = 24 * time.Hour
	}
	if local := now.Add(base); local.After(until) {
		until = local
	}
	until = until.Add(scheduleJitter(now.Format(time.RFC3339), time.Minute))
	if at, err := time.Parse(time.RFC3339, old.Until); err == nil && at.After(until) {
		until = at
	}
	until = until.Truncate(time.Second).Add(time.Second)
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{Origin: origin, Consecutive: count, Active: true, Mode: "cooldown", StatusCode: signal.StatusCode, RetryAfterSeconds: seconds, Until: until.Format(time.RFC3339), DetectedAt: now.Format(time.RFC3339), LastEvent: "download probes stopped until cooldown expires"}
	if a.state.CFSTSources == nil {
		a.state.CFSTSources = map[string]CFSTSourceRuntime{}
	}
	r := sourceRuntime(a.state, origin)
	r.RateLimit = a.state.CFSTRateLimit
	if signal.Origin != "" {
		r.ResolvedOrigin = sourceOrigin(signal.Origin)
	}
	a.state.CFSTSources[origin] = r
	if r.ResolvedOrigin != "" {
		target := sourceRuntime(a.state, r.ResolvedOrigin)
		target.RateLimit = r.RateLimit
		target.Origin = r.ResolvedOrigin
		a.state.CFSTSources[r.ResolvedOrigin] = target
	}
	a.state.CFSTSchedule.NextAllowedAt = cfstNextAllowed(a.state, cfg).Format(time.RFC3339)
	a.mu.Unlock()
	return until
}

// All entry points share one persisted admission budget. A manual action may
// bypass a domain's retry delay, but cannot bypass a provider's cooldown.
func (a *App) runCFSTFor(ctx context.Context, cfg Config, reason string) ([]Candidate, error) {
	a.cfstMu.Lock()
	defer a.cfstMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	a.mu.Lock()
	next := cfstNextAllowed(a.state, cfg)
	if now.Before(next) {
		a.state.CFSTSchedule.NextAllowedAt = next.Format(time.RFC3339)
		a.mu.Unlock()
		return nil, fmt.Errorf("CFST deferred until %s; cached candidates remain available for verification", next.Format(time.RFC3339))
	}
	selected, _ := selectSource(a.state, cfg, now)
	if selected == "" {
		a.mu.Unlock()
		return nil, fmt.Errorf("no download source available")
	}
	oldRefresh, oldSchedule := a.state.LastRefresh, a.state.CFSTSchedule
	selectedOrigin := sourceOrigin(selected)
	prior := sourceRuntime(a.state, selectedOrigin)
	recovery := prior.Failures > 0 || (prior.RateLimit.DetectedAt != "" && (prior.RateLimit.Consecutive > 0 || prior.RateLimit.Mode != "normal"))
	trigger := a.state.CurrentJob
	if trigger == "" {
		trigger = "run"
	}
	if a.state.CurrentDomain != "" {
		trigger += " · " + a.state.CurrentDomain
	}
	a.state.LastRefresh = now.Format(time.RFC3339)
	a.state.CFSTSchedule = CFSTScheduleRuntime{Source: selectedOrigin, LastAttempt: a.state.LastRefresh, Trigger: trigger, Reason: reason}
	a.state.CFSTSchedule.NextAllowedAt = cfstNextAllowed(a.state, cfg).Format(time.RFC3339)
	a.mu.Unlock()
	if err := a.persistStateStrict(); err != nil {
		a.mu.Lock()
		a.state.LastRefresh = oldRefresh
		a.state.CFSTSchedule = oldSchedule
		a.mu.Unlock()
		return nil, fmt.Errorf("persist CFST admission: %w", err)
	}
	a.appendLog("CFST admitted: %s · source=%s", trigger, selectedOrigin)
	cfstCtx, cancel := context.WithTimeout(ctx, cfstRunTimeout(cfg))
	defer cancel()
	selectedCfg := cfg
	selectedCfg.CFST.DownloadURL = selected
	candidates, signal, err := a.runCFSTMode(cfstCtx, selectedCfg, recovery)
	if signal != nil && !signal.Blocked {
		until := a.recordCFSTRateLimit(time.Now(), selectedCfg, *signal)
		a.appendLog("CFST HTTP %d: download probes stopped until %s; no immediate retry", signal.StatusCode, until.Format(time.RFC3339))
	} else if signal == nil && err == nil && hasBandwidthEvidence(candidates) {
		a.mu.Lock()
		if a.state.CFSTRateLimit.Origin == selectedOrigin || a.state.CFSTRateLimit.Origin == "" {
			a.state.CFSTRateLimit.Active = false
			a.state.CFSTRateLimit.Mode = "normal"
			a.state.CFSTRateLimit.Consecutive = 0
			a.state.CFSTRateLimit.LastEvent = "download probe succeeded"
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	if a.state.CFSTSources == nil {
		a.state.CFSTSources = map[string]CFSTSourceRuntime{}
	}
	r := sourceRuntime(a.state, selectedOrigin)
	r.LastAttempt = now.Format(time.RFC3339)
	if signal == nil && err == nil && hasBandwidthEvidence(candidates) {
		r.Failures = 0
		r.LastSuccess = time.Now().Format(time.RFC3339)
		r.LastError = ""
		r.RetryAt = ""
		r.RateLimit.Active = false
		r.RateLimit.Mode = "normal"
		r.RateLimit.Consecutive = 0
	}
	if (err != nil || !hasBandwidthEvidence(candidates)) && signal == nil {
		r.LastError = "source did not provide usable measurements"
		r.Failures++
		delay := time.Duration(cfg.Repair.RefreshCooldownMinutes) * time.Minute
		if delay <= 0 {
			delay = time.Hour
		}
		for i := 1; i < r.Failures && delay < 24*time.Hour; i++ {
			delay *= 2
		}
		if delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		r.RetryAt = time.Now().Add(delay).Format(time.RFC3339)
	}
	a.state.CFSTSources[selectedOrigin] = r
	a.state.CFSTSchedule.NextAllowedAt = cfstNextAllowed(a.state, cfg).Format(time.RFC3339)
	a.mu.Unlock()
	// Persist provider feedback even if the caller later aborts resolution.
	if persistErr := a.persistStateStrict(); persistErr != nil {
		return nil, fmt.Errorf("persist CFST outcome: %w", persistErr)
	}
	return candidates, err
}

func hasBandwidthEvidence(candidates []Candidate) bool {
	for _, c := range candidates {
		if !c.LatencyOnly && c.SpeedMB > 0 {
			return true
		}
	}
	return false
}
