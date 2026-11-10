package main

import (
	"net"
	"sort"
	"strings"
	"time"
)

func domainCheckDue(h DomainHealth, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, h.NextCheck)
	return err != nil || !now.Before(at)
}

func failureNeedsCandidates(detail string) bool {
	s := strings.ToLower(detail)
	return !strings.Contains(s, "reference unavailable") && !strings.Contains(s, "reference invalid") && !strings.Contains(s, "invalid custom http") && !strings.Contains(s, "sample missing") && httpStatusCodeFromDetail(detail) != 401 && httpStatusCodeFromDetail(detail) != 429 && !strings.Contains(s, "response did not match configured pattern")
}

func domainOutcome(h DomainHealth, passed bool, detail string, now time.Time, cfg Config, host string) DomainHealth {
	if passed {
		h.FailureStreak = 0
		h.LastSuccess = now.Format(time.RFC3339)
		h.NextCheck = ""
		h.FailureClass = ""
		return h
	}
	h.FailureStreak++
	h.LastFailure = now.Format(time.RFC3339)
	h.FailureClass = domainFailureClass(detail)
	base := time.Duration(cfg.RepairIntervalMinutes) * time.Minute
	if base <= 0 {
		base = 15 * time.Minute
	}
	capDelay := time.Duration(cfg.Repair.RefreshMaxBackoffMinutes) * time.Minute
	if capDelay < base {
		capDelay = base
	}
	for i := 1; i < h.FailureStreak && base < capDelay; i++ {
		base *= 2
	}
	if base > capDelay {
		base = capDelay
	}
	h.NextCheck = now.Add(base + scheduleJitter(host+h.LastFailure, base)).Format(time.RFC3339)
	return h
}

func (a *App) retryDueConfig(cfg Config, now time.Time) Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cfg.Domains = append([]Domain(nil), cfg.Domains...)
	active := cfg.Domains[:0]
	for _, d := range cfg.Domains {
		if domainCheckDue(a.state.DomainHealth[d.Host], now) {
			active = append(active, d)
		}
	}
	cfg.Domains = active
	return cfg
}

// Existing mappings are additional verification inputs, never fresh bandwidth
// measurements. They do not bypass validation, TTL, normal or bandwidth rules.
func repairCandidates(cached []Candidate, mappings map[string]string, cfg Config, now time.Time) []Candidate {
	out := freshCandidates(cached, time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute, now)
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.IP] = true
	}
	ips := []string{}
	for _, ip := range mappings {
		parsed := net.ParseIP(ip)
		if !seen[ip] && parsed != nil && parsed.IsGlobalUnicast() && !parsed.IsPrivate() {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	sort.Strings(ips)
	if len(ips) > 100 {
		ips = ips[:100]
	}
	for _, ip := range ips {
		out = append(out, Candidate{IP: ip, LatencyOnly: true, mappingFallback: true})
	}
	return out
}

func domainFailureClass(detail string) string {
	class := "unresolved"
	s := strings.ToLower(detail)
	switch {
	case httpStatusCodeFromDetail(detail) == 429:
		class = "rate-limit"
	case strings.Contains(s, "response did not match configured pattern"):
		class = "application-response"
	case !failureNeedsCandidates(detail):
		class = "configuration-or-authentication"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline") || strings.Contains(s, "connection"):
		class = "network"
	case strings.Contains(s, "http"):
		class = "http"
	}
	return class
}

// Keep still-fresh evidence from earlier scans. Replacing a duplicate never
// transfers a previous bandwidth measurement onto a newer latency-only result.
func (a *App) storeCandidates(candidates []Candidate, cfg Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := map[string]bool{}
	merged := append([]Candidate(nil), candidates...)
	for _, c := range merged {
		seen[c.IP] = true
	}
	for _, c := range freshCandidates(a.state.Candidates, time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute, time.Now()) {
		if !seen[c.IP] {
			merged = append(merged, c)
			seen[c.IP] = true
		}
	}
	merged = rankCandidates(merged)
	if len(merged) > 200 {
		merged = merged[:200]
	}
	a.state.Candidates = merged
}
