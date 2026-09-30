package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newRegressionApp(t *testing.T) (*App, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Tracker.AutoDiscover = false
	cfg.Tracker.SamplesPath = filepath.Join(dir, "samples.tsv")
	cfg.Tracker.AutoSamplesPath = filepath.Join(dir, "auto-samples.tsv")
	cfg.HostsPath = filepath.Join(dir, "hosts")
	return &App{
		config:  cfg,
		dataDir: dir,
		state: RuntimeState{
			Mappings: map[string]string{}, DomainStatus: map[string]string{},
			DomainHealth: map[string]DomainHealth{}, HTTPProbe: map[string]HTTPProbeRuntime{},
			TrackerSamples: map[string]TrackerSampleRuntime{}, TrackerKeepalive: map[string]TrackerKeepaliveRuntime{},
		},
	}, cfg
}

func TestPersistStateWhileProbesAndLogsChange(t *testing.T) {
	a, cfg := newRegressionApp(t)
	httpDomain := Domain{Host: "origin.example", Mode: "http"}
	trackerDomain := Domain{Host: "tracker.example", Mode: "tracker", Class: "latency"}
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			a.recordHTTPProbe(httpDomain, true, "HTTP 200")
			a.recordTrackerSampleTest(cfg, trackerDomain, true, "announce accepted")
			a.appendLog("probe %d", i)
		}
	}()
	for worker := 0; worker < 2; worker++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := a.persistStateStrict(); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if err := a.persistStateStrict(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(a.dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state RuntimeState
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if !state.HTTPProbe[httpDomain.Host].Reachable || !state.TrackerSamples[trackerDomain.Host].Passed || len(state.Logs) != 200 {
		t.Fatalf("probe updates were lost: %#v", state)
	}
	if state.Running || state.CurrentJob != "" {
		t.Fatal("persisted transient job state")
	}
}

func TestConcurrentJSONWritesRemainAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	type record struct {
		Writer int
		Body   string
	}
	const writers = 16
	var wg sync.WaitGroup
	errors := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := writeJSON(path, record{i, strings.Repeat(fmt.Sprint(i)+"/", 2000)}); err != nil {
					errors <- err
					return
				}
				b, err := os.ReadFile(path)
				if err != nil {
					errors <- err
					return
				}
				var got record
				if err := json.Unmarshal(b, &got); err != nil {
					errors <- err
					return
				}
				if got.Body != strings.Repeat(fmt.Sprint(got.Writer)+"/", 2000) {
					errors <- fmt.Errorf("partially written record from writer %d", got.Writer)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name() != "state.json" {
		t.Fatalf("temporary files leaked: %v", files)
	}
}

func TestConcurrentConfigSavesMatchDisk(t *testing.T) {
	a, cfg := newRegressionApp(t)
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	routes := a.routes()
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := cfg
			next.RepairIntervalMinutes = i + 1
			body, err := json.Marshal(next)
			if err != nil {
				errors <- err
				return
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body)))
			if rec.Code != http.StatusOK {
				errors <- fmt.Errorf("save returned %d: %s", rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	disk, err := loadConfig(a.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.RepairIntervalMinutes != a.snapshotConfig().RepairIntervalMinutes {
		t.Fatalf("disk/live config differ: %d / %d", disk.RepairIntervalMinutes, a.snapshotConfig().RepairIntervalMinutes)
	}
}

func TestTrackerTransportErrorDoesNotDisplayCredentials(t *testing.T) {
	a, cfg := newRegressionApp(t)
	d := Domain{Host: "tracker.example", Mode: "tracker", Class: "latency"}
	sample := TrackerSample{Domain: d.Host, Hash: make([]byte, 20), URL: "https://tracker.example/secret-in-path/announce?passkey=secret-in-query"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, detail := a.verifyDomain(ctx, d, "127.0.0.1", cfg, map[string]TrackerSample{d.Host: sample})
	if ok || !strings.Contains(detail, "context canceled") {
		t.Fatalf("unexpected result: %v %q", ok, detail)
	}
	if err := a.persistStateStrict(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(a.dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-in-path", "secret-in-query"} {
		if strings.Contains(detail, secret) || bytes.Contains(b, []byte(secret)) {
			t.Fatalf("credential displayed: %s", secret)
		}
	}
}

func TestTrackerFailureRedactsSecretsContainingS(t *testing.T) {
	for _, key := range []string{"passkey", "credential", "torrent_pass", "authkey"} {
		reason := "invalid " + key + "=secret-value for announce"
		verdict := evaluateTrackerResponse([]byte(fmt.Sprintf("d14:failure reason%d:%se", len(reason), reason)))
		if strings.Contains(verdict.Detail, "secret-value") || !strings.Contains(verdict.Detail, key+"=*** for announce") {
			t.Fatalf("bad redaction: %q", verdict.Detail)
		}
	}
}

func TestDomainHostsRejectInvalidNames(t *testing.T) {
	for _, host := range []string{"good.example\n127.0.0.1 injected.example", "good.example alias.example", "good.example\talias.example", "good.example#comment", "bad..example", "-bad.example", "bad-.example", strings.Repeat("x", 64) + ".example"} {
		t.Run(host, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Domains = []Domain{{Host: host, Class: "normal", Enabled: true}}
			if err := normalizeConfig(&cfg); err == nil {
				t.Fatal("invalid name accepted by config")
			}
			if _, err := renderHosts("127.0.0.1 localhost\n", map[string]string{host: "104.16.0.1"}); err == nil {
				t.Fatal("invalid name accepted by Hosts renderer")
			}
		})
	}
	for _, host := range []string{"localhost", "nas-1.local", "A.example", "example.com.", "xn--fiqs8s.example"} {
		cfg := defaultConfig()
		cfg.Domains = []Domain{{Host: host, Class: "normal", Enabled: true}}
		if err := normalizeConfig(&cfg); err != nil {
			t.Fatalf("valid name %q rejected: %v", host, err)
		}
	}
}

func TestMaintenanceSynchronizesOnlyDirectFollowers(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoApply = true
	cfg.Domains = []Domain{
		{Host: "origin.example", Class: "normal", Mode: "http", Enabled: true},
		{Host: "alias.example", Follow: "origin.example", Class: "follow", Mode: "http", Enabled: true},
		{Host: "other.example", Class: "normal", Mode: "http", Enabled: true},
		{Host: "other-alias.example", Follow: "other.example", Class: "follow", Mode: "http", Enabled: true},
	}
	a.state.Mappings = map[string]string{"origin.example": "104.16.0.1", "alias.example": "104.16.0.1", "other.example": "104.16.0.9", "other-alias.example": "104.16.0.9"}
	a.state.Candidates = []Candidate{{IP: "104.16.0.2", DelayMS: 1, ObservedAt: time.Now().Format(time.RFC3339)}}
	if err := os.WriteFile(cfg.HostsPath, []byte("127.0.0.1 localhost\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.runDomainMaintenance(context.Background(), cfg, "origin.example"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"origin.example", "alias.example"} {
		if a.state.Mappings[host] != "104.16.0.2" {
			t.Fatalf("mapping not propagated for %s", host)
		}
	}
	for _, host := range []string{"other.example", "other-alias.example"} {
		if a.state.Mappings[host] != "104.16.0.9" {
			t.Fatalf("unrelated mapping changed for %s", host)
		}
	}
	if a.state.DomainHealth["alias.example"] != a.state.DomainHealth["origin.example"] {
		t.Fatal("follower health not propagated")
	}
	contents, err := os.ReadFile(cfg.HostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(contents, []byte("104.16.0.2 alias.example")) {
		t.Fatal("applied Hosts still has old follower IP")
	}
}

func TestFailedMaintenanceDropsDependentFollowMapping(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.Domains = []Domain{
		{Host: "origin.example", Class: "latency", Mode: "http", Endpoint: "/%", Enabled: true},
		{Host: "alias.example", Follow: "origin.example", Class: "follow", Mode: "http", Enabled: true},
	}
	a.state.Mappings = map[string]string{"origin.example": "104.16.0.1", "alias.example": "104.16.0.1"}
	a.cfstBin = filepath.Join(a.dataDir, "cfst")
	if err := os.WriteFile(a.cfstBin, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := a.runDomainMaintenance(context.Background(), cfg, "origin.example"); err == nil {
		t.Fatal("expected failed maintenance")
	}
	if a.state.Mappings["origin.example"] != "" || a.state.Mappings["alias.example"] != "" {
		t.Fatal("unresolved target or follower retained")
	}
	if !strings.Contains(a.state.DomainStatus["alias.example"], "target has no mapping") {
		t.Fatal("unresolved follower status missing")
	}
}

func TestTrackerMaintenancePreservesOtherKeepalive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrents":[]}}`))
	}))
	defer srv.Close()
	a, cfg := newRegressionApp(t)
	cfg.Tracker.Transmission = DownloaderClientConfig{Enabled: true, URL: srv.URL}
	cfg.Domains = []Domain{{Host: "a.example", Class: "latency", Mode: "tracker", Enabled: true}, {Host: "b.example", Class: "latency", Mode: "tracker", Enabled: true}}
	other := TrackerKeepaliveRuntime{Attempts: 3, RejectedIP: "104.16.0.1", NextCheck: time.Now().Add(time.Minute).Format(time.RFC3339)}
	a.state.TrackerKeepalive["b.example"] = other
	if err := os.WriteFile(cfg.Tracker.SamplesPath, []byte("a.example\t/announce\t0123456789abcdef0123456789abcdef01234567\thttps://a.example/announce?passkey=fake\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.cfstBin = filepath.Join(a.dataDir, "cfst")
	if err := os.WriteFile(a.cfstBin, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := a.runDomainMaintenance(context.Background(), cfg, "a.example"); err == nil {
		t.Fatal("expected failed CFST")
	}
	if a.state.TrackerKeepalive["b.example"] != other {
		t.Fatal("other Tracker's retry state changed")
	}
}
