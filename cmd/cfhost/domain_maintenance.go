package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func configuredDomain(cfg Config, host string) (Domain, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, d := range cfg.Domains {
		if d.Host == host {
			return d, true
		}
	}
	return Domain{}, false
}

func copyStatuses(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func groupPreferredIP(cfg Config, current map[string]string, target Domain, skip string) string {
	key := groupKey(target)
	if key == "" {
		return ""
	}
	for _, d := range cfg.Domains {
		if !d.Enabled || d.Host == target.Host || groupKey(d) != key {
			continue
		}
		ip := current[d.Host]
		if ip != "" && ip != skip {
			return ip
		}
	}
	return ""
}

// runDomainMaintenance performs a Smart-Repair style pass for exactly one
// configured domain. It may refresh the global CFST candidate pool, but it never
// reselects or removes mappings for any other domain.
func (a *App) runDomainMaintenance(ctx context.Context, cfg Config, host string) error {
	d, ok := configuredDomain(cfg, host)
	if !ok {
		return fmt.Errorf("domain not found: %s", host)
	}
	if !d.Enabled {
		return fmt.Errorf("domain is disabled: %s", d.Host)
	}

	scopeCfg := cfg
	scopeCfg.Domains = []Domain{d}
	samples := a.loadSamples(ctx, scopeCfg)
	now := time.Now()

	a.mu.RLock()
	current := copyMappings(a.state.Mappings)
	statuses := copyStatuses(a.state.DomainStatus)
	health := copyHealth(a.state.DomainHealth)
	cachedState := append([]Candidate(nil), a.state.Candidates...)
	nextRefresh := a.state.NextRefresh
	a.mu.RUnlock()

	mappings := copyMappings(current)
	currentIP := current[d.Host]
	fresh := freshCandidates(cachedState, time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute, now)

	finish := func(resolved bool, detail string) error {
		h := health[d.Host]
		stamp := time.Now().Format(time.RFC3339)
		if resolved {
			h.FailureStreak = 0
			h.LastSuccess = stamp
		} else {
			h.FailureStreak++
			h.LastFailure = stamp
		}
		health[d.Host] = h
		if err := a.commitResolution(ctx, cfg, mappings, statuses, health, nextRefresh, false); err != nil {
			return err
		}
		if !resolved {
			return fmt.Errorf("%s unresolved: %s", d.Host, detail)
		}
		return nil
	}

	if d.Class == "normal" {
		candidates := fresh
		if len(candidates) == 0 {
			a.appendLog("%s manual maintain: refreshing CFST candidates", d.Host)
			a.markRefreshAttempt(now)
			refreshed, err := a.runCFST(ctx, cfg)
			if err != nil {
				if currentIP != "" {
					statuses[d.Host] = "manual normal retained · CFST refresh failed · " + err.Error()
				} else {
					statuses[d.Host] = "manual normal unresolved · CFST refresh failed · " + err.Error()
				}
				return finish(currentIP != "", err.Error())
			}
			a.storeCandidates(refreshed)
			candidates = refreshed
		}
		order := orderedCandidates(candidates, d, "", "", cfg)
		if len(order) == 0 {
			if currentIP != "" {
				statuses[d.Host] = "manual normal retained · no CFST candidate"
				return finish(true, "")
			}
			statuses[d.Host] = "manual normal unresolved · no CFST candidate"
			delete(mappings, d.Host)
			return finish(false, "no CFST candidate")
		}
		chosen := order[0]
		mappings[d.Host] = chosen.IP
		statuses[d.Host] = fmt.Sprintf("manual normal · lowest latency · %s · %.2f ms · verification skipped", chosen.IP, chosen.DelayMS)
		a.appendLog("%s -> %s (manual normal · %.2f ms)", d.Host, chosen.IP, chosen.DelayMS)
		return finish(true, "")
	}

	if !domainRefreshable(d, scopeCfg, samples) {
		statuses[d.Host] = "manual maintain · tracker sample missing"
		// Missing evidence is not proof that an existing mapping is bad. Keep the
		// current mapping, but mark the maintenance attempt as unresolved.
		if currentIP == "" {
			delete(mappings, d.Host)
		}
		return finish(false, "tracker sample missing")
	}

	if currentIP != "" {
		ok, detail := a.verifyDomain(ctx, d, currentIP, cfg, samples)
		if ok {
			statuses[d.Host] = "manual retained · " + currentIP + " · " + detail
			a.appendLog("%s retained %s (manual maintain · %s)", d.Host, currentIP, detail)
			return finish(true, "")
		}
		statuses[d.Host] = "manual current failed · " + detail
		a.appendLog("%s current %s failed during manual maintain: %s", d.Host, currentIP, detail)
		delete(mappings, d.Host)
	}

	tryCandidates := func(candidates []Candidate) (bool, string) {
		lastDetail := "no candidate"
		preferred := groupPreferredIP(cfg, current, d, currentIP)
		if preferred != "" {
			ok, detail := a.verifyDomain(ctx, d, preferred, cfg, samples)
			lastDetail = detail
			if ok {
				mappings[d.Host] = preferred
				statuses[d.Host] = "manual verified shared · " + preferred + " · " + detail
				a.appendLog("%s -> %s (manual shared group IP · %s)", d.Host, preferred, detail)
				return true, detail
			}
		}
		for _, candidate := range orderedCandidates(candidates, d, "", currentIP, cfg) {
			if candidate.IP == preferred {
				continue
			}
			ok, detail := a.verifyDomain(ctx, d, candidate.IP, cfg, samples)
			lastDetail = detail
			if !ok {
				continue
			}
			mappings[d.Host] = candidate.IP
			statuses[d.Host] = "manual verified · " + candidate.IP + " · " + detail
			a.appendLog("%s -> %s (manual maintain · %s)", d.Host, candidate.IP, detail)
			return true, detail
		}
		return false, lastDetail
	}

	if resolved, _ := tryCandidates(fresh); resolved {
		return finish(true, "")
	}

	// A manual per-domain maintenance action is explicit user intent, so it
	// refreshes CFST immediately instead of waiting for the automatic failure
	// threshold/backoff. Only the selected domain consumes the new candidates.
	a.appendLog("%s manual maintain: cached candidates exhausted, refreshing CFST", d.Host)
	a.markRefreshAttempt(now)
	refreshed, err := a.runCFST(ctx, cfg)
	if err != nil {
		statuses[d.Host] = "manual unresolved · CFST refresh failed · " + err.Error()
		return finish(false, err.Error())
	}
	a.storeCandidates(refreshed)
	resolved, lastDetail := tryCandidates(refreshed)
	if resolved {
		return finish(true, "")
	}
	statuses[d.Host] = "manual unresolved · " + lastDetail
	delete(mappings, d.Host)
	return finish(false, lastDetail)
}

func (a *App) startDomainMaintenance(host string) (bool, error) {
	cfg := a.snapshotConfig()
	d, ok := configuredDomain(cfg, host)
	if !ok {
		return false, fmt.Errorf("domain not found: %s", host)
	}
	if !d.Enabled {
		return false, fmt.Errorf("domain is disabled: %s", d.Host)
	}

	a.mu.Lock()
	if a.state.Running {
		a.mu.Unlock()
		return false, nil
	}
	a.state.Running = true
	a.state.CurrentJob = "maintain"
	a.state.CurrentDomain = d.Host
	a.state.LastError = ""
	mappingsBefore := len(a.state.Mappings)
	refreshBefore := a.state.LastRefresh
	a.mu.Unlock()

	go func() {
		started := time.Now()
		cfg := a.snapshotConfig()
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.CFST.RunTimeoutMinutes)*time.Minute)
		defer cancel()

		a.appendLog("maintain started: %s", d.Host)
		err := a.runDomainMaintenance(ctx, cfg, d.Host)
		finished := time.Now()
		now := finished.Format(time.RFC3339)

		a.mu.Lock()
		a.state.Running = false
		a.state.CurrentJob = ""
		a.state.CurrentDomain = ""
		a.state.LastRun = now
		if err != nil {
			a.state.LastError = err.Error()
		} else {
			a.state.LastSuccess = now
			a.state.LastError = ""
		}
		record := RunRecord{
			Kind:           "maintain",
			TargetDomain:   d.Host,
			StartedAt:      started.Format(time.RFC3339),
			FinishedAt:     now,
			DurationMS:     finished.Sub(started).Milliseconds(),
			Success:        err == nil,
			MappingsBefore: mappingsBefore,
			MappingsAfter:  len(a.state.Mappings),
			CandidateCount: len(a.state.Candidates),
			FullRefresh:    a.state.LastRefresh != refreshBefore,
		}
		if _, mapped := a.state.Mappings[d.Host]; !mapped {
			record.UnresolvedCount = 1
			record.UnresolvedDomains = []string{d.Host}
		}
		if err != nil {
			record.Error = err.Error()
		}
		a.state.History = append(a.state.History, record)
		if len(a.state.History) > 200 {
			a.state.History = a.state.History[len(a.state.History)-200:]
		}
		a.mu.Unlock()

		if err != nil {
			a.appendLog("maintain failed: %s · %v", d.Host, err)
		} else {
			a.appendLog("maintain finished: %s", d.Host)
		}
		a.persistState()
	}()
	return true, nil
}
