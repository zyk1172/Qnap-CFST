package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const upstreamDownloadURL = "https://cf.xiu2.xyz/url"
const cloudflareDownloadURL = "https://speed.cloudflare.com/__down?bytes=90000000"

type CFSTSourceRuntime struct {
	Failures       int                  `json:"failures,omitempty"`
	Configured     bool                 `json:"configured"`
	Origin         string               `json:"origin"`
	ResolvedOrigin string               `json:"resolvedOrigin,omitempty"`
	RateLimit      CFSTRateLimitRuntime `json:"rateLimit"`
	LastAttempt    string               `json:"lastAttempt,omitempty"`
	LastSuccess    string               `json:"lastSuccess,omitempty"`
	RetryAt        string               `json:"retryAt,omitempty"`
	LastError      string               `json:"lastError,omitempty"`
}

func sourceOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	if scheme == "http" {
		host = strings.TrimSuffix(host, ":80")
	}
	return scheme + "://" + host
}

func configuredSources(cfg Config) []string {
	primary := cfg.CFST.DownloadURL
	if primary == "" {
		primary = upstreamDownloadURL
	}
	urls := append([]string{primary}, cfg.CFST.DownloadURLs...)
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		origin := sourceOrigin(raw)
		if origin != "" && !seen[origin] {
			seen[origin] = true
			out = append(out, raw)
		}
	}
	return out
}

func normalizeSources(c *CFSTConfig) error {
	if c.DownloadURL == "" {
		c.DownloadURL = upstreamDownloadURL
	}
	if c.DownloadURLs == nil {
		c.DownloadURLs = []string{upstreamDownloadURL}
		if sourceOrigin(c.DownloadURL) == sourceOrigin(upstreamDownloadURL) {
			c.DownloadURLs = []string{cloudflareDownloadURL}
		}
	}
	if len(c.DownloadURLs) > 7 {
		return fmt.Errorf("at most 8 download sources are allowed")
	}
	for _, raw := range append([]string{c.DownloadURL}, c.DownloadURLs...) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("invalid download source URL")
		}
	}
	return nil
}

func sourceRuntime(s RuntimeState, origin string) CFSTSourceRuntime {
	r := s.CFSTSources[origin]
	r.Origin = origin
	// Migrate existing installations without dropping their provider feedback.
	if r.RateLimit.DetectedAt == "" && (s.CFSTRateLimit.Origin == origin || s.CFSTRateLimit.Origin == "") {
		r.RateLimit = s.CFSTRateLimit
	}
	return r
}

func sourceAllowedAt(s RuntimeState, origin string) time.Time {
	r := sourceRuntime(s, origin)
	next, _ := time.Parse(time.RFC3339, r.RetryAt)
	for _, key := range []string{origin, r.ResolvedOrigin} {
		rate := sourceRuntime(s, key).RateLimit
		if at, err := time.Parse(time.RFC3339, rate.Until); rate.Active && err == nil && at.After(next) {
			next = at
		}
	}
	return next
}

func selectSource(s RuntimeState, cfg Config, now time.Time) (string, time.Time) {
	urls := configuredSources(cfg)
	start := 0
	for i, raw := range urls {
		if sourceOrigin(raw) == s.CFSTSchedule.Source {
			start = (i + 1) % len(urls)
			break
		}
	}
	var earliest time.Time
	for i := range urls {
		raw := urls[(start+i)%len(urls)]
		at := sourceAllowedAt(s, sourceOrigin(raw))
		if !now.Before(at) {
			return raw, time.Time{}
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return "", earliest
}

func (a *App) noteSourceOrigin(raw, actual string) {
	origin := sourceOrigin(raw)
	actual = sourceOrigin(actual)
	if origin == "" || actual == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.CFSTSources == nil {
		a.state.CFSTSources = map[string]CFSTSourceRuntime{}
	}
	r := sourceRuntime(a.state, origin)
	r.ResolvedOrigin = actual
	a.state.CFSTSources[origin] = r
}

func (a *App) blockedSourceOrigins(cfg Config) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	blocked := map[string]bool{}
	if legacy := a.state.CFSTRateLimit.Origin; legacy != "" && time.Now().Before(sourceAllowedAt(a.state, legacy)) {
		blocked[legacy] = true
	}
	for key := range a.state.CFSTSources {
		if time.Now().Before(sourceAllowedAt(a.state, key)) {
			blocked[key] = true
		}
	}
	for _, raw := range configuredSources(cfg) {
		key := sourceOrigin(raw)
		if time.Now().Before(sourceAllowedAt(a.state, key)) {
			blocked[key] = true
		}
	}
	out := []string{}
	for key := range blocked {
		if key != "" {
			out = append(out, key)
		}
	}
	return strings.Join(out, ",")
}

func sourceStatusView(s RuntimeState, cfg Config, now time.Time) map[string]CFSTSourceRuntime {
	view := map[string]CFSTSourceRuntime{}
	for _, raw := range configuredSources(cfg) {
		origin := sourceOrigin(raw)
		r := sourceRuntime(s, origin)
		r.Configured = true
		if r.ResolvedOrigin != "" {
			other := sourceRuntime(s, r.ResolvedOrigin)
			otherTime, _ := time.Parse(time.RFC3339, other.RateLimit.Until)
			ownTime, _ := time.Parse(time.RFC3339, r.RateLimit.Until)
			if otherTime.After(ownTime) {
				r.RateLimit = other.RateLimit
			}
		}
		r.RateLimit = effectiveRateLimit(r.RateLimit, now)
		view[origin] = r
	}
	return view
}
