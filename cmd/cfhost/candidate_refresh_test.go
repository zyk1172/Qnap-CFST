package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for task")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func blockingCandidateRefresh(t *testing.T, a *App) {
	t.Helper()
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
echo call >> calls
while [ "$#" -gt 0 ]; do case "$1" in -o) output="$2"; shift 2 ;; *) shift ;; esac; done
while [ ! -e release ]; do sleep 0.01; done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.2,4,4,0,20,30,SIN\n' > "$output"
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(a.dataDir, "release"), nil, 0600)
		waitForCondition(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return !a.state.CandidateRefresh.Running })
	})
}

func TestCandidateRefreshDoesNotOccupyResolutionSlotOrLoseConcurrentHandoff(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "repair.example", Class: "latency", Mode: "http", Enabled: true}, {Host: "other.example", Class: "normal", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Mappings["repair.example"] = "104.16.0.1"
	a.state.DomainHealth["repair.example"] = DomainHealth{FailureStreak: 2, NextCheck: time.Now().Add(time.Hour).Format(time.RFC3339)}
	blockingCandidateRefresh(t, a)
	if err := a.requestCandidateRefresh("test repair", []string{"repair.example"}, false); err != nil {
		t.Fatal(err)
	}
	if !a.startCandidateRefresh(time.Now()) {
		t.Fatal("background scan did not start")
	}
	waitForCondition(t, func() bool { _, err := os.Stat(filepath.Join(a.dataDir, "calls")); return err == nil })
	if a.startCandidateRefresh(time.Now()) {
		t.Fatal("duplicate background scan")
	}
	a.mu.RLock()
	slotBusy := a.state.Running
	a.mu.RUnlock()
	if slotBusy {
		t.Fatal("candidate generator occupied resolution job slot")
	}
	// Finish CFST while a foreground slice is inside its domain probe. Its
	// final checkpoint must not overwrite the independently queued handoff.
	cfg.Domains = append(cfg.Domains, Domain{Host: "live.example", Class: "latency", Mode: "http", Enabled: true})
	a.mu.Lock()
	a.config = cfg
	a.state.Mappings["live.example"] = "104.16.0.9"
	a.mu.Unlock()
	probe := func(_ context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		if err := os.WriteFile(filepath.Join(a.dataDir, "release"), nil, 0600); err != nil {
			t.Error(err)
		}
		waitForCondition(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return !a.state.CandidateRefresh.Running })
		return true, "HTTP 200"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	ready := append([]string(nil), a.state.CandidateRefresh.ReadyTargets...)
	a.mu.RUnlock()
	if len(ready) != 1 || ready[0] != "repair.example" {
		t.Fatalf("concurrent CFST handoff lost: %v", ready)
	}
	if got := nextScheduledJob(time.Now(), cfg, a.state); got != "repair" {
		t.Fatalf("new candidates did not wake repair: %s", got)
	}
	probe = func(_ context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		return true, "HTTP 200"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if a.state.Mappings["repair.example"] != "104.16.0.2" || a.state.DomainHealth["repair.example"].FailureStreak != 0 {
		t.Fatal("fresh candidates did not recover failed target")
	}
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("duplicate scans: %q", calls)
	}
}

func TestCandidateRefreshCoalescesAndPersistsBudgetsAcrossRestart(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "a.example", Class: "latency", Enabled: true}, {Host: "b.example", Class: "latency", Enabled: true}}
	a.config = cfg
	a.state.LastRefresh = time.Now().Format(time.RFC3339)
	if err := a.requestCandidateRefresh("one", []string{"a.example"}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.requestCandidateRefresh("two", []string{"b.example", "a.example"}, false); err != nil {
		t.Fatal(err)
	}
	if len(a.state.CandidateRefresh.Targets) != 2 || a.startCandidateRefresh(time.Now()) {
		t.Fatal("request deduplication or cooldown broken")
	}
	a.mu.Lock()
	a.state.CandidateRefresh.Running = true
	a.mu.Unlock()
	a.persistState()
	b := &App{config: cfg, dataDir: a.dataDir}
	b.loadState()
	if b.state.CandidateRefresh.Running || !b.state.CandidateRefresh.Requested || b.startCandidateRefresh(time.Now()) {
		t.Fatal("restart lost queue or bypassed persisted CFST budget")
	}
}

func TestCandidateRefreshHonorsHostsPauseAndNew429(t *testing.T) {
	a, cfg, _ := replacedHostsApp(t)
	if err := a.requestCandidateRefresh("blocked", nil, false); err != nil {
		t.Fatal(err)
	}
	if a.startCandidateRefresh(time.Now()) || !a.state.HostsMaintenance.Blocked {
		t.Fatal("invalid Hosts allowed background network work")
	}
	b, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "active.example", Class: "latency", Enabled: true}}
	b.config = cfg
	fakeLimitedCFST(t, b)
	if err := b.requestCandidateRefresh("limit", nil, false); err != nil {
		t.Fatal(err)
	}
	if !b.startCandidateRefresh(time.Now()) {
		t.Fatal("refresh did not start")
	}
	waitForCondition(t, func() bool { b.mu.RLock(); defer b.mu.RUnlock(); return !b.state.CandidateRefresh.Running })
	if !b.state.CFSTRateLimit.Active {
		t.Fatal("new 429 lost")
	}
	if err := b.requestCandidateRefresh("again", nil, false); err != nil {
		t.Fatal(err)
	}
	if b.startCandidateRefresh(time.Now()) {
		t.Fatal("automatic refresh ignored fresh server cooldown")
	}
	calls, _ := os.ReadFile(filepath.Join(b.dataDir, "calls"))
	if strings.Count(string(calls), "call") != 1 {
		t.Fatalf("429 caused extra scan: %q", calls)
	}
}

func TestAutomaticOptimizeUsesBackgroundScanThenCheckpointedResolution(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.AutoRepair = false
	cfg.Optimize.ScheduledFull = true
	cfg.Domains = []Domain{{Host: "opt.example", Class: "normal", Mode: "http", Enabled: true}}
	a.config = cfg
	blockingCandidateRefresh(t, a)
	if err := a.requestAutomaticOptimize(cfg); err != nil {
		t.Fatal(err)
	}
	if !a.state.CandidateRefresh.Optimize || a.state.LastOptimize != "" {
		t.Fatal("queued optimization treated as already applied")
	}
	if !a.startCandidateRefresh(time.Now()) {
		t.Fatal("scheduled optimization requires AutoRepair unexpectedly")
	}
	if err := os.WriteFile(filepath.Join(a.dataDir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return !a.state.CandidateRefresh.Running })
	if !a.state.CandidateRefresh.ReadyOptimize || nextScheduledJob(time.Now(), cfg, a.state) != "repair" {
		t.Fatal("optimization handoff lost")
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), a.verifyDomain); err != nil {
		t.Fatal(err)
	}
	if a.state.LastOptimize == "" || a.state.Mappings["opt.example"] != "104.16.0.2" || a.state.AutomaticRepair.Optimize {
		t.Fatal("optimization did not complete via bounded repair")
	}
}

func TestManualRepairCancelsStaleAutomaticHandoffDuringCFST(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Optimize.ScheduledFull = true
	cfg.Domains = []Domain{{Host: "manual.example", Class: "normal", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Candidates = []Candidate{{IP: "104.16.0.7", SpeedMB: 40, ObservedAt: time.Now().Format(time.RFC3339)}}
	blockingCandidateRefresh(t, a)
	if err := a.requestCandidateRefresh("old request", []string{"manual.example"}, true); err != nil {
		t.Fatal(err)
	}
	if !a.startCandidateRefresh(time.Now()) {
		t.Fatal("scan did not start")
	}
	if err := a.runSmartRepair(withManualOperation(context.Background()), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.dataDir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return !a.state.CandidateRefresh.Running })
	if a.state.Mappings["manual.example"] != "104.16.0.7" || len(a.state.CandidateRefresh.ReadyTargets) > 0 || a.state.CandidateRefresh.ReadyOptimize {
		t.Fatal("background completion overrode manual resolution")
	}
}

func TestQueuedAutomaticOptimizeDoesNotPostponeRepairOrClaimSuccess(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Optimize.ScheduledFull = true
	cfg.Domains = []Domain{{Host: "opt.example", Class: "normal", Mode: "http", Enabled: true}}
	a.config = cfg
	old := time.Now().Add(-time.Hour).Format(time.RFC3339)
	a.state.LastRun = old
	a.state.LastSuccess = old
	if !a.startJob("optimize") {
		t.Fatal("optimization queue job did not start")
	}
	waitForCondition(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return !a.state.Running })
	if a.state.LastRun != old || a.state.LastSuccess != old || len(a.state.History) != 1 || !a.state.History[0].Queued {
		t.Fatal("queue admission claimed completed resolution or postponed Repair")
	}
	if nextScheduledJob(time.Now(), cfg, a.state) != "repair" {
		t.Fatal("queued CFST starved overdue Repair")
	}
}

func TestPausedRequestsAndEmptyOptimizationCannotLoop(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "paused.example", Class: "latency", Enabled: true}}
	cfg.Optimize.ScheduledFull = true
	a.config = cfg
	a.state.DomainHealth["paused.example"] = DomainHealth{FailureStreak: 30}
	if err := a.requestCandidateRefresh("old failure", []string{"paused.example"}, false); err != nil {
		t.Fatal(err)
	}
	if a.startCandidateRefresh(time.Now()) || a.state.CandidateRefresh.Requested {
		t.Fatal("paused target retained a scan request")
	}
	if err := a.requestAutomaticOptimize(cfg); err != nil {
		t.Fatal(err)
	}
	if optimizeDue(time.Now(), a.state.LastOptimize, a.state.LastOptimizeAttempt, a.state.LastRefresh, cfg.Optimize) {
		t.Fatal("no-active-domain optimization repeated every scheduler tick")
	}
}

func TestCompletedForegroundRecoveryMakesOldCandidateHandoffObsolete(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "recovered.example", Class: "latency", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Mappings["recovered.example"] = "104.16.0.1"
	a.state.DomainHealth["recovered.example"] = DomainHealth{LastSuccess: time.Now().Format(time.RFC3339Nano)}
	a.state.AutomaticRepair.LastSweep = time.Now().Format(time.RFC3339Nano)
	a.state.CandidateRefresh = CandidateRefreshRuntime{ReadyTargets: []string{"recovered.example"}, FinishedAt: time.Now().Add(-time.Second).Format(time.RFC3339Nano)}
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}}
	calls := 0
	probe := func(_ context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		calls++
		return true, "HTTP 200"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || a.state.Mappings["recovered.example"] != "104.16.0.1" || len(a.state.CandidateRefresh.ReadyTargets) > 0 {
		t.Fatal("obsolete handoff re-probed or switched a recovered domain")
	}
}
