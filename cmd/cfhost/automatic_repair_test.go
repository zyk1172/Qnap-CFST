package main

import (
	"context"
	"fmt"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func shortAutomaticPolicy() automaticRepairPolicy {
	return automaticRepairPolicy{Slice: 180 * time.Millisecond, Domain: 70 * time.Millisecond, ContinueAfter: 10 * time.Millisecond, Workers: 2}
}

func prepareAutomaticApp(t *testing.T) (*App, Config) {
	t.Helper()
	a, cfg := newRegressionApp(t)
	cfg.AutoRepair = true
	cfg.Tracker.RealAnnounce = false
	cfg.Tracker.AutoDiscover = false
	cfg.Domains = nil
	a.config = cfg
	return a, cfg
}

func TestAutomaticSliceBoundsStallsAndCommitsOtherDomains(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	for i := 0; i < 5; i++ {
		host := fmt.Sprintf("slow%d.example", i)
		cfg.Domains = append(cfg.Domains, Domain{Host: host, Class: "latency", Mode: "http", Enabled: true})
		a.state.Mappings[host] = "104.16.0.1"
	}
	cfg.Domains = append(cfg.Domains, Domain{Host: "healthy.example", Class: "latency", Mode: "http", Enabled: true})
	a.state.Mappings["healthy.example"] = "104.16.0.2"
	a.config = cfg
	var active, max atomic.Int32
	probe := func(ctx context.Context, d Domain, ip string, cfg Config, samples map[string]TrackerSample) (bool, string) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := max.Load(); n > old && !max.CompareAndSwap(old, n); old = max.Load() {
		}
		if d.Host == "healthy.example" {
			return true, "HTTP 200"
		}
		<-ctx.Done()
		return false, ctx.Err().Error()
	}
	start := time.Now()
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second || max.Load() != 2 || active.Load() != 0 {
		t.Fatalf("unbounded work: elapsed=%s concurrency=%d active=%d", time.Since(start), max.Load(), active.Load())
	}
	t.Logf("synthetic stalled probes: slice=%s elapsed=%s max-concurrency=%d unfinished=%d", shortAutomaticPolicy().Slice, time.Since(start), max.Load(), len(a.state.AutomaticRepair.Pending))
	if len(a.state.AutomaticRepair.Pending) == 0 {
		t.Fatal("unfinished work disappeared")
	}
	for _, h := range a.state.DomainHealth {
		if h.FailureStreak != 0 {
			t.Fatal("slice cancellation counted as failure")
		}
	}
	// Unstarted domains take precedence over repeatedly slow domains next time.
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if a.state.DomainHealth["healthy.example"].LastSuccess == "" {
		t.Fatal("later healthy domain starved")
	}
	if !strings.Contains(strings.Join(a.state.Logs, "\n"), "unfinished=") {
		t.Fatal("missing truthful partial-work log")
	}
}

func TestAutomaticCandidateCursorAndCurrentCheckSurviveRestart(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	d := Domain{Host: "retry.example", Class: "latency", Mode: "http", Enabled: true}
	cfg.Domains = []Domain{d, {Host: "already.example", Class: "latency", Mode: "http", Enabled: true}}
	cfg.Verify.CandidateLimit = 1
	a.state.Mappings[d.Host] = "104.16.0.1"
	a.state.Mappings["already.example"] = "104.16.0.9"
	stamp := time.Now().Format(time.RFC3339)
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: stamp, DelayMS: 1}, {IP: "104.16.0.3", ObservedAt: stamp, DelayMS: 2}}
	a.config = cfg
	var mu sync.Mutex
	calls := map[string]int{}
	probe := func(ctx context.Context, d Domain, ip string, cfg Config, samples map[string]TrackerSample) (bool, string) {
		mu.Lock()
		calls[d.Host+ip]++
		mu.Unlock()
		return d.Host == "already.example" || ip == "104.16.0.3", "HTTP 200"
	}
	policy := shortAutomaticPolicy()
	policy.Slice = time.Second
	policy.Domain = time.Second
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, policy, probe); err != nil {
		t.Fatal(err)
	}
	if len(a.state.AutomaticRepair.Pending) != 1 || a.state.DomainHealth[d.Host].FailureStreak != 0 || a.cachedCandidatesRemaining(d.Host) == 0 {
		t.Fatal("batch exhaustion lost progress or counted a failure")
	}
	b := &App{config: cfg, dataDir: a.dataDir}
	b.loadState()
	if err := b.runAutomaticRepairWithPolicy(context.Background(), cfg, policy, probe); err != nil {
		t.Fatal(err)
	}
	if b.state.Mappings[d.Host] != "104.16.0.3" || len(b.state.AutomaticRepair.Pending) != 0 {
		t.Fatal("restart did not resume remaining candidate")
	}
	if calls[d.Host+"104.16.0.1"] != 1 || calls[d.Host+"104.16.0.2"] != 1 || calls["already.example104.16.0.9"] != 1 {
		t.Fatalf("completed work repeated: %v", calls)
	}
}

func TestAutomaticCancellationDoesNotRecordInterruptedCandidate(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	d := Domain{Host: "partial.example", Class: "latency", Mode: "http", Enabled: true}
	cfg.Domains = []Domain{d}
	a.config = cfg
	c := Candidate{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}
	a.state.Candidates = []Candidate{c}
	probe := func(ctx context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		<-ctx.Done()
		return false, ctx.Err().Error()
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if a.state.CandidateSweeps[d.Host].Failed[candidateEvidenceKey(c)] || a.state.DomainHealth[d.Host].FailureStreak > 0 {
		t.Fatal("interrupted probe treated as a real failure")
	}
}

func TestAutomaticAuthenticationFailureSkipsCandidateScanAndKeepsPause(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "auth.example", Class: "latency", Mode: "http", Enabled: true}, {Host: "paused.example", Class: "latency", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Mappings["auth.example"] = "104.16.0.1"
	a.state.DomainHealth["paused.example"] = DomainHealth{FailureStreak: 30}
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}}
	var calls atomic.Int32
	probe := func(_ context.Context, d Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		calls.Add(1)
		if d.Host != "auth.example" {
			t.Error("paused domain probed")
		}
		return false, "HTTP 401"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || a.state.CandidateRefresh.Requested || a.state.DomainHealth["auth.example"].FailureClass != "configuration-or-authentication" {
		t.Fatal("authentication error consumed candidates or queued CFST")
	}
	if a.state.DomainHealth["paused.example"].FailureStreak != 30 {
		t.Fatal("automatic slice bypassed pause")
	}
}

func TestAutomaticProbeGateReusesOnlyTCPFailures(t *testing.T) {
	for _, tcpFailure := range []bool{true, false} {
		t.Run(fmt.Sprint(tcpFailure), func(t *testing.T) {
			gate := &automaticProbeGate{tracker: make(chan struct{}, 1), connectFailures: map[string]time.Time{}}
			calls := 0
			probe := func(ctx context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
				calls++
				if tcpFailure {
					httptrace.ContextClientTrace(ctx).ConnectDone("tcp", "104.16.0.1:443", fmt.Errorf("connection refused"))
				}
				return false, "HTTP 403"
			}
			for _, host := range []string{"a.example", "b.example"} {
				gate.verify(context.Background(), Domain{Host: host}, "104.16.0.1", Config{}, nil, probe)
			}
			want := 2
			if tcpFailure {
				want = 1
			}
			if calls != want {
				t.Fatalf("probes=%d want=%d", calls, want)
			}
		})
	}
}

func TestAutomaticProbeGateSpacesStartsAndSerializesTrackers(t *testing.T) {
	gate := &automaticProbeGate{gap: 20 * time.Millisecond, tracker: make(chan struct{}, 1), connectFailures: map[string]time.Time{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var starts []time.Time
	var active, max atomic.Int32
	probe := func(ctx context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		n := active.Add(1)
		if n > max.Load() {
			max.Store(n)
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		return true, "announce ok"
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gate.verify(context.Background(), Domain{Mode: "tracker"}, "104.16.0.1", Config{Tracker: TrackerConfig{RealAnnounce: true}}, nil, probe)
		}()
	}
	wg.Wait()
	if max.Load() != 1 {
		t.Fatal("concurrent announce probes")
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 19*time.Millisecond {
			t.Fatal("request starts not throttled")
		}
	}
}

func TestAutomaticConfigChangeCannotCommitOldMappings(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "change.example", Class: "latency", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}}
	probe := func(_ context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		a.mu.Lock()
		a.config.Domains = nil
		a.mu.Unlock()
		return true, "HTTP 200"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if len(a.state.Mappings) != 0 || !a.state.AutomaticRepair.Recheck {
		t.Fatal("obsolete config applied or recheck lost")
	}
}

func TestAutomaticContinuationSchedulingAndManualOverride(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "normal.example", Class: "normal", Mode: "http", Enabled: true}}
	a.config = cfg
	now := time.Now()
	a.state.LastRun = now.Format(time.RFC3339)
	a.state.AutomaticRepair = AutomaticRepairRuntime{Pending: map[string]AutomaticDomainWork{"normal.example": {}}, NextCheck: now.Add(time.Second).Format(time.RFC3339Nano)}
	cfg.Repair.CandidateMinCount = 0
	if got := nextScheduledJob(now, cfg, a.state); got != "" {
		t.Fatalf("early continuation: %s", got)
	}
	if got := nextScheduledJob(now.Add(2*time.Second), cfg, a.state); got != "repair" {
		t.Fatalf("continuation waited regular interval: %s", got)
	}
	a.state.DomainHealth["normal.example"] = DomainHealth{FailureStreak: 100, NextCheck: now.Add(time.Hour).Format(time.RFC3339)}
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", SpeedMB: 10, ObservedAt: now.Format(time.RFC3339)}}
	if err := a.runSmartRepair(withManualOperation(context.Background()), cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.Mappings["normal.example"] != "104.16.0.2" || len(a.state.AutomaticRepair.Pending) > 0 || a.state.DomainHealth["normal.example"].FailureStreak != 0 {
		t.Fatal("manual request retained automatic constraints")
	}
}

func TestAutomaticSlicePreservesOneHostsFileAndUnmanagedRules(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.AutoApply = true
	cfg.Domains = []Domain{{Host: "healthy.example", Class: "latency", Mode: "http", Enabled: true}, {Host: "slow.example", Class: "latency", Mode: "http", Enabled: true}}
	a.config = cfg
	if err := os.WriteFile(cfg.HostsPath, []byte("127.0.0.1 localhost\n10.0.0.2 original.local\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(cfg.HostsPath)
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}}
	probe := func(ctx context.Context, d Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		if d.Host == "healthy.example" {
			return true, "HTTP 200"
		}
		<-ctx.Done()
		return false, ctx.Err().Error()
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(cfg.HostsPath)
	b, _ := os.ReadFile(cfg.HostsPath)
	if !os.SameFile(before, after) || !strings.Contains(string(b), "10.0.0.2 original.local") || !strings.Contains(string(b), "104.16.0.2 healthy.example") {
		t.Fatalf("Hosts inode/rules/partial success lost: %s", b)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "downloader-hosts")); !os.IsNotExist(err) {
		t.Fatal("unexpected second Hosts source")
	}
}

func TestAutomaticContinuationsCannotPostponeFullSweep(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Repair.CandidateMinCount = 0
	cfg.Domains = []Domain{{Host: "pending.example", Class: "latency", Mode: "http", Enabled: true}, {Host: "healthy.example", Class: "latency", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Mappings["pending.example"] = "104.16.0.1"
	a.state.Mappings["healthy.example"] = "104.16.0.2"
	a.state.LastRun = time.Now().Format(time.RFC3339)
	a.state.AutomaticRepair = AutomaticRepairRuntime{LastSweep: time.Now().Add(-time.Hour).Format(time.RFC3339Nano), NextCheck: time.Now().Add(time.Hour).Format(time.RFC3339Nano), Pending: map[string]AutomaticDomainWork{"pending.example": {Key: automaticWorkKey(cfg, cfg.Domains[0], "104.16.0.1"), CurrentIP: "104.16.0.1", SkipIP: "104.16.0.1"}}}
	if nextScheduledJob(time.Now(), cfg, a.state) != "repair" {
		t.Fatal("recent continuation postponed regular check")
	}
	var calls atomic.Int32
	probe := func(_ context.Context, _ Domain, _ string, _ Config, _ map[string]TrackerSample) (bool, string) {
		calls.Add(1)
		return true, "HTTP 200"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || a.state.DomainHealth["healthy.example"].LastSuccess == "" {
		t.Fatal("unfinished work starved healthy domain checks")
	}
}

func TestAutomaticExhaustionQueuesOneRefreshWithoutInlineCFST(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	d := Domain{Host: "failed.example", Class: "latency", Mode: "http", Enabled: true}
	cfg.Domains = []Domain{d}
	a.config = cfg
	a.state.Mappings[d.Host] = "104.16.0.1"
	a.state.DomainHealth[d.Host] = DomainHealth{FailureStreak: 1}
	probe := func(_ context.Context, _ Domain, _ string, c Config, _ map[string]TrackerSample) (bool, string) {
		if c.Verify.HTTPRetries != 1 || c.Tracker.Retries != 1 {
			t.Error("automatic inline retries retained")
		}
		return false, "HTTP 503"
	}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if !a.state.CandidateRefresh.Requested || a.state.CandidateRefresh.Running || a.state.DomainHealth[d.Host].FailureStreak != 2 {
		t.Fatal("exhaustion did not queue background CFST exactly once")
	}
	// Reaching the existing maintenance pause must suppress new scans too.
	a.state.CandidateRefresh = CandidateRefreshRuntime{}
	a.state.DomainHealth[d.Host] = DomainHealth{FailureStreak: 29}
	a.state.AutomaticRepair = AutomaticRepairRuntime{}
	if err := a.runAutomaticRepairWithPolicy(context.Background(), cfg, shortAutomaticPolicy(), probe); err != nil {
		t.Fatal(err)
	}
	if a.state.CandidateRefresh.Requested || a.state.DomainHealth[d.Host].FailureStreak != 30 {
		t.Fatal("paused site triggered CFST")
	}
}

func TestAutomaticConfigDefaultsRemainCompatibleWithLongManualTimeout(t *testing.T) {
	cfg := defaultConfig()
	cfg.VerifyTimeoutSeconds = 120
	cfg.Repair.TimeSliceSeconds = 0
	cfg.Repair.DomainSliceSeconds = 0
	cfg.Repair.Concurrency = 0
	cfg.Repair.ContinueAfterSeconds = 0
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.VerifyTimeoutSeconds != 120 || cfg.Repair.TimeSliceSeconds != 120 || cfg.Repair.Concurrency != 3 {
		t.Fatal("migration changed legacy/manual configuration")
	}
	cfg.Repair.Concurrency = 9
	if err := normalizeConfig(&cfg); err == nil {
		t.Fatal("unbounded worker count accepted")
	}
}

func TestAutomaticJobRouterRecordsStagesBeforeReleasingSlot(t *testing.T) {
	a, cfg := prepareAutomaticApp(t)
	cfg.Domains = []Domain{{Host: "normal.example", Class: "normal", Mode: "http", Enabled: true}}
	a.config = cfg
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", SpeedMB: 40, ObservedAt: time.Now().Format(time.RFC3339)}}
	if !a.startJob("repair") {
		t.Fatal("automatic job did not start")
	}
	waitForCondition(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return !a.state.Running })
	if len(a.state.History) != 1 || len(a.state.History[0].Stages) == 0 || a.state.Mappings["normal.example"] != "104.16.0.2" {
		t.Fatal("scheduler did not use checkpointed automatic route")
	}
	b := &App{dataDir: a.dataDir}
	b.loadState()
	if len(b.state.History) != 1 {
		t.Fatal("job slot released before history persisted")
	}
}
