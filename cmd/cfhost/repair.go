package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

type pendingDomain struct {
	Domain        Domain
	FailedCurrent string
	Refreshable   bool
}

func (a *App) runJob(ctx context.Context, kind string, cfg Config) error {
	switch kind {
	case "run":
		a.markRefreshAttempt(time.Now())
		candidates, err := a.runCFST(ctx, cfg)
		if err != nil {
			return err
		}
		a.storeCandidates(candidates)
		return nil
	case "repair":
		return a.runSmartRepair(ctx, cfg)
	default:
		return fmt.Errorf("unknown job: %s", kind)
	}
}

func (a *App) runSmartRepair(ctx context.Context, cfg Config) error {
	now := time.Now()
	samples := map[string]TrackerSample{}
	if cfg.Tracker.RealAnnounce {
		loaded, err := loadTrackerSamples(cfg.Tracker.SamplesPath)
		if loaded != nil {
			samples = loaded
		}
		if err != nil {
			if os.IsNotExist(err) {
				a.appendLog("tracker samples unavailable: %s", cfg.Tracker.SamplesPath)
			} else {
				a.appendLog("tracker samples warning: %v", err)
			}
		}
		if len(samples) > 0 {
			a.appendLog("tracker samples loaded: %d", len(samples))
		}
	}

	a.mu.RLock()
	current := copyMappings(a.state.Mappings)
	health := copyHealth(a.state.DomainHealth)
	cachedState := append([]Candidate(nil), a.state.Candidates...)
	lastRefresh := a.state.LastRefresh
	a.mu.RUnlock()

	cached := freshCandidates(cachedState, time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute, now)
	a.appendLog("repair: current=%d cached-candidates=%d", len(current), len(cached))

	mappings := make(map[string]string)
	statuses := make(map[string]string)
	groupIP := make(map[string]string)
	pending := make([]pendingDomain, 0)

	for _, d := range cfg.Domains {
		if !d.Enabled {
			statuses[d.Host] = "disabled"
			continue
		}
		refreshable := domainRefreshable(d, cfg, samples)
		if !refreshable {
			statuses[d.Host] = "tracker sample missing"
			pending = append(pending, pendingDomain{Domain: d, FailedCurrent: current[d.Host], Refreshable: false})
			continue
		}
		currentIP := current[d.Host]
		if currentIP != "" {
			ok, detail := verifyConfiguredDomain(ctx, d, currentIP, cfg, samples)
			if ok {
				mappings[d.Host] = currentIP
				statuses[d.Host] = "retained · " + currentIP + " · " + detail
				if d.Group != "" && groupIP[d.Group] == "" {
					groupIP[d.Group] = currentIP
				}
				continue
			}
			statuses[d.Host] = "current failed · " + detail
			a.appendLog("%s current %s failed: %s", d.Host, currentIP, detail)
		}
		pending = append(pending, pendingDomain{Domain: d, FailedCurrent: currentIP, Refreshable: refreshable})
	}

	pending = a.resolvePending(ctx, cfg, samples, cached, pending, mappings, statuses, groupIP)

	maxProspective := 0
	refreshablePending := 0
	for _, p := range pending {
		if !p.Refreshable {
			continue
		}
		refreshablePending++
		streak := health[p.Domain.Host].FailureStreak + 1
		if streak > maxProspective {
			maxProspective = streak
		}
	}

	bootstrap := refreshablePending > 0 && len(current) == 0 && len(cached) == 0
	refreshNow, nextRefresh, backoff := refreshDecision(
		now,
		lastRefresh,
		maxProspective,
		cfg.Repair.FailureThreshold,
		time.Duration(cfg.Repair.RefreshCooldownMinutes)*time.Minute,
		time.Duration(cfg.Repair.RefreshMaxBackoffMinutes)*time.Minute,
		bootstrap,
	)

	var refreshErr error
	if refreshablePending > 0 && refreshNow {
		a.appendLog("repair: starting CFST refresh (failure streak=%d, backoff=%s)", maxProspective, backoff)
		a.markRefreshAttempt(now)
		lastRefresh = now.Format(time.RFC3339)
		newCandidates, err := a.runCFST(ctx, cfg)
		if err != nil {
			refreshErr = err
			a.appendLog("repair: CFST refresh failed: %v", err)
		} else {
			a.storeCandidates(newCandidates)
			pending = a.resolvePending(ctx, cfg, samples, newCandidates, pending, mappings, statuses, groupIP)
		}
	} else if refreshablePending > 0 {
		if !nextRefresh.IsZero() {
			a.appendLog("repair: CFST refresh deferred until %s", nextRefresh.Format(time.RFC3339))
		} else {
			a.appendLog("repair: waiting for failure threshold (%d/%d)", maxProspective, cfg.Repair.FailureThreshold)
		}
	}

	finalNow := time.Now()
	configured := make(map[string]bool)
	refreshableUnresolved := make(map[string]bool)
	for _, p := range pending {
		configured[p.Domain.Host] = true
		if p.Refreshable {
			refreshableUnresolved[p.Domain.Host] = true
		}
	}
	for _, d := range cfg.Domains {
		if !d.Enabled {
			continue
		}
		configured[d.Host] = true
		h := health[d.Host]
		if _, ok := mappings[d.Host]; ok {
			h.FailureStreak = 0
			h.LastSuccess = finalNow.Format(time.RFC3339)
		} else {
			h.FailureStreak++
			h.LastFailure = finalNow.Format(time.RFC3339)
			if statuses[d.Host] == "" {
				statuses[d.Host] = "no verified candidate"
			}
		}
		health[d.Host] = h
	}
	for host := range health {
		if !configured[host] {
			delete(health, host)
		}
	}

	next := ""
	maxFinalStreak := 0
	for host := range refreshableUnresolved {
		if health[host].FailureStreak > maxFinalStreak {
			maxFinalStreak = health[host].FailureStreak
		}
	}
	if maxFinalStreak > 0 {
		_, nextTime, _ := refreshDecision(
			finalNow,
			lastRefresh,
			maxFinalStreak,
			cfg.Repair.FailureThreshold,
			time.Duration(cfg.Repair.RefreshCooldownMinutes)*time.Minute,
			time.Duration(cfg.Repair.RefreshMaxBackoffMinutes)*time.Minute,
			false,
		)
		if !nextTime.IsZero() {
			next = nextTime.Format(time.RFC3339)
		}
	}

	a.mu.Lock()
	a.state.Mappings = mappings
	a.state.DomainStatus = statuses
	a.state.DomainHealth = health
	a.state.NextRefresh = next
	a.mu.Unlock()

	if cfg.AutoApply {
		if err := a.applyMappings(cfg.HostsPath, mappings); err != nil {
			return err
		}
	}
	if refreshErr != nil {
		return refreshErr
	}
	return nil
}

func (a *App) resolvePending(
	ctx context.Context,
	cfg Config,
	samples map[string]TrackerSample,
	candidates []Candidate,
	pending []pendingDomain,
	mappings map[string]string,
	statuses map[string]string,
	groupIP map[string]string,
) []pendingDomain {
	if len(pending) == 0 {
		return pending
	}
	remaining := make([]pendingDomain, 0, len(pending))
	for _, p := range pending {
		if !p.Refreshable {
			remaining = append(remaining, p)
			continue
		}
		preferred := groupIP[p.Domain.Group]
		resolved := false
		lastDetail := "no candidate"
		if preferred != "" && preferred != p.FailedCurrent {
			ok, detail := verifyConfiguredDomain(ctx, p.Domain, preferred, cfg, samples)
			lastDetail = detail
			if ok {
				mappings[p.Domain.Host] = preferred
				statuses[p.Domain.Host] = "verified shared · " + preferred + " · " + detail
				a.appendLog("%s -> %s (shared group IP · %s)", p.Domain.Host, preferred, detail)
				resolved = true
			}
		}
		if resolved {
			continue
		}
		order := orderedCandidates(candidates, "", p.FailedCurrent)
		for _, c := range order {
			if c.IP == preferred {
				continue
			}
			ok, detail := verifyConfiguredDomain(ctx, p.Domain, c.IP, cfg, samples)
			lastDetail = detail
			if !ok {
				continue
			}
			mappings[p.Domain.Host] = c.IP
			statuses[p.Domain.Host] = "verified · " + c.IP + " · " + detail
			if p.Domain.Group != "" && groupIP[p.Domain.Group] == "" {
				groupIP[p.Domain.Group] = c.IP
			}
			a.appendLog("%s -> %s (%s)", p.Domain.Host, c.IP, detail)
			resolved = true
			break
		}
		if !resolved {
			statuses[p.Domain.Host] = "no verified candidate · " + lastDetail
			remaining = append(remaining, p)
		}
	}
	return remaining
}

func refreshDecision(
	now time.Time,
	lastRefresh string,
	failureStreak int,
	threshold int,
	base time.Duration,
	maxBackoff time.Duration,
	bootstrap bool,
) (bool, time.Time, time.Duration) {
	if bootstrap {
		return true, time.Time{}, 0
	}
	if failureStreak < threshold || failureStreak <= 0 {
		return false, time.Time{}, 0
	}
	if base <= 0 {
		base = time.Hour
	}
	if maxBackoff < base {
		maxBackoff = base
	}
	backoff := base
	for i := threshold; i < failureStreak && backoff < maxBackoff; i++ {
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	last, err := time.Parse(time.RFC3339, lastRefresh)
	if err != nil || last.IsZero() {
		return true, time.Time{}, backoff
	}
	next := last.Add(backoff)
	if !now.Before(next) {
		return true, next, backoff
	}
	return false, next, backoff
}

func (a *App) markRefreshAttempt(at time.Time) {
	a.mu.Lock()
	a.state.LastRefresh = at.Format(time.RFC3339)
	a.state.NextRefresh = ""
	a.mu.Unlock()
}

func (a *App) storeCandidates(candidates []Candidate) {
	a.mu.Lock()
	a.state.Candidates = rankCandidates(candidates)
	a.mu.Unlock()
}

func copyMappings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyHealth(in map[string]DomainHealth) map[string]DomainHealth {
	out := make(map[string]DomainHealth, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
