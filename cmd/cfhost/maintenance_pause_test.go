package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenancePauseThresholdAndRestart(t *testing.T) {
	a, cfg := newRegressionApp(t)
	const host = "tracker.closed.example"
	cfg.Domains = []Domain{{Host: host, Class: "latency", Mode: "tracker", Enabled: true}}
	cfg.Tracker.Transmission.Enabled = false
	cfg.AutoApply = false
	a.config = cfg
	a.state.DomainHealth[host] = DomainHealth{FailureStreak: 29}
	a.state.Mappings[host] = "104.16.0.1"
	a.cfstBin = filepath.Join(a.dataDir, "must-not-run-cfst")
	if err := a.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.DomainHealth[host].FailureStreak != 30 {
		t.Fatalf("30th failure must pause: %+v", a.state.DomainHealth[host])
	}
	if !strings.Contains(a.state.DomainStatus[host], "maintenance paused") {
		t.Fatal(a.state.DomainStatus[host])
	}
	restarted, _ := newRegressionApp(t)
	restarted.dataDir = a.dataDir
	restarted.config = cfg
	restarted.loadState()
	if !maintenancePaused(cfg.Domains[0], restarted.state.DomainHealth) {
		t.Fatal("pause lost on restart")
	}
	if err := restarted.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if restarted.state.DomainHealth[host].FailureStreak != 30 {
		t.Fatal("paused domain retried or reset")
	}
	if restarted.state.Mappings[host] != "104.16.0.1" {
		t.Fatal("pause changed mapping")
	}
}

func TestLegacyPausedDomainDoesNotTriggerCFSTOrBlockOtherDomains(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoApply = false
	cfg.Domains = []Domain{
		{Host: "offline.example", Class: "latency", Mode: "tracker", Enabled: true},
		{Host: "active.example", Class: "normal", Mode: "http", Enabled: true},
	}
	cfg.Tracker.Transmission.Enabled = false
	a.config = cfg
	a.state.DomainHealth["offline.example"] = DomainHealth{FailureStreak: 131}
	a.state.Mappings["offline.example"] = "104.16.0.1"
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", ObservedAt: time.Now().Format(time.RFC3339)}}
	a.cfstBin = filepath.Join(a.dataDir, "must-not-run-cfst")
	if err := a.runSmartRepair(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if a.state.DomainHealth["offline.example"].FailureStreak != 131 {
		t.Fatal("legacy pause retried")
	}
	if a.state.Mappings["active.example"] != "104.16.0.2" {
		t.Fatal("other domain was blocked")
	}
	if a.state.LastRefresh != "" {
		t.Fatal("paused domain triggered a global refresh")
	}
	cfg.Domains = cfg.Domains[:1]
	if err := a.runFullOptimize(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if started, err := a.startDomainMaintenance("offline.example"); started || err == nil {
		t.Fatal("manual maintenance bypassed pause")
	}
	if err := a.runDomainMaintenance(context.Background(), cfg, "offline.example"); err == nil {
		t.Fatal("direct maintenance bypassed pause")
	}
}

func TestPausedTrackerDoesNotDiscoverOrKeepalive(t *testing.T) {
	a, cfg := newRegressionApp(t)
	const host = "tracker.offline.example"
	cfg.Domains = []Domain{{Host: host, Class: "latency", Mode: "tracker", Enabled: true}}
	a.state.DomainHealth[host] = DomainHealth{FailureStreak: 30}
	calls := 0
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected RPC", 500) }))
	defer rpc.Close()
	cfg.Tracker.Transmission.Enabled = true
	cfg.Tracker.Transmission.URL = rpc.URL
	cfg.Tracker.AutoDiscover = true
	if samples := a.loadSamples(context.Background(), cfg); len(samples) != 0 {
		t.Fatal(samples)
	}
	samples := map[string]TrackerSample{host: {Domain: host}}
	if failures := a.evaluateTransmissionTrackerKeepalive(context.Background(), cfg, samples); len(failures) != 0 {
		t.Fatal(failures)
	}
	a.reconcileTransmissionTrackerKeepaliveAfterRepair(context.Background(), cfg, samples, map[string]string{}, map[string]string{host: "104.16.0.1"})
	if calls != 0 {
		t.Fatalf("paused Tracker made %d RPC calls", calls)
	}
}

func TestMaintenanceResumeAndPersistenceRollback(t *testing.T) {
	for _, diskFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "disk-failure"}[diskFailure], func(t *testing.T) {
			a, cfg := newRegressionApp(t)
			const host = "offline.example"
			cfg.Domains = []Domain{{Host: host, Class: "latency", Mode: "http", Enabled: true}}
			a.config = cfg
			a.state.DomainHealth[host] = DomainHealth{FailureStreak: 131, LastFailure: "2026-10-21T23:26:14+08:00"}
			a.state.DomainStatus[host] = "paused"
			a.state.TrackerKeepalive[host] = TrackerKeepaliveRuntime{Status: "paused", Attempts: 3}
			if diskFailure {
				if err := os.Mkdir(filepath.Join(a.dataDir, "state.json"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/domain-resume", strings.NewReader(`{"host":"offline.example"}`)))
			if diskFailure {
				if w.Code != 500 || a.state.DomainHealth[host].FailureStreak != 131 || a.state.TrackerKeepalive[host].Attempts != 3 {
					t.Fatalf("rollback failed: %d %+v", w.Code, a.state)
				}
			} else {
				if w.Code != 200 || a.state.DomainHealth[host].FailureStreak != 0 {
					t.Fatalf("resume failed: %d %s", w.Code, w.Body)
				}
				saved, _ := newRegressionApp(t)
				saved.dataDir = a.dataDir
				saved.loadState()
				if saved.state.DomainHealth[host].FailureStreak != 0 {
					t.Fatal("resume not persisted")
				}
				if _, ok := a.state.TrackerKeepalive[host]; ok {
					t.Fatal("old retry cycle retained")
				}
			}
			if a.state.Running {
				t.Fatal("job slot not released")
			}
		})
	}
}

func TestPauseStatusExcludesDisabledAndFollowDomains(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.Domains = []Domain{
		{Host: "offline.example", Enabled: true},
		{Host: "disabled.example", Enabled: false},
		{Host: "follow.example", Enabled: true, Class: "follow", Follow: "offline.example"},
	}
	a.config = cfg
	for _, d := range cfg.Domains {
		a.state.DomainHealth[d.Host] = DomainHealth{FailureStreak: 131}
	}
	w := httptest.NewRecorder()
	a.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var body struct {
		MaintenancePaused []MaintenancePause `json:"maintenancePaused"`
		Limit             int                `json:"maintenanceFailureLimit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Limit != 30 || len(body.MaintenancePaused) != 1 || body.MaintenancePaused[0].Host != "offline.example" {
		t.Fatalf("bad status: %+v", body)
	}
}
