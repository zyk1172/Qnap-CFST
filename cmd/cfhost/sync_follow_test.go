package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSyncFollowMappingsDoNotClaimIndependentVerification(t *testing.T) {
	for _, targetClass := range []string{"normal", "latency"} {
		for _, mode := range []string{"http", "tracker"} {
			t.Run(targetClass+"/"+mode, func(t *testing.T) {
				a, cfg := newRegressionApp(t)
				cfg.Domains = []Domain{
					{Host: "origin.example", Class: targetClass, Mode: "http", Enabled: true},
					{Host: "alias.example", Follow: "origin.example", Class: "follow", Mode: mode, Enabled: true},
				}
				a.state.Mappings["origin.example"] = "104.16.0.1"
				a.state.DomainHealth["origin.example"] = DomainHealth{LastSuccess: "2026-09-30T00:00:00Z"}
				applyFollowMappings(cfg, a.state.Mappings, a.state.DomainStatus, a.state.DomainHealth)
				payload, err := a.buildSyncPayload(cfg, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range strings.Split(payload.MapText, "\n") {
					fields := strings.Split(row, "\t")
					if fields[0] != "alias.example" {
						continue
					}
					found = true
					if len(fields) != 11 || fields[1] != "104.16.0.1" || fields[2] != "follow" || fields[7] != "-" || fields[8] != "-" || fields[9] != "FOLLOWED" || fields[10] != "origin.example" {
						t.Fatalf("incorrect follow record: %q", row)
					}
				}
				if !found {
					t.Fatal("follow record missing")
				}
				var status map[string]any
				if err := json.Unmarshal([]byte(payload.StatusText), &status); err != nil {
					t.Fatal(err)
				}
				if status["schema"] != float64(4) || status["follow_count"] != float64(1) || status["domain_count"] != float64(2) {
					t.Fatalf("incorrect counts: %#v", status)
				}
				expectedLatency := float64(0)
				if targetClass == "latency" {
					expectedLatency = 1
				}
				if status["latency_count"] != expectedLatency {
					t.Fatalf("follower counted as latency: %#v", status)
				}
			})
		}
	}
}

func TestSyncSignatureTracksFollowTargetWithSameIP(t *testing.T) {
	a, cfg := newRegressionApp(t)
	cfg.Domains = []Domain{
		{Host: "origin.example", Class: "normal", Mode: "http", Enabled: true},
		{Host: "other.example", Class: "normal", Mode: "http", Enabled: true},
		{Host: "alias.example", Follow: "origin.example", Class: "follow", Mode: "http", Enabled: true},
	}
	a.state.Mappings = map[string]string{"origin.example": "104.16.0.1", "other.example": "104.16.0.1", "alias.example": "104.16.0.1"}
	before, err := a.buildSyncPayload(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Domains[2].Follow = "other.example"
	after, err := a.buildSyncPayload(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if before.Signature == after.Signature {
		t.Fatal("changed follow target would skip publication")
	}
}
