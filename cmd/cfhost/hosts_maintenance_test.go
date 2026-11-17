package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func replacedHostsApp(t *testing.T) (*App, Config, string) {
	t.Helper()
	a, cfg := newRegressionApp(t)
	actual := filepath.Join(a.dataDir, "current-hosts")
	if err := os.WriteFile(actual, []byte("127.0.0.1 localhost\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(actual, cfg.HostsPath); err != nil {
		t.Fatal(err)
	}
	replacement := actual + ".new"
	if err := os.WriteFile(replacement, []byte("127.0.0.1 replaced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, actual); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOSTS_VERIFY_PATH", actual)
	cfg.AutoApply, cfg.AutoRepair = true, true
	cfg.Domains = []Domain{{Host: "tracker.example", Enabled: true, Class: "latency", Mode: "tracker"}}
	a.config = cfg
	return a, cfg, actual
}

func refreshTestHostsBinding(t *testing.T, bound, actual string) {
	t.Helper()
	if err := os.Remove(bound); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(actual, bound); err != nil {
		t.Fatal(err)
	}
}

func TestBrokenHostsStopsResolutionBeforeDownloaderOrCFST(t *testing.T) {
	a, cfg, _ := replacedHostsApp(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer srv.Close()
	cfg.Tracker.AutoDiscover = true
	cfg.Tracker.Transmission = DownloaderClientConfig{Enabled: true, URL: srv.URL}
	a.cfstBin = filepath.Join(a.dataDir, "must-not-run")
	if err := os.WriteFile(a.cfstBin, []byte("#!/bin/sh\necho called > cfst-called\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := a.snapshotResolution()
	for _, run := range []func() error{
		func() error { return a.runSmartRepair(context.Background(), cfg) },
		func() error { return a.runFullOptimize(context.Background(), cfg) },
		func() error { return a.runDomainMaintenance(context.Background(), cfg, "tracker.example") },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "maintenance paused") {
			t.Fatalf("preflight error: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("blocked maintenance contacted downloader %d times", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "cfst-called")); !os.IsNotExist(err) {
		t.Fatal("blocked maintenance launched CFST")
	}
	if !reflect.DeepEqual(before, a.snapshotResolution()) || a.state.LastOptimizeAttempt != "" {
		t.Fatal("preflight changed resolution or consumed optimize attempt")
	}
	if !a.state.HostsMaintenance.Blocked {
		t.Fatal("missing infrastructure pause")
	}
}

func TestHostsPauseBackoffPersistsAndRecoversWithoutResettingBudgets(t *testing.T) {
	a, cfg, actual := replacedHostsApp(t)
	now := time.Now().Truncate(time.Second)
	a.state.LastRefresh = now.Add(-time.Minute).Format(time.RFC3339)
	a.state.CFSTSchedule.NextAllowedAt = now.Add(time.Hour).Format(time.RFC3339)
	a.state.TrackerKeepalive["tracker.example"] = TrackerKeepaliveRuntime{NextCheck: now.Add(-time.Minute).Format(time.RFC3339)}
	for _, delay := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if got := a.scheduledJob(now); got != "" {
			t.Fatalf("blocked scheduler launched %s", got)
		}
		next, err := time.Parse(time.RFC3339, a.state.HostsMaintenance.NextCheck)
		if err != nil || next.Sub(now) != delay {
			t.Fatalf("backoff %v: %+v", delay, a.state.HostsMaintenance)
		}
		before := a.state.HostsMaintenance
		if got := a.scheduledJob(now.Add(time.Second)); got != "" || a.state.HostsMaintenance != before {
			t.Fatal("scheduler repeated a local check before its deadline")
		}
		now = next
	}
	b := &App{dataDir: a.dataDir, config: cfg}
	b.loadState()
	if b.state.HostsMaintenance != a.state.HostsMaintenance {
		t.Fatal("restart lost Hosts pause")
	}
	budget := b.state.CFSTSchedule
	lastRefresh := b.state.LastRefresh
	refreshTestHostsBinding(t, cfg.HostsPath, actual)
	if err := b.preflightMaintenanceHosts(cfg, now.Add(-time.Second), true); err != nil {
		t.Fatal(err)
	}
	if b.state.HostsMaintenance.Blocked || b.state.CFSTSchedule != budget || b.state.LastRefresh != lastRefresh {
		t.Fatal("recovery changed scan admission or remained blocked")
	}
	if got := b.scheduledJob(now); got != "repair" {
		t.Fatalf("due keepalive did not resume after recovery: %s", got)
	}
}

func TestApplyFailureKeepsFailedObservationsAndRejectsUnappliedSuccess(t *testing.T) {
	a, cfg, actual := replacedHostsApp(t)
	cfg.Domains = append(cfg.Domains, Domain{Host: "new.example", Enabled: true, Class: "latency", Mode: "http"})
	a.state.Mappings = map[string]string{"tracker.example": "104.16.0.1", "new.example": "104.16.0.3"}
	a.state.DomainHealth["new.example"] = DomainHealth{FailureStreak: 3, LastFailure: "2026-11-14T03:00:00Z"}
	keepalive := TrackerKeepaliveRuntime{RejectedIP: "104.16.0.3", Attempts: 3, Status: "failed"}
	a.state.TrackerKeepalive["new.example"] = keepalive
	at := time.Now().Format(time.RFC3339)
	health := map[string]DomainHealth{
		"tracker.example": {FailureStreak: 1, LastFailure: at, NextCheck: time.Now().Add(time.Hour).Format(time.RFC3339)},
		"new.example":     {LastSuccess: at},
	}
	mappings := map[string]string{"tracker.example": "104.16.0.1", "new.example": "104.16.0.4"}
	statuses := map[string]string{"tracker.example": "unresolved", "new.example": "candidate verified"}
	if err := a.commitResolution(context.Background(), cfg, mappings, statuses, health, at, false); err == nil {
		t.Fatal("expected late binding divergence")
	}
	if a.state.DomainHealth["tracker.example"] != health["tracker.example"] {
		t.Fatal("apply failure discarded failed observation or retry deadline")
	}
	if a.state.Mappings["new.example"] != "104.16.0.3" || a.state.DomainHealth["new.example"].FailureStreak != 3 || a.state.TrackerKeepalive["new.example"] != keepalive {
		t.Fatal("unapplied success changed live mapping health or rejection")
	}
	if a.state.ResolutionAttempt.Applied || a.state.ResolutionAttempt.DomainStatus["new.example"] != "candidate verified" || !strings.Contains(a.state.DomainStatus["new.example"], "unresolved") {
		t.Fatal("unapplied observations were lost or presented as healthy")
	}
	b := &App{dataDir: a.dataDir, config: cfg}
	b.loadState()
	if b.state.DomainHealth["tracker.example"].FailureStreak != 1 || !b.state.HostsMaintenance.Blocked {
		t.Fatal("failed observation/pause not durable")
	}
	refreshTestHostsBinding(t, cfg.HostsPath, actual)
	if err := b.preflightMaintenanceHosts(cfg, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	health["tracker.example"] = DomainHealth{FailureStreak: 2, LastFailure: at}
	before, _ := os.Stat(actual)
	if err := b.commitResolution(context.Background(), cfg, mappings, statuses, health, at, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(actual)
	if !os.SameFile(before, after) || !b.state.ResolutionAttempt.Applied {
		t.Fatal("recovery replaced the single Hosts file or did not commit")
	}
	if b.state.DomainHealth["tracker.example"].FailureStreak != 2 {
		t.Fatal("failure streak restarted at one")
	}
}

func TestHostsPreflightChecksMarkersWithoutChangingSingleFile(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoApply = true
	t.Setenv("HOSTS_VERIFY_PATH", cfg.HostsPath)
	content := "127.0.0.1 localhost\n" + hostsBegin + "\n"
	if err := os.WriteFile(cfg.HostsPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.preflightMaintenanceHosts(cfg, time.Now(), true); err == nil {
		t.Fatal("invalid markers did not pause maintenance")
	}
	got, _ := os.ReadFile(cfg.HostsPath)
	if string(got) != content {
		t.Fatal("preflight changed Hosts")
	}
	a.config = cfg
	w := httptest.NewRecorder()
	a.handleHealth(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("paused maintenance reported healthy")
	}
	if err := os.WriteFile(cfg.HostsPath, []byte("127.0.0.1 localhost\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.preflightMaintenanceHosts(cfg, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.handleHealth(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatal("recovered target did not report healthy")
	}
	cfg.AutoApply, cfg.Sync.Enabled = false, false
	if err := a.preflightMaintenanceHosts(cfg, time.Now(), true); err != nil || a.state.HostsMaintenance.Blocked {
		t.Fatal("disabled Hosts integration kept unrelated maintenance blocked")
	}
}
