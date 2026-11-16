package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClearCFHostBlocksPreservesAllOtherBytes(t *testing.T) {
	prefix := "# 原有内容\r\n127.0.0.1 localhost\r\n\r\n# CF-YX-LATENCY-BEGIN\r\n1.2.3.4 legacy.example\r\n# CF-YX-LATENCY-END\r\n"
	suffix := "\r\n# other manager\n5.6.7.8 original.example"
	block := hostsBegin + "\r\n104.16.0.1 managed.example\r\n# comment\r\n" + hostsEnd + "\r\n"
	got, count, err := clearCFHostBlocks(prefix + block + block + suffix)
	if err != nil || count != 2 || got != prefix+suffix {
		t.Fatalf("clear changed unowned bytes: count=%d err=%v got=%q", count, err, got)
	}
	again, count, err := clearCFHostBlocks(got)
	if err != nil || count != 0 || again != got {
		t.Fatal("clear is not idempotent")
	}
	for _, broken := range []string{hostsBegin + "\n1.2.3.4 x\n", hostsEnd + "\n", hostsBegin + "\n" + hostsBegin + "\n" + hostsEnd} {
		if _, _, err := clearCFHostBlocks(broken); err == nil {
			t.Fatal("ambiguous markers must not cause deletion")
		}
	}
}

func TestClearSharedHostsDisablesAutoApplyAndSurvivesRepairAndRestart(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.AutoApply, cfg.AutoRepair = true, true
	cfg.Domains = []Domain{{Host: "managed.example", Enabled: true, Class: "normal", Mode: "http"}}
	a.config = cfg
	a.state.Mappings = map[string]string{"managed.example": "104.16.0.1"}
	a.state.Candidates = []Candidate{{IP: "104.16.0.1", SpeedMB: 20, ObservedAt: time.Now().Format(time.RFC3339)}}
	a.state.LastRefresh = time.Now().Format(time.RFC3339)
	original := "# original\r\n127.0.0.1 localhost\r\n" + hostsBegin + "\n104.16.0.1 managed.example\n" + hostsEnd + "\n# keep without newline"
	want := "# original\r\n127.0.0.1 localhost\r\n# keep without newline"
	if err := os.WriteFile(cfg.HostsPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	consumer := filepath.Join(a.dataDir, "consumer-hosts")
	if err := os.Link(cfg.HostsPath, consumer); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOSTS_VERIFY_PATH", consumer)
	before, _ := os.Stat(cfg.HostsPath)
	oldCandidates := append([]Candidate(nil), a.state.Candidates...)
	oldRefresh := a.state.LastRefresh
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/hosts/clear", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []string{cfg.HostsPath, consumer} {
		got, _ := os.ReadFile(p)
		after, _ := os.Stat(p)
		if string(got) != want || !os.SameFile(before, after) {
			t.Fatal("clear changed file identity or other bytes")
		}
	}
	if a.snapshotConfig().AutoApply || a.state.HostsClearedAt == "" || a.state.Running {
		t.Fatal("clear did not persist switch or release slot")
	}
	if !reflect.DeepEqual(oldCandidates, a.state.Candidates) || a.state.LastRefresh != oldRefresh || a.state.Mappings["managed.example"] != "104.16.0.1" {
		t.Fatal("clear erased recoverable configuration/evidence")
	}
	loaded, err := loadConfig(a.dataDir)
	if err != nil || loaded.AutoApply || !loaded.AutoRepair {
		t.Fatal("restart did not retain disabled automatic apply")
	}
	if err := a.runSmartRepair(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg.HostsPath)
	if string(got) != want {
		t.Fatal("background repair repopulated cleared rules")
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/apply", nil))
	got, _ = os.ReadFile(cfg.HostsPath)
	if w.Code != 200 || !strings.Contains(string(got), hostsBegin) || a.state.HostsClearedAt != "" {
		t.Fatal("explicit apply could not restore retained mappings")
	}
}

func TestClearRefusesBusyAndRollsBackOnPersistenceFailure(t *testing.T) {
	for _, failure := range []string{"busy", "config", "state", "markers", "binding"} {
		t.Run(failure, func(t *testing.T) {
			a, cfg := newRegressionApp(t)
			cfg.AutoApply = true
			a.config = cfg
			content := "127.0.0.1 localhost\n" + hostsBegin + "\n104.16.0.1 managed.example\n" + hostsEnd + "\n"
			if failure == "markers" {
				content += hostsEnd + "\n"
			}
			if err := os.WriteFile(cfg.HostsPath, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOSTS_VERIFY_PATH", cfg.HostsPath)
			switch failure {
			case "busy":
				a.state.Running = true
			case "config", "state":
				if err := os.Mkdir(filepath.Join(a.dataDir, failure+".json"), 0700); err != nil {
					t.Fatal(err)
				}
			case "binding":
				actual := filepath.Join(a.dataDir, "current-hosts")
				os.WriteFile(actual, []byte("127.0.0.1 other\n"), 0600)
				t.Setenv("HOSTS_VERIFY_PATH", actual)
			}
			if _, err := a.clearManagedHosts(); err == nil {
				t.Fatal("expected clear to be refused/rolled back")
			}
			got, _ := os.ReadFile(cfg.HostsPath)
			if string(got) != content || !a.snapshotConfig().AutoApply || a.state.HostsClearedAt != "" {
				t.Fatal("failed clear changed Hosts or original settings")
			}
			if failure == "busy" {
				w := httptest.NewRecorder()
				a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/apply", nil))
				if w.Code != 409 {
					t.Fatal("apply bypassed operation exclusion")
				}
			}
		})
	}
}
