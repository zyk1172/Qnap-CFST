package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSyncPayloadUsesClassNotSiteGroup(t *testing.T) {
	a := &App{
		config: defaultConfig(),
		state: RuntimeState{
			Mappings: map[string]string{"tracker.example.com": "104.16.0.1"},
			DomainStatus: map[string]string{"tracker.example.com": "retained · 104.16.0.1 · announce response"},
			DomainHealth: map[string]DomainHealth{"tracker.example.com": {LastSuccess: "2026-09-22T01:00:00+08:00"}},
			Candidates: []Candidate{{IP: "104.16.0.1", LossRate: 0, DelayMS: 20, SpeedMB: 10, Colo: "HKG"}},
		},
	}
	cfg := defaultConfig()
	cfg.Domains = []Domain{{Host: "tracker.example.com", Group: "mteam", Class: "latency", Mode: "tracker", Enabled: true}}
	cfg.Sync.Repository = "owner/repo"
	cfg.Sync.Branch = "main"
	payload, err := a.buildSyncPayload(cfg, time.Date(2026, 9, 22, 1, 1, 0, 0, time.FixedZone("UTC+8", 8*3600)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.MapText, "tracker.example.com\t104.16.0.1\tlatency\t") {
		t.Fatalf("sync class missing: %s", payload.MapText)
	}
	if strings.Contains(payload.MapText, "\tmteam\t") {
		t.Fatalf("site group leaked into compatibility group column: %s", payload.MapText)
	}
}

func TestGitHubAtomicPublishUsesOneRefUpdate(t *testing.T) {
	refUpdates := 0
	commits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/main"):
			_, _ = io.WriteString(w, `{"object":{"sha":"oldcommit"}}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/oldcommit"):
			_, _ = io.WriteString(w, `{"tree":{"sha":"oldtree"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/blobs"):
			_, _ = io.WriteString(w, `{"sha":"blobsha"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			_, _ = io.WriteString(w, `{"sha":"newtree"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			commits++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			parents, _ := body["parents"].([]any)
			if len(parents) != 1 || parents[0] != "oldcommit" {
				t.Fatalf("unexpected parents: %#v", body["parents"])
			}
			_, _ = io.WriteString(w, `{"sha":"newcommit"}`)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/git/refs/heads/main"):
			refUpdates++
			_, _ = io.WriteString(w, `{"ref":"refs/heads/main"}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := &githubClient{baseURL: server.URL, token: "test", http: server.Client()}
	sha, err := client.publishAtomic(context.Background(), "owner/repo", "main", "test", "map", "status")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "newcommit" || commits != 1 || refUpdates != 1 {
		t.Fatalf("atomic publish mismatch: sha=%s commits=%d refUpdates=%d", sha, commits, refUpdates)
	}
}


func TestSyncPayloadPreservesNormalStrategy(t *testing.T) {
	a:=&App{
		config:defaultConfig(),
		state:RuntimeState{
			Mappings:map[string]string{"plain.example.com":"104.16.0.9"},
			DomainStatus:map[string]string{"plain.example.com":"normal · lowest latency · 104.16.0.9 · 11.00 ms · verification skipped"},
			DomainHealth:map[string]DomainHealth{"plain.example.com":{LastSuccess:"2026-09-22T01:00:00+08:00"}},
			Candidates:[]Candidate{{IP:"104.16.0.9",LossRate:0.1,DelayMS:11,SpeedMB:6,Colo:"HKG"}},
		},
	}
	cfg:=defaultConfig()
	cfg.Domains=[]Domain{{Host:"plain.example.com",Class:"normal",Mode:"tracker",Enabled:true}}
	cfg.Sync.Repository="owner/repo"
	cfg.Sync.Branch="main"
	payload,err:=a.buildSyncPayload(cfg,time.Date(2026,9,22,1,1,0,0,time.FixedZone("UTC+8",8*3600)))
	if err!=nil{t.Fatal(err)}
	if !strings.Contains(payload.MapText,"plain.example.com\t104.16.0.9\tnormal\t11\t6\t0.1\tHKG\t") {
		t.Fatalf("normal sync class missing: %s",payload.MapText)
	}
	if !strings.Contains(payload.MapText,"\t-\tSELECTED\n") {
		t.Fatalf("normal record must be marked SELECTED without fake HTTP code: %s",payload.MapText)
	}
	var status map[string]any
	if err:=json.Unmarshal([]byte(payload.StatusText),&status);err!=nil{t.Fatal(err)}
	if int(status["schema"].(float64))!=3 || int(status["normal_count"].(float64))!=1 {
		t.Fatalf("unexpected schema-3 normal status: %#v",status)
	}
}
