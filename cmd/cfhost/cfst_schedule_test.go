package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeLimitedCFST(t *testing.T, a *App) {
	t.Helper()
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	// Each invocation leaves evidence. No real network request is made.
	script := `#!/bin/sh
echo call >> calls
while [ "$#" -gt 0 ]; do
 case "$1" in
  -latency-output) latency="$2"; shift 2 ;;
  *) shift ;;
 esac
done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.2,4,4,0,35,0,SIN\n' > "$latency"
echo 'CFST_RATE_LIMIT status=429 retry_after="3600"'
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestCFST429StopsWithoutRetryAndSurvivesRestart(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	// An old disabled adaptive toggle must not defeat provider Retry-After.
	cfg.CFST.AdaptiveRateLimit = false
	got, err := a.runCFST(context.Background(), cfg)
	if err != nil || len(got) != 1 || !got[0].LatencyOnly || got[0].SpeedMB != 0 {
		t.Fatalf("latency evidence lost: %+v %v", got, err)
	}
	until, _ := time.Parse(time.RFC3339, a.state.CFSTRateLimit.Until)
	if until.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatal("Retry-After shortened")
	}
	reloaded, _ := newRegressionApp(t)
	reloaded.dataDir = a.dataDir
	reloaded.cfstBin = a.cfstBin
	reloaded.loadState()
	for _, kind := range []string{"run", "optimize"} {
		before := reloaded.state.LastRefresh
		if err := reloaded.runJob(context.Background(), kind, cfg); err == nil || !strings.Contains(err.Error(), "deferred") {
			t.Fatalf("%s bypassed cooldown: %v", kind, err)
		}
		if before != reloaded.state.LastRefresh {
			t.Fatal("denied attempt changed refresh timestamp")
		}
	}
	if err := reloaded.runDomainMaintenance(context.Background(), cfg, cfg.Domains[2].Host); err == nil {
		t.Fatal("unconfigured maintenance unexpectedly passed")
	}
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("unexpected provider retry: %q", calls)
	}
}

func TestCFSTConcurrentRequestsConsumeOneBudget(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() { defer wg.Done(); _, _ = a.runCFST(context.Background(), cfg) }()
	}
	wg.Wait()
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("duplicate scans: %q", calls)
	}
}

func TestCFSTAdmissionFailureMakesNoProviderRequest(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	if err := os.Mkdir(filepath.Join(a.dataDir, "state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runCFST(context.Background(), cfg); err == nil {
		t.Fatal("scan proceeded without durable budget")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "calls")); !os.IsNotExist(err) {
		t.Fatal("provider was contacted")
	}
	if a.state.LastRefresh != "" {
		t.Fatal("failed admission was not rolled back")
	}
}

func TestRateLimitDeadlineNeverShrinksAndExpiredStatusIsDerived(t *testing.T) {
	a, cfg := newRegressionApp(t)
	now := time.Now().Truncate(time.Second)
	old := now.Add(4 * time.Hour)
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{Active: true, Until: old.Format(time.RFC3339)}
	if until := a.recordCFSTRateLimit(now, cfg, cfstRateLimitSignal{429, "60"}); until.Before(old) {
		t.Fatal("cooldown shortened")
	}
	a.state.CFSTRateLimit.Until = now.Add(-time.Minute).Format(time.RFC3339)
	w := httptest.NewRecorder()
	a.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var state RuntimeState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.CFSTRateLimit.Active || state.CFSTRateLimit.Mode != "ready" {
		t.Fatalf("expired cooldown still displayed: %+v", state.CFSTRateLimit)
	}
	if !a.state.CFSTRateLimit.Active {
		t.Fatal("status GET mutated the persisted feedback")
	}
}

func TestExpiredCooldownUsesOneSmallRecoveryScan(t *testing.T) {
	a, cfg := newRegressionApp(t)
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{Active: true, Consecutive: 1, DetectedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339), Until: time.Now().Add(-time.Hour).Format(time.RFC3339)}
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
printf '%s\n' "$@" > args
while [ "$#" -gt 0 ]; do
 case "$1" in -o) output="$2"; shift 2 ;; *) shift ;; esac
done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.1,4,4,0,30,4,SIN\n' > "$output"
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.CFST.DownloadCount = 25
	cfg.CFST.DegradedDownloadCount = 25
	cfg.CFST.DownloadSeconds = 20
	cfg.CFST.DegradedDownloadSeconds = 20
	if _, err := a.runCFST(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(a.dataDir, "args"))
	if !strings.Contains(string(args), "-dn\n5\n") || !strings.Contains(string(args), "-dt\n3\n") {
		t.Fatalf("large recovery burst: %s", args)
	}
	if a.state.CFSTRateLimit.Active || a.state.CFSTRateLimit.Consecutive != 0 {
		t.Fatal("recovery not recorded")
	}
}

func TestDomainRetryIsolationAndMissingCredentialsDoNotTriggerCFST(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	t.Setenv("CFHOST_PROBE_MISSING_TEST_HOST", "bad.example")
	t.Setenv("CFHOST_PROBE_MISSING_TEST", "")
	cfg.Domains = []Domain{
		{Host: "bad.example", Class: "latency", Mode: "http", Endpoint: "/profile", Enabled: true, HTTPProbe: &HTTPProbeConfig{Headers: []HTTPProbeHeader{{Name: "x-api-key", ValueRef: "env:CFHOST_PROBE_MISSING_TEST"}}}},
		{Host: "healthy.example", Class: "normal", Mode: "http", Enabled: true},
	}
	cfg.Repair.FailureThreshold = 1
	a.state.Mappings = map[string]string{"bad.example": "104.16.0.1", "healthy.example": "104.16.0.2"}
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}}
	if err := a.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	bad := a.state.DomainHealth["bad.example"]
	if bad.FailureStreak != 1 || bad.NextCheck == "" || bad.FailureClass != "configuration-or-authentication" {
		t.Fatalf("missing retry classification: %+v", bad)
	}
	if a.state.DomainHealth["healthy.example"].LastSuccess == "" {
		t.Fatal("healthy domain not maintained")
	}
	if err := a.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.DomainHealth["bad.example"] != bad {
		t.Fatal("deferred domain retried or had evidence cleared")
	}
	if a.state.Mappings["bad.example"] != "104.16.0.1" {
		t.Fatal("unresolved mapping discarded")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "calls")); !os.IsNotExist(err) {
		t.Fatal("missing secret caused a global scan")
	}
}

func TestPerDomainBackoffPersistsAndOnlySuccessClearsIt(t *testing.T) {
	a, cfg := newRegressionApp(t)
	now := time.Now()
	h := domainOutcome(DomainHealth{}, false, "Tracker did not respond", now, cfg, "tracker.example")
	first, _ := time.Parse(time.RFC3339, h.NextCheck)
	h = domainOutcome(h, false, "Tracker did not respond", now, cfg, "tracker.example")
	second, _ := time.Parse(time.RFC3339, h.NextCheck)
	if !second.After(first) {
		t.Fatal("repeated failure did not increase backoff")
	}
	a.state.DomainHealth["tracker.example"] = h
	if err := a.persistStateStrict(); err != nil {
		t.Fatal(err)
	}
	b, _ := newRegressionApp(t)
	b.dataDir = a.dataDir
	b.loadState()
	if domainCheckDue(b.state.DomainHealth["tracker.example"], now) {
		t.Fatal("restart bypassed domain backoff")
	}
	h = domainOutcome(h, true, "", now, cfg, "tracker.example")
	if h.NextCheck != "" || h.FailureClass != "" || h.FailureStreak != 0 {
		t.Fatal("success did not clear backoff")
	}
}

func TestMappingFallbackCannotBecomeBandwidthEvidence(t *testing.T) {
	cfg := defaultConfig()
	now := time.Now()
	pool := repairCandidates([]Candidate{{IP: "104.16.0.1", SpeedMB: 99, ObservedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}}, map[string]string{"a": "104.16.0.2"}, cfg, now)
	if len(pool) != 1 || !pool[0].LatencyOnly || pool[0].ObservedAt != "" || pool[0].SpeedMB != 0 {
		t.Fatalf("fabricated fresh measurement: %+v", pool)
	}
	for _, class := range []string{"normal", "bandwidth"} {
		if len(orderedCandidates(pool, Domain{Class: class}, "", "", cfg)) != 0 {
			t.Fatalf("%s consumed unmeasured mapping", class)
		}
	}
	if len(orderedCandidates(pool, Domain{Class: "latency"}, "", "", cfg)) != 1 {
		t.Fatal("verified strategy lost mapping fallback")
	}
}

func TestFreshDownloaderFailureCannotBypassGlobalCooldown(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	const host = "tracker.example"
	now := time.Now()
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "test")
			w.WriteHeader(409)
			return
		}
		var request struct {
			Method    string `json:"method"`
			Arguments struct {
				Fields []string `json:"fields"`
			} `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		row := map[string]any{"id": 1, "hashString": testHashA, "status": 6, "leftUntilDone": 0, "isFinished": true}
		if containsString(request.Arguments.Fields, "trackerStats") {
			row["trackerStats"] = []map[string]any{{"announce": "https://tracker.example/announce", "hasAnnounced": true, "lastAnnounceResult": "Tracker HTTP 403", "lastAnnounceTime": now.Unix(), "lastAnnounceSucceeded": false}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": []any{row}}})
	}))
	defer rpc.Close()
	cfg.Domains = []Domain{{Host: host, Class: "latency", Mode: "tracker", Endpoint: "/announce", Enabled: true}}
	cfg.Tracker.Transmission = DownloaderClientConfig{Enabled: true, URL: rpc.URL}
	if err := os.WriteFile(cfg.Tracker.SamplesPath, []byte(host+"\t/announce\t"+testHashA+"\thttps://tracker.example/announce\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.state.Mappings[host] = "104.16.0.1"
	a.state.LastRefresh = now.Add(-10 * time.Minute).Format(time.RFC3339)
	a.state.DomainHealth[host] = DomainHealth{FailureStreak: 5, LastFailure: now.Add(-time.Hour).Format(time.RFC3339)}
	last := a.state.LastRefresh
	if err := a.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.LastRefresh != last {
		t.Fatal("fresh downloader failure forced a refresh")
	}
	if a.state.DomainHealth[host].FailureStreak != 6 {
		t.Fatal("Tracker failure not recorded")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "calls")); !os.IsNotExist(err) {
		t.Fatal("fresh failure bypassed cooldown")
	}
	if !strings.Contains(strings.Join(a.state.Logs, "\n"), "failed from downloader runtime") {
		t.Fatal("test did not exercise downloader failure path")
	}
}

func TestCandidateCachePreservesTTLAndMeasurementProvenance(t *testing.T) {
	a, cfg := newRegressionApp(t)
	now := time.Now()
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	a.state.Candidates = []Candidate{
		{IP: "104.16.0.1", SpeedMB: 10, ObservedAt: fresh},
		{IP: "104.16.0.2", SpeedMB: 20, ObservedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		{IP: "104.16.0.3", SpeedMB: 30, ObservedAt: fresh},
	}
	a.storeCandidates([]Candidate{{IP: "104.16.0.3", LatencyOnly: true, ObservedAt: now.Format(time.RFC3339)}}, cfg)
	if len(a.state.Candidates) != 2 {
		t.Fatalf("TTL merge: %+v", a.state.Candidates)
	}
	for _, c := range a.state.Candidates {
		if c.IP == "104.16.0.1" && c.ObservedAt != fresh {
			t.Fatal("old measurement falsely refreshed")
		}
		if c.IP == "104.16.0.3" && (!c.LatencyOnly || c.SpeedMB != 0) {
			t.Fatal("old bandwidth leaked into new latency result")
		}
	}
}

func TestWaitingForProviderDoesNotCountAsSiteFailure(t *testing.T) {
	a, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, a)
	cfg.Domains = []Domain{{Host: "new.example", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true}}
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{Active: true, Until: time.Now().Add(time.Hour).Format(time.RFC3339)}
	_ = a.runSmartRepair(context.Background(), cfg)
	if a.state.DomainHealth["new.example"].FailureStreak != 0 {
		t.Fatal("infrastructure wait counted as site failure")
	}
	_ = a.runDomainMaintenance(context.Background(), cfg, "new.example")
	if a.state.DomainHealth["new.example"].FailureStreak != 0 {
		t.Fatal("manual wait counted as site failure")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "calls")); !os.IsNotExist(err) {
		t.Fatal("provider contacted during cooldown")
	}
}
