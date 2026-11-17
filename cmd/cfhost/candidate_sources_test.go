package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCandidateBatchesResumeAfterRestartBeforeNewScan(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	cfg.Verify.CandidateLimit = 10
	cfg.Domains = []Domain{{Host: "offline.example", Class: "latency", Mode: "http", Endpoint: "/%", Enabled: true}}
	for i := 1; i <= 13; i++ {
		a.state.Candidates = append(a.state.Candidates, Candidate{IP: fmt.Sprintf("104.16.0.%d", i), ObservedAt: time.Now().Format(time.RFC3339)})
	}
	_ = a.runDomainMaintenance(context.Background(), cfg, "offline.example")
	if got := a.cachedCandidatesRemaining("offline.example"); got != 3 {
		t.Fatalf("lost tail: %d", got)
	}
	if a.state.DomainHealth["offline.example"].FailureStreak != 0 {
		t.Fatal("unfinished sweep counted as whole-pool failure")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "calls")); !os.IsNotExist(err) {
		t.Fatal("scan ran with untried cached candidates")
	}
	restarted, _ := newRegressionApp(t)
	restarted.dataDir = a.dataDir
	restarted.cfstBin = a.cfstBin
	restarted.loadState()
	batch := restarted.candidateBatch(restarted.state.Candidates, cfg.Domains[0], "", cfg)
	if len(batch) != 3 || batch[0].IP == "104.16.0.1" {
		t.Fatalf("repeated first batch: %+v", batch)
	}
	_ = restarted.runDomainMaintenance(context.Background(), cfg, "offline.example")
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("expected one scan after exhaustion: %q", calls)
	}
}

func TestNewCandidateMeasurementsDoNotStrandUntestedOlderIPs(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.Verify.CandidateLimit = 1
	d := Domain{Host: "api.example", Class: "latency", Mode: "http"}
	old := []Candidate{{IP: "104.16.0.1", ObservedAt: "old"}, {IP: "104.16.0.2", ObservedAt: "old"}}
	b := a.candidateBatch(old, d, "", cfg)
	a.recordCandidateAttempt(d.Host, b[0], false)
	updated := append(old, Candidate{IP: "104.16.0.3", ObservedAt: "new", DelayMS: 10})
	b = a.candidateBatch(updated, d, "", cfg)
	if b[0].IP != "104.16.0.2" {
		t.Fatalf("old untried candidate lost: %+v", b)
	}
	a.recordCandidateAttempt(d.Host, b[0], true)
	if _, ok := a.state.CandidateSweeps[d.Host]; ok {
		t.Fatal("successful switch did not complete sweep")
	}
}

func TestLegacySourceConfigAddsUpstreamAndExplicitSingleSourceSurvives(t *testing.T) {
	c := CFSTConfig{DownloadURL: cloudflareDownloadURL}
	if err := normalizeSources(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.DownloadURLs) != 1 || c.DownloadURLs[0] != upstreamDownloadURL {
		t.Fatalf("upstream missing: %+v", c)
	}
	c.DownloadURLs = []string{}
	if err := normalizeSources(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.DownloadURLs) != 0 {
		t.Fatal("explicit single-source config overwritten")
	}
	c.DownloadURLs = []string{cloudflareDownloadURL + "&other=1"}
	if len(configuredSources(Config{CFST: c})) != 1 {
		t.Fatal("same backend counted as multiple sources")
	}
}

func TestSourceRotationPersistsWithoutIncreasingGlobalScanBudget(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.CFST.DownloadURL = upstreamDownloadURL
	cfg.CFST.DownloadURLs = []string{cloudflareDownloadURL}
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in -o) output="$2"; shift 2 ;; -url) echo "$2" >> sources; shift 2 ;; *) shift ;; esac
done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.1,4,4,0,30,5,SIN\n' > "$output"
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for i, origin := range []string{sourceOrigin(upstreamDownloadURL), sourceOrigin(cloudflareDownloadURL), sourceOrigin(upstreamDownloadURL)} {
		if i > 0 {
			a.state.LastRefresh = time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
		}
		if _, err := a.runCFST(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if a.state.CFSTSchedule.Source != origin {
			t.Fatalf("wrong source on round %d: %+v", i, a.state.CFSTSchedule)
		}
		if _, err := a.runCFST(context.Background(), cfg); err == nil {
			t.Fatal("switching sources bypassed global interval")
		}
		reloaded, _ := newRegressionApp(t)
		reloaded.dataDir = a.dataDir
		reloaded.cfstBin = a.cfstBin
		reloaded.loadState()
		a = reloaded
	}
	sources, _ := os.ReadFile(filepath.Join(a.dataDir, "sources"))
	if len(strings.Fields(string(sources))) != 3 {
		t.Fatalf("extra scans: %s", sources)
	}
}

func TestSourceCooldownIsIndependentButRedirectAliasesShareIt(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.CFST.DownloadURL = upstreamDownloadURL
	cfg.CFST.DownloadURLs = []string{cloudflareDownloadURL, "https://independent.example/file"}
	a.noteSourceOrigin(upstreamDownloadURL, sourceOrigin(cloudflareDownloadURL))
	until := a.recordCFSTRateLimit(time.Now(), cfg, cfstRateLimitSignal{StatusCode: 429, RetryAfter: "3600", Origin: sourceOrigin(cloudflareDownloadURL)})
	raw, _ := selectSource(a.state, cfg, time.Now())
	if raw != "https://independent.example/file" {
		t.Fatalf("alias bypassed cooldown: %s", raw)
	}
	if sourceAllowedAt(a.state, sourceOrigin(cloudflareDownloadURL)).Before(until) {
		t.Fatal("redirect target cooldown lost")
	}
	if err := a.persistStateStrict(); err != nil {
		t.Fatal(err)
	}
	b, _ := newRegressionApp(t)
	b.dataDir = a.dataDir
	b.loadState()
	if raw, _ := selectSource(b.state, cfg, time.Now()); raw != "https://independent.example/file" {
		t.Fatal("restart lost source association")
	}
	if !strings.Contains(b.blockedSourceOrigins(cfg), sourceOrigin(cloudflareDownloadURL)) {
		t.Fatal("redirect guard missing backend")
	}
}

func TestSourceSignalPreservesHTTPDateAndActualOrigin(t *testing.T) {
	s, ok := parseCFSTRateLimitSignal(`CFST_RATE_LIMIT status=429 retry_after="Wed, 11 Nov 2026 00:00:00 GMT" origin="https://backend.example"`)
	if !ok || s.RetryAfter != "Wed, 11 Nov 2026 00:00:00 GMT" || s.Origin != "https://backend.example" {
		t.Fatalf("bad signal: %+v", s)
	}
	s, ok = parseCFSTRateLimitSignal(`CFST_SOURCE_BLOCKED origin="https://backend.example"`)
	if !ok || !s.Blocked || s.StatusCode != 0 {
		t.Fatal("local cooldown invented an HTTP response")
	}
}

func TestHealthySourceDoesNotEraseOtherSourceCooldown(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.CFST.DownloadURL = "https://first.example/file"
	cfg.CFST.DownloadURLs = []string{"https://second.example/file"}
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in -url) url="$2"; shift 2 ;; -o) output="$2"; shift 2 ;; -latency-output) latency="$2"; shift 2 ;; *) shift ;; esac
done
echo "$url" >> sources
case "$url" in
 *first.example*) printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.1,4,4,0,30,0,SIN\n' > "$latency"; echo 'CFST_RATE_LIMIT status=429 retry_after="3600" origin="https://first.example"' ;;
 *) printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.2,4,4,0,30,5,SIN\n' > "$output" ;;
esac
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runCFST(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := newRegressionApp(t)
	b.dataDir = a.dataDir
	b.cfstBin = a.cfstBin
	b.loadState()
	b.state.LastRefresh = time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := b.runCFST(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if b.state.CFSTSchedule.Source != "https://second.example" {
		t.Fatal("healthy source not selected")
	}
	if !b.state.CFSTSources["https://first.example"].RateLimit.Active {
		t.Fatal("success of another source erased cooldown")
	}
	if !time.Now().Before(sourceAllowedAt(b.state, "https://first.example")) {
		t.Fatal("limited source became available early")
	}
}

func TestLegacyCooldownStillBlocksAnUnknownRedirectAlias(t *testing.T) {
	a, cfg := newRegressionApp(t)
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{Origin: "https://speed.cloudflare.com", Active: true, Until: time.Now().Add(time.Hour).Format(time.RFC3339)}
	cfg.CFST.DownloadURL = upstreamDownloadURL
	cfg.CFST.DownloadURLs = []string{}
	if !strings.Contains(a.blockedSourceOrigins(cfg), "https://speed.cloudflare.com") {
		t.Fatal("legacy cooldown absent from redirect guard")
	}
}
