package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManualAPIBypassesAllCFSTAdmissionLimitsButUpdatesAutomaticBudget(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.CFST.DownloadCount = 17
	a.config = cfg
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
echo call >> calls
printf '%s|%s\n' "$CFST_BLOCKED_ORIGINS" "$CFST_MAX_DOWNLOAD_ATTEMPTS" >> admission-env
printf '%s\n' "$@" > admission-args
while [ "$#" -gt 0 ]; do case "$1" in -o) output="$2"; shift 2 ;; *) shift ;; esac; done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.1,4,4,0,20,30,SIN\n' > "$output"
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.state.LastRefresh = time.Now().Format(time.RFC3339)
	until := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	a.state.CFSTSources = map[string]CFSTSourceRuntime{}
	for _, raw := range configuredSources(cfg) {
		a.state.CFSTSources[sourceOrigin(raw)] = CFSTSourceRuntime{Failures: 3, RetryAt: until, RateLimit: CFSTRateLimitRuntime{Active: true, Until: until, DetectedAt: a.state.LastRefresh, Consecutive: 2}}
	}
	if _, err := a.runCFST(context.Background(), cfg); err == nil {
		t.Fatal("automatic entry bypassed budget")
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/run", nil))
	if w.Code != 202 {
		t.Fatalf("manual API: %d", w.Code)
	}
	for i := 0; i < 200; i++ {
		a.mu.RLock()
		running := a.state.Running
		a.mu.RUnlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.RLock()
	running := a.state.Running
	lastErr := a.state.LastError
	manual := a.state.CFSTSchedule.Manual
	a.mu.RUnlock()
	if running || lastErr != "" || !manual {
		t.Fatalf("manual admission failed: running=%v err=%s manual=%v", running, lastErr, manual)
	}
	env, _ := os.ReadFile(filepath.Join(a.dataDir, "admission-env"))
	args, _ := os.ReadFile(filepath.Join(a.dataDir, "admission-args"))
	if string(env) != "|17\n" || !strings.Contains(string(args), "-dn\n17\n") {
		t.Fatal("manual request retained backend block or recovery-size cap")
	}
	if _, err := a.runCFST(context.Background(), cfg); err == nil {
		t.Fatal("manual work erased automatic budget")
	}
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("unexpected scans: %q", calls)
	}
}

func TestManualRepairBypassesDomainPauseRetryAndCandidateBatch(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.Domains = []Domain{{Host: "manual.example", Enabled: true, Class: "normal", Mode: "http"}}
	a.state.Mappings = map[string]string{"manual.example": "104.16.0.1"}
	a.state.DomainHealth["manual.example"] = DomainHealth{FailureStreak: 131, NextCheck: time.Now().Add(12 * time.Hour).Format(time.RFC3339)}
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", DelayMS: 10, SpeedMB: 20, ObservedAt: time.Now().Format(time.RFC3339)}}
	if err := a.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.Mappings["manual.example"] != "104.16.0.1" {
		t.Fatal("automatic work bypassed pause")
	}
	ctx := withManualOperation(context.Background())
	if err := a.runSmartRepair(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.Mappings["manual.example"] != "104.16.0.2" || a.state.DomainHealth["manual.example"].FailureStreak != 0 {
		t.Fatal("manual repair was filtered by pause/retry")
	}
	d := Domain{Host: "batch.example", Class: "latency"}
	cfg.Domains = []Domain{d}
	cfg.Verify.CandidateLimit = 1
	all := []Candidate{{IP: "104.16.0.3"}, {IP: "104.16.0.4"}, {IP: "104.16.0.5"}}
	a.candidateBatch(all, d, "", cfg)
	a.recordCandidateAttempt(d.Host, all[0], false)
	a.resetManualSweeps(ctx, cfg)
	if got := a.candidateBatch(all, d, "", manualConfig(ctx, cfg)); len(got) != 3 {
		t.Fatal("manual verification retained previous failures or batch limit")
	}
	if got := manualAnnounceIDs(ctx, []int{1, 1, 2}); len(got) != 2 {
		t.Fatal("manual RPC targets not unique")
	}
	if got := manualAnnounceIDs(ctx, []int{1, 2}); len(got) != 0 {
		t.Fatal("same action repeated RPC targets")
	}
}

func TestManualOperationKeepsHostsProtectionAndRecordsFresh429(t *testing.T) {
	a, cfg, _ := replacedHostsApp(t)
	if err := a.runFullOptimize(withManualOperation(context.Background()), cfg); err == nil || !a.state.HostsMaintenance.Blocked {
		t.Fatal("manual budget override bypassed Hosts protection")
	}
	b, cfg := newRegressionApp(t)
	fakeLimitedCFST(t, b)
	if _, err := b.runCFST(withManualOperation(context.Background()), cfg); err != nil {
		t.Fatal(err)
	}
	if !b.state.CFSTRateLimit.Active {
		t.Fatal("manual request ignored new server rate-limit feedback")
	}
	if _, err := b.runCFST(context.Background(), cfg); err == nil {
		t.Fatal("automatic retry ignored fresh 429")
	}
	calls, _ := os.ReadFile(filepath.Join(b.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatal("new 429 caused immediate retry")
	}
}

func TestAutomaticDeferredOptimizeDoesNotFailOrPostponeRepair(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.Domains = []Domain{{Host: "normal.example", Enabled: true, Class: "normal", Mode: "http"}}
	a.config = cfg
	old := time.Now().Add(-time.Hour).Format(time.RFC3339)
	a.state.LastRun = old
	a.state.LastSuccess = old
	a.state.LastRefresh = time.Now().Format(time.RFC3339)
	if !a.startJob("optimize") {
		t.Fatal("automatic job did not start")
	}
	for i := 0; i < 200; i++ {
		a.mu.RLock()
		running := a.state.Running
		a.mu.RUnlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state.Running || a.state.LastError != "" || a.state.LastRun != old || a.state.LastSuccess != old {
		t.Fatal("budget deferral was recorded as failure/success or postponed Repair")
	}
	if len(a.state.History) != 1 || !strings.Contains(a.state.History[0].Error, "deferred") {
		t.Fatal("missing auditable budget-wait record")
	}
}
