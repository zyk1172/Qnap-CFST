package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostsBindingDetectsReplacementBeforeWriting(t *testing.T) {
	a, cfg := newRegressionApp(t)
	actual := filepath.Join(a.dataDir, "host-etc", "hosts")
	if err := os.MkdirAll(filepath.Dir(actual), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, []byte("127.0.0.1 localhost\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(a.dataDir, "bound-hosts")
	if err := os.Link(actual, bound); err != nil {
		t.Fatal(err)
	}
	cfg.HostsPath = bound
	t.Setenv("HOSTS_VERIFY_PATH", actual)
	if status, err := hostsBindingStatus(bound); err != nil || status != "verified" {
		t.Fatalf("initial: %s %v", status, err)
	}
	replacement := actual + ".new"
	if err := os.WriteFile(replacement, []byte("127.0.0.1 new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, actual); err != nil {
		t.Fatal(err)
	}
	_, err := a.stageApplyMappings(cfg, map[string]string{"example.com": "104.16.0.1"})
	if err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("expected divergence: %v", err)
	}
	old, _ := os.ReadFile(bound)
	newFile, _ := os.ReadFile(actual)
	if string(old) != "127.0.0.1 localhost\n" || string(newFile) != "127.0.0.1 new\n" {
		t.Fatal("diverged files must be untouched")
	}
	cfg.AutoApply = true
	a.config = cfg
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("diverged binding must fail health check: %d", w.Code)
	}
}

func TestDefaultHostBindingRequiresIndependentVerification(t *testing.T) {
	t.Setenv("HOSTS_VERIFY_PATH", "")
	if status, err := hostsBindingStatus("/host/etc/hosts"); status != "unverified" || err == nil {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestLatencyCandidatesRequireDomainVerification(t *testing.T) {
	cfg := defaultConfig()
	c := Candidate{IP: "104.16.0.2", LatencyOnly: true}
	for _, class := range []string{"normal", "bandwidth"} {
		if candidateEligible(c, Domain{Class: class}, cfg) {
			t.Fatalf("%s selected unmeasured candidate", class)
		}
	}
	if !candidateEligible(c, Domain{Class: "latency"}, cfg) {
		t.Fatal("latency candidate should be eligible for domain verification")
	}
}

func TestCFSTSupplementsSmallPoolWithLatencyOnly(t *testing.T) {
	a, cfg := newRegressionApp(t)
	a.cfstBin = filepath.Join(a.dataDir, "fake-cfst")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in
  -o) output="$2"; shift 2 ;;
  -latency-output) latency="$2"; shift 2 ;;
  *) shift ;;
 esac
done
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.1,4,4,0,30,25,SIN\n' > "$output"
printf 'ip,sent,received,loss,delay,speed,colo\n104.16.0.1,4,4,0,30,0,SIN\n104.16.0.2,4,4,0,35,0,SIN\n' > "$latency"
`
	if err := os.WriteFile(a.cfstBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := a.runCFST(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].LatencyOnly || !got[1].LatencyOnly || got[1].SpeedMB != 0 {
		t.Fatalf("bad candidate merge: %+v", got)
	}
}

func TestTrackerServerErrorsAreNotConnected(t *testing.T) {
	for _, reason := range []string{"Tracker HTTP response 502 (Bad Gateway)", "Tracker HTTP response 521 (Unknown Error)", "HTTP 503"} {
		stat := transmissionTrackerStat{HasAnnounced: true, LastAnnounceResult: reason}
		if label := transmissionTrackerStatusLabel(stat); label != "ServerError" {
			t.Fatalf("%q: %s", reason, label)
		}
		rows := []transmissionTrackerStatsRow{{ID: 1, Status: 6, TrackerStats: []transmissionTrackerStat{{Host: "tracker.example", HasAnnounced: true, LastAnnounceResult: reason}}}}
		health := transmissionDomainHealthForTarget(rows, "tracker.example")
		if health.Connected != 0 || health.ServerErrors != 1 || health.ConnectionFailures != 0 {
			t.Fatalf("server error treated as healthy/connection error: %+v", health)
		}
	}
}

func TestAssetCachePolicyAvoidsMixedVersions(t *testing.T) {
	a, _ := newRegressionApp(t)
	for _, path := range []string{"/js/core.js?v=safety-2", "/css/app.css?v=safety-2"} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d cache=%q", path, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

func TestUnresolvedHistoryIncludesRetainedFailure(t *testing.T) {
	cfg := defaultConfig()
	cfg.Domains = []Domain{{Host: "bad.example", Enabled: true}, {Host: "healthy.example", Enabled: true}}
	mappings := map[string]string{"bad.example": "104.16.0.1", "healthy.example": "104.16.0.2"}
	names, total := unresolvedDomainsWithStatus(cfg, mappings, map[string]string{"bad.example": "stale retained · HTTP 403"})
	if total != 1 || len(names) != 1 || names[0] != "bad.example" {
		t.Fatalf("unexpected unresolved: %v %d", names, total)
	}
}
