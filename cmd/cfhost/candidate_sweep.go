package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

type CandidateSweep struct {
	ConfigKey string          `json:"configKey"`
	Failed    map[string]bool `json:"failed"`
	Remaining int             `json:"remaining"`
}

func candidateEvidenceKey(c Candidate) string { return c.IP + "|" + c.ObservedAt + "|" + c.Source }

// The cursor is a set of failed evidence records, rather than an array index:
// inserting/reordering new measurements cannot strand old untried candidates.
func (a *App) candidateBatch(all []Candidate, d Domain, skip string, cfg Config) []Candidate {
	options := cfg
	options.Verify.CandidateLimit = 0
	ordered := orderedCandidates(all, d, "", skip, options)
	b, _ := json.Marshal(struct {
		D Domain
		V VerifyConfig
		B BandwidthConfig
	}{d, cfg.Verify, cfg.Bandwidth})
	key := fmt.Sprintf("%x", sha256.Sum256(b))
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.CandidateSweeps == nil {
		a.state.CandidateSweeps = map[string]CandidateSweep{}
	}
	s := a.state.CandidateSweeps[d.Host]
	if s.ConfigKey != key {
		s = CandidateSweep{ConfigKey: key, Failed: map[string]bool{}}
	}
	keep := map[string]bool{}
	remaining := []Candidate{}
	for _, c := range ordered {
		id := candidateEvidenceKey(c)
		if s.Failed[id] {
			keep[id] = true
		} else {
			remaining = append(remaining, c)
		}
	}
	s.Failed = keep
	s.Remaining = len(remaining)
	a.state.CandidateSweeps[d.Host] = s
	if cfg.Verify.CandidateLimit > 0 && len(remaining) > cfg.Verify.CandidateLimit {
		remaining = remaining[:cfg.Verify.CandidateLimit]
	}
	return remaining
}

func (a *App) recordCandidateAttempt(host string, c Candidate, passed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.CandidateSweeps == nil {
		a.state.CandidateSweeps = map[string]CandidateSweep{}
	}
	if passed {
		delete(a.state.CandidateSweeps, host)
		return
	}
	s := a.state.CandidateSweeps[host]
	if s.Failed == nil {
		s.Failed = map[string]bool{}
	}
	if !s.Failed[candidateEvidenceKey(c)] {
		s.Failed[candidateEvidenceKey(c)] = true
		if s.Remaining > 0 {
			s.Remaining--
		}
	}
	a.state.CandidateSweeps[host] = s
}

func (a *App) cachedCandidatesRemaining(host string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state.CandidateSweeps[host].Remaining
}

func (a *App) candidateOutcome(h DomainHealth, passed bool, detail string, now time.Time, cfg Config, host string) DomainHealth {
	if !passed && a.cachedCandidatesRemaining(host) > 0 && failureNeedsCandidates(detail) {
		// A bounded batch ending is not proof that the whole candidate pool failed.
		interval := time.Duration(cfg.RepairIntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = 15 * time.Minute
		}
		h.NextCheck = now.Add(interval).Format(time.RFC3339)
		return h
	}
	if passed {
		a.mu.Lock()
		delete(a.state.CandidateSweeps, host)
		a.mu.Unlock()
	}
	return domainOutcome(h, passed, detail, now, cfg, host)
}
