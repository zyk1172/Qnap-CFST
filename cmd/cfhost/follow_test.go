package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizeFollowStrategy(t *testing.T) {
	cfg := defaultConfig()
	cfg.Domains = []Domain{
		{Host: "Origin.Example.com", Class: "latency", Mode: "http", Enabled: true},
		{Host: "Alias.Example.com", Follow: "ORIGIN.EXAMPLE.COM", Class: "follow", Mode: "tracker", Enabled: true},
		{Host: "Plain.Example.com", Follow: "origin.example.com", Class: "normal", Mode: "http", Enabled: true},
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Domains[1].Follow; got != "origin.example.com" {
		t.Fatalf("follow target not normalized: %q", got)
	}
	if got := cfg.Domains[2].Follow; got != "" {
		t.Fatalf("non-follow strategy must ignore follow target, got %q", got)
	}
}

func TestNormalizeFollowRejectsMissingSelfAndChains(t *testing.T) {
	cases := []struct {
		name    string
		domains []Domain
		want    string
	}{
		{
			name: "missing target",
			domains: []Domain{{Host:"alias.example",Follow:"missing.example",Class:"follow",Enabled:true}},
			want: "does not exist",
		},
		{
			name: "self follow",
			domains: []Domain{{Host:"alias.example",Follow:"alias.example",Class:"follow",Enabled:true}},
			want: "cannot follow itself",
		},
		{
			name: "follow chain",
			domains: []Domain{
				{Host:"origin.example",Class:"latency",Enabled:true},
				{Host:"middle.example",Follow:"origin.example",Class:"follow",Enabled:true},
				{Host:"alias.example",Follow:"middle.example",Class:"follow",Enabled:true},
			},
			want: "follow chains are not allowed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Domains = tc.domains
			err := normalizeConfig(&cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalize error=%v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestApplyFollowMappingsMirrorsTargetExactly(t *testing.T) {
	cfg := defaultConfig()
	cfg.Domains = []Domain{
		{Host:"origin.example",Class:"latency",Mode:"http",Enabled:true},
		{Host:"alias.example",Follow:"origin.example",Class:"follow",Mode:"http",Enabled:true},
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	mappings := map[string]string{"origin.example":"104.16.0.8","alias.example":"104.16.0.99"}
	statuses := map[string]string{}
	health := map[string]DomainHealth{"origin.example":{FailureStreak:0,LastSuccess:"2026-09-27T00:00:00Z"}}
	if unresolved := applyFollowMappings(cfg,mappings,statuses,health); unresolved != 0 {
		t.Fatalf("unexpected unresolved followers: %d", unresolved)
	}
	if got := mappings["alias.example"]; got != "104.16.0.8" {
		t.Fatalf("follower mapping=%q, want exact target IP", got)
	}
	if !strings.Contains(statuses["alias.example"],"origin.example") {
		t.Fatalf("follow status missing target: %q", statuses["alias.example"])
	}
	if health["alias.example"].LastSuccess != health["origin.example"].LastSuccess {
		t.Fatalf("follower health must mirror target: %#v",health)
	}
}

func TestApplyFollowMappingsDropsFollowerWhenTargetUnavailable(t *testing.T) {
	cfg := defaultConfig()
	cfg.Domains = []Domain{
		{Host:"origin.example",Class:"latency",Mode:"http",Enabled:false},
		{Host:"alias.example",Follow:"origin.example",Class:"follow",Mode:"http",Enabled:true},
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	mappings := map[string]string{"alias.example":"104.16.0.99"}
	statuses := map[string]string{}
	health := map[string]DomainHealth{"alias.example":{LastSuccess:"old"}}
	if unresolved := applyFollowMappings(cfg,mappings,statuses,health); unresolved != 1 {
		t.Fatalf("unresolved=%d want 1",unresolved)
	}
	if _, ok := mappings["alias.example"]; ok {
		t.Fatalf("stale follower mapping must be removed: %#v",mappings)
	}
	if !strings.Contains(statuses["alias.example"],"target disabled") {
		t.Fatalf("unexpected follower status: %q",statuses["alias.example"])
	}
}

func TestSmartRepairSynchronizesFollowDomainWithoutIndependentVerification(t *testing.T) {
	cfg := defaultConfig()
	cfg.AutoApply = false
	cfg.Sync.Enabled = false
	cfg.Tracker.AutoDiscover = false
	cfg.Domains = []Domain{
		{Host:"origin.example",Class:"normal",Mode:"http",Endpoint:"/",Enabled:true},
		{Host:"alias.example",Follow:"origin.example",Class:"follow",Mode:"tracker",Endpoint:"/announce",Enabled:true},
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := &App{
		config: cfg,
		dataDir: t.TempDir(),
		state: RuntimeState{
			Mappings: map[string]string{},
			DomainStatus: map[string]string{},
			DomainHealth: map[string]DomainHealth{},
			TrackerSamples: map[string]TrackerSampleRuntime{},
			TrackerKeepalive: map[string]TrackerKeepaliveRuntime{},
			Candidates: []Candidate{{IP:"104.16.0.42",DelayMS:12,ObservedAt:now.Format(time.RFC3339)}},
		},
	}
	if err := a.runSmartRepair(context.Background(),cfg); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Mappings["origin.example"]; got != "104.16.0.42" {
		t.Fatalf("origin mapping=%q",got)
	}
	if got := a.state.Mappings["alias.example"]; got != "104.16.0.42" {
		t.Fatalf("follower mapping=%q, want target mapping",got)
	}
	if !strings.Contains(a.state.DomainStatus["alias.example"],"follow") {
		t.Fatalf("follow status missing: %q",a.state.DomainStatus["alias.example"])
	}
	if _, exists := a.state.TrackerSamples["alias.example"]; exists {
		t.Fatal("follow domain must not allocate Tracker sample state")
	}
}

func TestTrackerTargetsExcludeFollowStrategy(t *testing.T) {
	cfg := defaultConfig()
	cfg.Domains = []Domain{
		{Host:"origin.example",Class:"latency",Mode:"tracker",Enabled:true},
		{Host:"alias.example",Follow:"origin.example",Class:"follow",Mode:"tracker",Enabled:true},
	}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	targets := trackerTargetDomains(cfg)
	if !targets["origin.example"] {
		t.Fatal("independent tracker target missing")
	}
	if targets["alias.example"] {
		t.Fatal("follow tracker must not trigger sample discovery")
	}
}
