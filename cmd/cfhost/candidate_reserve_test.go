package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestExpiredReserveSchedulesWithoutUnhealthyDomains(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoRepair = true
	cfg.Domains = []Domain{{Host: "healthy.example", Enabled: true, Class: "latency"}}
	now := time.Now().Truncate(time.Second)
	a.state.LastRun = now.Format(time.RFC3339)
	a.state.LastRefresh = now.Add(-48 * time.Hour).Format(time.RFC3339)
	a.state.Mappings["healthy.example"] = "104.16.0.1"
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", SpeedMB: 30, ObservedAt: a.state.LastRefresh}}
	view := candidateReserveStatus(a.state, cfg, now)
	if view.Status != "ready" || view.FreshCount != 0 || view.UsableCount != 0 {
		t.Fatalf("expired observations counted as usable: %+v", view)
	}
	if got := nextScheduledJob(now, cfg, a.state); got != "reserve" {
		t.Fatalf("healthy mappings prevented replenishment: %q", got)
	}
	a.state.LastRun = now.Add(-time.Hour).Format(time.RFC3339)
	if got := nextScheduledJob(now, cfg, a.state); got != "repair" {
		t.Fatalf("overdue repair lost priority: %q", got)
	}
	a.state.LastRun = now.Format(time.RFC3339)
	a.state.TrackerKeepalive["healthy.example"] = TrackerKeepaliveRuntime{NextCheck: now.Add(-time.Minute).Format(time.RFC3339)}
	if got := nextScheduledJob(now, cfg, a.state); got != "repair" {
		t.Fatalf("keepalive lost priority: %q", got)
	}
	delete(a.state.TrackerKeepalive, "healthy.example")
	cfg.Optimize.ScheduledFull = true
	if got := nextScheduledJob(now, cfg, a.state); got != "optimize" {
		t.Fatalf("scheduled optimization lost priority: %q", got)
	}
	a.state.Running = true
	if got := nextScheduledJob(now, cfg, a.state); got != "" {
		t.Fatalf("overlapping job scheduled: %q", got)
	}
}

func TestReserveCountsOnlyDistinctFreshEligibleCandidates(t *testing.T) {
	cfg := defaultConfig()
	cfg.AutoRepair = true
	cfg.Repair.CandidateMinCount = 2
	cfg.Domains = []Domain{{Host: "latency.example", Enabled: true, Class: "latency"}}
	now := time.Now().Truncate(time.Second)
	fresh := now.Format(time.RFC3339)
	s := RuntimeState{Candidates: []Candidate{
		{IP: "104.16.0.1", LatencyOnly: true, ObservedAt: fresh},
		{IP: "104.16.0.1", LatencyOnly: true, ObservedAt: fresh},
		{IP: "104.16.0.2", ObservedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		{IP: "104.16.0.3", ObservedAt: now.Add(time.Hour).Format(time.RFC3339)},
		{IP: "104.16.0.4"},
		{IP: "192.168.1.1", ObservedAt: fresh},
	}}
	if v := candidateReserveStatus(s, cfg, now); v.FreshCount != 1 || v.UsableCount != 1 || v.Status != "ready" {
		t.Fatalf("bad freshness/deduplication: %+v", v)
	}
	cfg.Domains = append(cfg.Domains, Domain{Host: "bandwidth.example", Enabled: true, Class: "bandwidth"})
	if v := candidateReserveStatus(s, cfg, now); v.UsableCount != 0 {
		t.Fatalf("latency-only pool concealed empty bandwidth reserve: %+v", v)
	}
	s.Candidates = []Candidate{{IP: "104.16.0.1", SpeedMB: 30, ObservedAt: fresh}, {IP: "104.16.0.2", SpeedMB: 30, ObservedAt: fresh}}
	if v := candidateReserveStatus(s, cfg, now); v.Status != "sufficient" || v.UsableCount != 2 {
		t.Fatalf("sufficient reserve not recognized: %+v", v)
	}
}

func TestReserveHonorsIntervalAndAllSourceCooldowns(t *testing.T) {
	cfg := defaultConfig()
	cfg.AutoRepair = true
	now := time.Now().Truncate(time.Second)
	s := RuntimeState{LastRefresh: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	v := candidateReserveStatus(s, cfg, now)
	at, _ := time.Parse(time.RFC3339, v.NextAllowedAt)
	if v.Status != "waiting" || at.Before(now.Add(4*time.Hour)) {
		t.Fatalf("reserve interval bypassed: %+v", v)
	}
	cfg.Repair.CandidateRefreshMinutes = 1
	s.LastRefresh = now.Add(-30 * time.Minute).Format(time.RFC3339)
	v = candidateReserveStatus(s, cfg, now)
	at, _ = time.Parse(time.RFC3339, v.NextAllowedAt)
	if v.Status != "waiting" || at.Before(now.Add(30*time.Minute)) {
		t.Fatalf("short reserve interval bypassed global budget: %+v", v)
	}
	s.LastRefresh = now.Add(-48 * time.Hour).Format(time.RFC3339)
	until := now.Add(8 * time.Hour).Format(time.RFC3339)
	s.CFSTSources = map[string]CFSTSourceRuntime{}
	for _, raw := range configuredSources(cfg) {
		s.CFSTSources[sourceOrigin(raw)] = CFSTSourceRuntime{RateLimit: CFSTRateLimitRuntime{Active: true, Until: until, DetectedAt: now.Add(-time.Minute).Format(time.RFC3339)}}
	}
	if v := candidateReserveStatus(s, cfg, now); v.Status != "waiting" || v.NextAllowedAt != until {
		t.Fatalf("source cooldown bypassed: %+v", v)
	}
	delete(s.CFSTSources, sourceOrigin(cfg.CFST.DownloadURLs[0]))
	if v := candidateReserveStatus(s, cfg, now); v.Status != "ready" {
		t.Fatalf("available alternate source was blocked: %+v", v)
	}
}

func TestReserveRespectsDisabledAndPausedDomains(t *testing.T) {
	cfg := defaultConfig()
	cfg.AutoRepair = true
	cfg.Domains = []Domain{{Host: "paused.example", Enabled: true, Class: "latency"}, {Host: "disabled.example", Class: "bandwidth"}, {Host: "follow.example", Enabled: true, Class: "follow"}}
	s := RuntimeState{DomainHealth: map[string]DomainHealth{"paused.example": {FailureStreak: maintenanceFailureLimit}}}
	if v := candidateReserveStatus(s, cfg, time.Now()); v.Status != "disabled" {
		t.Fatalf("paused domains triggered scans: %+v", v)
	}
	cfg.Domains[0].Class = "normal"
	cfg.Domains[0].Host = "active.example"
	cfg.AutoRepair = false
	if v := candidateReserveStatus(s, cfg, time.Now()); v.Status != "disabled" {
		t.Fatal("automatic scans enabled without AutoRepair")
	}
	cfg.AutoRepair = true
	cfg.Repair.CandidateMinCount = 0
	if v := candidateReserveStatus(s, cfg, time.Now()); v.Status != "disabled" {
		t.Fatal("explicit disable ignored")
	}
}

func TestReserveJobPreservesResolutionAndPersistsCandidates(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoRepair = true
	cfg.AutoApply = true
	cfg.Sync.Enabled = true
	cfg.Repair.CandidateMinCount = 10
	cfg.Domains = []Domain{{Host: "healthy.example", Enabled: true, Class: "latency"}, {Host: "paused.example", Enabled: true, Class: "latency"}}
	a.state.Mappings = map[string]string{"healthy.example": "104.16.0.1", "paused.example": "104.16.0.3"}
	a.state.DomainHealth["paused.example"] = DomainHealth{FailureStreak: maintenanceFailureLimit, LastFailure: "2026-11-01T00:00:00Z"}
	a.state.DomainStatus["healthy.example"] = "retained"
	a.state.TrackerKeepalive["healthy.example"] = TrackerKeepaliveRuntime{Attempts: 3}
	a.state.Candidates = []Candidate{{IP: "104.16.0.4", SpeedMB: 10, ObservedAt: time.Now().Format(time.RFC3339)}}
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
echo call >> calls
while [ "$#" -gt 0 ]; do
 case "$1" in -o) output="$2"; shift 2 ;; *) shift ;; esac
done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.2,4,4,0,30,20,SIN\n' > "$output"
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	before := a.snapshotResolution()
	if err := os.WriteFile(cfg.HostsPath, []byte("127.0.0.1 localhost\n104.16.0.1 healthy.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hosts, err := os.ReadFile(cfg.HostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.runJob(context.Background(), "reserve", cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, a.snapshotResolution()) {
		t.Fatal("reserve job changed resolution or paused-domain state")
	}
	after, _ := os.ReadFile(cfg.HostsPath)
	if string(after) != string(hosts) || a.state.Sync.LastAttempt != "" {
		t.Fatal("reserve job applied Hosts or published sync")
	}
	b, _ := newRegressionApp(t)
	b.dataDir = a.dataDir
	b.cfstBin = a.cfstBin
	b.loadState()
	if len(b.state.Candidates) != 2 {
		t.Fatalf("merged candidates not persisted: %+v", b.state.Candidates)
	}
	if err := b.runJob(context.Background(), "reserve", cfg); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("sparse result caused repeated scan after restart: %q", calls)
	}
}

func TestReserveFailurePersistsBudgetWithoutChangingMappings(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoRepair = true
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	if err := os.WriteFile(a.cfstBin, []byte("#!/bin/sh\necho call >> calls\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a.state.Mappings["healthy.example"] = "104.16.0.1"
	before := a.snapshotResolution()
	if err := a.runJob(context.Background(), "reserve", cfg); err == nil {
		t.Fatal("failed scan reported success")
	}
	if !reflect.DeepEqual(before, a.snapshotResolution()) {
		t.Fatal("failed reserve changed resolution")
	}
	b, _ := newRegressionApp(t)
	b.dataDir = a.dataDir
	b.cfstBin = a.cfstBin
	b.loadState()
	if err := b.runJob(context.Background(), "reserve", cfg); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(a.dataDir, "calls"))
	if string(calls) != "call\n" {
		t.Fatalf("failed scan retried immediately: %q", calls)
	}
}

func TestLegacyReserveDefaultsAndStatusProjection(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		raw  string
		want int
	}{
		{`{"repair":{"candidateTTLMinutes":1440}}`, 10},
		{`{"repair":{"candidateMinCount":0}}`, 0},
	} {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(test.raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(dir)
		if err != nil || cfg.Repair.CandidateMinCount != test.want || cfg.Repair.CandidateRefreshMinutes != 360 {
			t.Fatalf("migration: %+v %v", cfg.Repair, err)
		}
	}
	a, cfg := newRegressionApp(t)
	cfg.AutoRepair = true
	a.config = cfg
	before, _ := json.Marshal(a.state)
	w := httptest.NewRecorder()
	a.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var response struct {
		CandidateReserve CandidateReserveStatus `json:"candidateReserve"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.CandidateReserve.Status != "ready" {
		t.Fatalf("missing live reserve status: %s %v", w.Body.String(), err)
	}
	after, _ := json.Marshal(a.state)
	if string(before) != string(after) {
		t.Fatal("status projection mutated runtime state")
	}
}
