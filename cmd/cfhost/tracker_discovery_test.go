package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testHashA = "0123456789abcdef0123456789abcdef01234567"
const testHashB = "89abcdef0123456789abcdef0123456789abcdef"

func TestDiscoverTransmissionSamplesModernRPCStopsAfterOneSample(t *testing.T) {
	var requests atomic.Int32
	var trackerLookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/transmission/rpc" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "session-123")
			w.Header().Set("X-Transmission-Rpc-Version", "6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params struct {
				Fields []string `json:"fields"`
				IDs []int `json:"ids"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil { t.Fatal(err) }
		if req.JSONRPC != "2.0" || req.Method != "torrent_get" {
			t.Fatalf("expected Transmission 4.1 JSON-RPC request, got %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		if len(req.Params.IDs) == 0 {
			_, _ = w.Write([]byte(`{
				"jsonrpc":"2.0",
				"result":{"torrents":[
					{"id":11,"hash_string":"0123456789abcdef0123456789abcdef01234567","left_until_done":0,"is_finished":true,"status":6,"activity_date":500},
					{"id":12,"hash_string":"89abcdef0123456789abcdef0123456789abcdef","left_until_done":0,"is_finished":true,"status":6,"activity_date":400}
				]},
				"id":1
			}`))
			return
		}
		trackerLookups.Add(1)
		if req.Params.IDs[0] != 11 {
			t.Fatalf("unexpected first torrent id %d", req.Params.IDs[0])
		}
		_, _ = w.Write([]byte(`{
			"jsonrpc":"2.0",
			"result":{"torrents":[
				{"id":11,"trackers":[{"announce":"https://tracker.m-team.cc/announce?passkey=modern-secret","tier":0}]},
				{"id":12,"trackers":[{"announce":"https://tracker.m-team.cc/announce?passkey=unused-second-sample","tier":0}]}
			]},
			"id":1
		}`))
	}))
	defer srv.Close()

	got, err := discoverTransmissionSamples(context.Background(), DownloaderClientConfig{Enabled:true, URL:srv.URL}, map[string]bool{"tracker.m-team.cc": true}, 20)
	if err != nil { t.Fatal(err) }
	if requests.Load() != 3 { t.Fatalf("expected 409 negotiation + lightweight list + one tracker lookup, got %d requests", requests.Load()) }
	if trackerLookups.Load() != 1 { t.Fatalf("expected one tracker lookup, got %d", trackerLookups.Load()) }
	sample, ok := got["tracker.m-team.cc"]
	if !ok { t.Fatal("missing m-team sample") }
	if sample.HashHex != testHashA || sample.Path != "/announce" || !strings.Contains(sample.URL, "modern-secret") {
		t.Fatalf("unexpected sample: %#v", sample)
	}
}

func TestDiscoverTransmissionSamplesLegacyRPC(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "legacy-session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method string `json:"method"`
			Arguments struct {
				Fields []string `json:"fields"`
				IDs []int `json:"ids"`
			} `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil { t.Fatal(err) }
		if req.Method != "torrent-get" { t.Fatalf("expected legacy torrent-get, got %s", req.Method) }
		if len(req.Arguments.IDs) == 0 {
			_, _ = w.Write([]byte(`{
				"result":"success",
				"arguments":{"torrents":[{"id":21,"hashString":"0123456789abcdef0123456789abcdef01234567","leftUntilDone":0,"isFinished":true,"status":6,"activityDate":200}]}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"result":"success",
			"arguments":{"torrents":[{"id":21,"trackers":[{"announce":"https://tracker.hdtime.org/announce.php?passkey=legacy-secret","tier":0}]}]}
		}`))
	}))
	defer srv.Close()

	got, err := discoverTransmissionSamples(context.Background(), DownloaderClientConfig{Enabled:true, URL:srv.URL}, map[string]bool{"tracker.hdtime.org": true}, 20)
	if err != nil { t.Fatal(err) }
	if requests.Load() != 3 { t.Fatalf("expected 409 negotiation + list + tracker lookup, got %d requests", requests.Load()) }
	if _, ok := got["tracker.hdtime.org"]; !ok { t.Fatal("missing legacy Transmission sample") }
}

func TestDiscoverQBittorrentSamplesWithTrackerFallback(t *testing.T) {
	var loginCalls atomic.Int32
	var trackerCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginCalls.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "abc", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if cookie, err := r.Cookie("SID"); err != nil || cookie.Value != "abc" {
				t.Fatalf("qB cookie missing")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{
					"hash":"89abcdef0123456789abcdef0123456789abcdef",
					"name":"ptcafe seed",
					"amount_left":0,
					"progress":1,
					"state":"uploading",
					"tracker":"",
					"last_activity":300
				}
			]`))
		case "/api/v2/torrents/trackers":
			trackerCalls.Add(1)
			if r.URL.Query().Get("hash") != testHashB {
				t.Fatalf("unexpected hash %s", r.URL.Query().Get("hash"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"url":"** [DHT] **","status":0,"tier":-1},
				{"url":"https://tracker.ptcafe.club/announce.php?passkey=qb-secret","status":2,"tier":0}
			]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	got, err := discoverQBittorrentSamples(context.Background(), DownloaderClientConfig{
		Enabled:  true,
		URL:      srv.URL,
		Username: "admin",
		Password: "password",
	}, map[string]bool{"tracker.ptcafe.club": true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls.Load() != 1 || trackerCalls.Load() != 1 {
		t.Fatalf("unexpected calls login=%d tracker=%d", loginCalls.Load(), trackerCalls.Load())
	}
	sample, ok := got["tracker.ptcafe.club"]
	if !ok {
		t.Fatal("missing qBittorrent-discovered sample")
	}
	if sample.HashHex != testHashB || sample.Path != "/announce.php" || !strings.Contains(sample.URL, "qb-secret") {
		t.Fatalf("unexpected sample: %#v", sample)
	}
}

func TestQBittorrentUsesWorkingTrackerFromTorrentList(t *testing.T) {
	var trackerCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[
				{
					"hash":"0123456789abcdef0123456789abcdef01234567",
					"name":"direct",
					"amount_left":0,
					"progress":1,
					"tracker":"https://tracker.hdtime.org/announce.php?passkey=direct",
					"last_activity":500
				}
			]`))
		case "/api/v2/torrents/trackers":
			trackerCalls.Add(1)
			t.Fatal("tracker fallback should not be called when the target was found directly")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	got, err := discoverQBittorrentSamples(context.Background(), DownloaderClientConfig{
		Enabled: true,
		URL:     srv.URL,
	}, map[string]bool{"tracker.hdtime.org": true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["tracker.hdtime.org"]; !ok {
		t.Fatal("missing direct qB tracker sample")
	}
	if trackerCalls.Load() != 0 {
		t.Fatal("unexpected fallback lookup")
	}
}

func TestSaveAndLoadAutoTrackerSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tracker-samples.auto.tsv")
	hash, err := decodeInfoHash(testHashA)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]TrackerSample{
		"tracker.m-team.cc": {
			Domain:  "tracker.m-team.cc",
			Path:    "/announce",
			HashHex: testHashA,
			Hash:    hash,
			URL:     "https://tracker.m-team.cc/announce?passkey=saved",
		},
	}
	if err := saveTrackerSamples(path, input); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected 0600 auto sample file, got %o", info.Mode().Perm())
	}
	loaded, err := loadTrackerSamples(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["tracker.m-team.cc"].HashHex != testHashA {
		t.Fatalf("unexpected loaded sample %#v", loaded["tracker.m-team.cc"])
	}
}

func TestNormalizeDownloaderURLs(t *testing.T) {
	got, err := normalizeTransmissionURL("http://nas:9091")
	if err != nil || got != "http://nas:9091/transmission/rpc" {
		t.Fatalf("unexpected Transmission URL %q err=%v", got, err)
	}
	got, err = normalizeTransmissionURL("http://nas:9091/transmission/")
	if err != nil || got != "http://nas:9091/transmission/rpc" {
		t.Fatalf("unexpected Transmission URL %q err=%v", got, err)
	}
	got, err = normalizeBaseURL("http://nas:8080/qb/", "qBittorrent")
	if err != nil || got != "http://nas:8080/qb" {
		t.Fatalf("unexpected qB URL %q err=%v", got, err)
	}
}


func TestTransmissionDomainHealthFindsNonSampleConnectionFailure(t *testing.T) {
	rows := []transmissionTrackerStatsRow{
		{ID:11, Status:6, TrackerStats:[]transmissionTrackerStat{{
			Announce:"https://tracker.m-team.cc/announce?passkey=sample",
			HasAnnounced:true,
			LastAnnounceSucceeded:true,
			LastAnnounceTime:100,
		}}},
		{ID:12, Status:6, TrackerStats:[]transmissionTrackerStat{{
			Announce:"https://tracker.m-team.cc/announce?passkey=other",
			HasAnnounced:true,
			LastAnnounceResult:"Could not connect to tracker",
			LastAnnounceTime:200,
		}}},
	}
	health := transmissionDomainHealthForTarget(rows, "tracker.m-team.cc")
	if health.Matched != 2 || health.Connected != 1 || health.ConnectionFailures != 1 {
		t.Fatalf("unexpected health: %#v", health)
	}
	if len(health.FailureTorrentIDs) != 1 || health.FailureTorrentIDs[0] != 12 {
		t.Fatalf("non-sample failed torrent id was not surfaced: %#v", health.FailureTorrentIDs)
	}
	if !strings.Contains(health.LatestFailure.Detail, "Could not connect to tracker") {
		t.Fatalf("unexpected failure detail %q", health.LatestFailure.Detail)
	}
}


func TestTransmissionBusinessTrackerErrorCountsAsConnected(t *testing.T) {
	rows := []transmissionTrackerStatsRow{
		{ID:11, Status:6, TrackerStats:[]transmissionTrackerStat{{
			Announce:"https://tracker.m-team.cc/announce",
			HasAnnounced:true,
			LastAnnounceResult:"Missing key peer_id",
			LastAnnounceSucceeded:false,
			LastAnnounceTimedOut:false,
			LastAnnounceTime:100,
		}}},
	}
	health := transmissionDomainHealthForTarget(rows, "tracker.m-team.cc")
	if health.Connected != 1 || health.BusinessErrors != 1 || health.ConnectionFailures != 0 {
		t.Fatalf("PT business error must prove Tracker connectivity: %#v", health)
	}
	if !trackerDomainHealthAcceptable(health) {
		t.Fatal("business-level Tracker rejection must not trigger Repair")
	}
	if got := transmissionTrackerStatusLabel(rows[0].TrackerStats[0]); got != "ConnectedError" {
		t.Fatalf("business error label=%q, want ConnectedError", got)
	}
}


func TestTransmissionTimedOutTrackerIsConnectionFailure(t *testing.T) {
	body := []byte(`{
		"jsonrpc":"2.0",
		"result":{"torrents":[{
			"hash_string":"0123456789abcdef0123456789abcdef01234567",
			"tracker_stats":[{
				"announce":"https://tracker.example.com/announce",
				"host":"tracker.example.com",
				"has_announced":true,
				"last_announce_result":"",
				"last_announce_succeeded":false,
				"last_announce_timed_out":true,
				"last_announce_time":123
			}]
		}]},
		"id":1
	}`)
	rows, err := parseTransmissionTrackerStats(body, true)
	if err != nil { t.Fatal(err) }
	if len(rows) != 1 || len(rows[0].TrackerStats) != 1 || !rows[0].TrackerStats[0].LastAnnounceTimedOut {
		t.Fatalf("timeout flag not parsed: %#v", rows)
	}
}


func TestParseTransmissionTrackerRuntimeFieldsModern(t *testing.T) {
	body := []byte(`{
		"jsonrpc":"2.0",
		"result":{"torrents":[{
			"hash_string":"0123456789abcdef0123456789abcdef01234567",
			"status":6,
			"tracker_stats":[{
				"announce":"https://tracker.example.com/announce",
				"host":"tracker.example.com",
				"announce_state":1,
				"has_announced":true,
				"last_announce_result":"Success",
				"last_announce_succeeded":true,
				"last_announce_timed_out":false,
				"last_announce_time":1700000000,
				"next_announce_time":1700001800,
				"last_announce_peer_count":12,
				"seeder_count":34,
				"leecher_count":5
			}]
		}]},
		"id":1
	}`)
	rows, err := parseTransmissionTrackerStats(body, true)
	if err != nil { t.Fatal(err) }
	if len(rows) != 1 || rows[0].Status != 6 || len(rows[0].TrackerStats) != 1 {
		t.Fatalf("unexpected parsed rows: %#v", rows)
	}
	stat := rows[0].TrackerStats[0]
	if stat.AnnounceState != 1 || stat.NextAnnounceTime != 1700001800 || stat.LastAnnouncePeerCount != 12 || stat.SeederCount != 34 || stat.LeecherCount != 5 {
		t.Fatalf("tracker runtime fields not parsed: %#v", stat)
	}
	if got := transmissionTrackerStatusLabel(stat); got != "Working" {
		t.Fatalf("successful announce status=%q, want Working", got)
	}
}

func TestTransmissionTrackerRuntimeForDomainAggregatesActiveSeeds(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "runtime-ui-session")
			w.Header().Set("X-Transmission-Rpc-Version", "6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params struct {
				Fields []string `json:"fields"`
				IDs []int `json:"ids"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil { t.Fatal(err) }
		if req.JSONRPC != "2.0" || req.Method != "torrent_get" {
			t.Fatalf("unexpected request: %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		if !containsString(req.Params.Fields, "tracker_stats") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"result":{"torrents":[
					{"id":11,"hash_string":"%s","left_until_done":0,"is_finished":true,"status":6,"activity_date":500},
					{"id":12,"hash_string":"%s","left_until_done":0,"is_finished":true,"status":6,"activity_date":400},
					{"id":13,"hash_string":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","left_until_done":0,"is_finished":true,"status":0,"activity_date":600}
				]},
				"id":1
			}`, testHashA, testHashB)))
			return
		}
		if len(req.Params.IDs) != 2 || req.Params.IDs[0] != 11 || req.Params.IDs[1] != 12 {
			t.Fatalf("expected both active seed ids, got %#v", req.Params.IDs)
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"jsonrpc":"2.0",
			"result":{"torrents":[
				{
					"hash_string":"%s",
					"status":6,
					"tracker_stats":[{
						"announce":"https://tracker.ptcafe.club/announce.php?passkey=hidden",
						"host":"tracker.ptcafe.club",
						"announce_state":1,
						"has_announced":true,
						"last_announce_result":"Working",
						"last_announce_succeeded":true,
						"last_announce_timed_out":false,
						"last_announce_time":1700000000,
						"next_announce_time":1700001800
					}]
				},
				{
					"hash_string":"%s",
					"status":6,
					"tracker_stats":[{
						"announce":"https://tracker.ptcafe.club/announce.php?passkey=other",
						"host":"tracker.ptcafe.club",
						"announce_state":1,
						"has_announced":true,
						"last_announce_result":"Could not connect to tracker",
						"last_announce_succeeded":false,
						"last_announce_timed_out":false,
						"last_announce_time":1700000100,
						"next_announce_time":1700001900
					}]
				}
			]},
			"id":1
		}`, testHashA, testHashB)))
	}))
	defer srv.Close()

	runtime, err := transmissionTrackerRuntimeForDomain(context.Background(), DownloaderClientConfig{
		Enabled:true,
		URL:srv.URL,
	}, "tracker.ptcafe.club", 200)
	if err != nil { t.Fatal(err) }
	if !runtime.Available || runtime.TrackerStatus != "Partial" {
		t.Fatalf("mixed Transmission status must not be reported as all Working: %#v", runtime)
	}
	if runtime.MatchedTorrents != 2 || runtime.SeedingTorrents != 2 || runtime.WorkingTorrents != 1 || runtime.ErrorTorrents != 1 {
		t.Fatalf("unexpected aggregate counts: %#v", runtime)
	}
	if len(runtime.Issues) != 1 || !strings.Contains(runtime.Issues[0].Result, "Could not connect to tracker") {
		t.Fatalf("actual red-seed error was not surfaced: %#v", runtime.Issues)
	}
	if requests.Load() != 3 {
		t.Fatalf("expected session negotiation + torrent list + one tracker_stats batch, got %d", requests.Load())
	}
}

func TestAggregateTransmissionTrackerRuntimeIgnoresStoppedTorrents(t *testing.T) {
	rows := []transmissionTrackerStatsRow{
		{Status:6, TrackerStats:[]transmissionTrackerStat{{
			Announce:"https://tracker.example.com/announce",
			HasAnnounced:true,
			LastAnnounceSucceeded:true,
			LastAnnounceTime:200,
		}}},
		{Status:0, TrackerStats:[]transmissionTrackerStat{{
			Announce:"https://tracker.example.com/announce",
			HasAnnounced:true,
			LastAnnounceResult:"Could not connect to tracker",
			LastAnnounceTime:300,
		}}},
	}
	got := aggregateTransmissionTrackerRuntime(rows,"tracker.example.com",2,2,false)
	if got.TrackerStatus!="Working" || got.MatchedTorrents!=1 || got.ErrorTorrents!=0 {
		t.Fatalf("stopped torrent must not poison active seed status: %#v",got)
	}
}


func TestTransmissionTrackerStatusLabelErrors(t *testing.T) {
	if got := transmissionTrackerStatusLabel(transmissionTrackerStat{HasAnnounced:true, LastAnnounceTimedOut:true}); got != "Timeout" {
		t.Fatalf("timeout label=%q", got)
	}
	if got := transmissionTrackerStatusLabel(transmissionTrackerStat{HasAnnounced:true, LastAnnounceResult:"Tracker gave HTTP response code 403"}); got != "Disconnected" {
		t.Fatalf("HTTP 403 must be treated as a failed connection for keepalive, label=%q", got)
	}
	if got := transmissionTrackerStatusLabel(transmissionTrackerStat{HasAnnounced:true, LastAnnounceSucceeded:true, LastAnnounceResult:"HTTP 403 Forbidden"}); got != "Disconnected" {
		t.Fatalf("HTTP 403 must override a succeeded flag, label=%q", got)
	}
	if got := transmissionTrackerStatusLabel(transmissionTrackerStat{HasAnnounced:true, LastAnnounceResult:"Could not connect to tracker"}); got != "Disconnected" {
		t.Fatalf("unreachable label=%q", got)
	}
	if got := transmissionTrackerStatusLabel(transmissionTrackerStat{}); got != "Waiting" {
		t.Fatalf("waiting label=%q", got)
	}
}

func TestDownloaderTrackerFailureStalenessGuard(t *testing.T) {
	failure := downloaderTrackerFailure{LastAnnounceTime: 1_800_000_000}
	before := time.Unix(failure.LastAnnounceTime-10, 0).UTC().Format(time.RFC3339)
	after := time.Unix(failure.LastAnnounceTime+10, 0).UTC().Format(time.RFC3339)
	if !downloaderFailureIsNewer(failure, before) {
		t.Fatal("new Transmission failure should invalidate an older successful mapping")
	}
	if downloaderFailureIsNewer(failure, after) {
		t.Fatal("stale Transmission failure must not invalidate a mapping repaired afterwards")
	}
}

func TestTrackerKeepaliveThresholdRequiresPercentAndAbsoluteLimit(t *testing.T) {
	cases := []struct{
		name string
		health transmissionDomainHealth
		want bool
	}{
		{"ninety-percent-five-failures", transmissionDomainHealth{Evaluated:50, Connected:45, ConnectionFailures:5, ConnectedPercent:90}, true},
		{"above-ninety-six-failures", transmissionDomainHealth{Evaluated:100, Connected:94, ConnectionFailures:6, ConnectedPercent:94}, false},
		{"below-ninety", transmissionDomainHealth{Evaluated:10, Connected:8, ConnectionFailures:2, ConnectedPercent:80}, false},
		{"no-failures", transmissionDomainHealth{Evaluated:10, Connected:10, ConnectedPercent:100}, true},
	}
	for _, tc := range cases {
		if got := trackerDomainHealthAcceptable(tc.health); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestTransmissionHealthScanIsNotCappedAt200(t *testing.T) {
	const total = 205
	var trackerStatLookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "full-scan-session")
			w.Header().Set("X-Transmission-Rpc-Version", "6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params struct {
				Fields []string `json:"fields"`
				IDs []int `json:"ids"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil { t.Fatal(err) }
		if req.Method != "torrent_get" { t.Fatalf("unexpected method %q", req.Method) }

		if !containsString(req.Params.Fields, "tracker_stats") {
			torrents := make([]map[string]any, 0, total)
			for i:=1;i<=total;i++ {
				torrents = append(torrents, map[string]any{
					"id":i, "hash_string":fmt.Sprintf("%040x", i),
					"left_until_done":0, "is_finished":true, "status":6, "activity_date":total-i,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","result":map[string]any{"torrents":torrents},"id":1})
			return
		}
		trackerStatLookups.Add(int32(len(req.Params.IDs)))
		rows := make([]map[string]any,0,len(req.Params.IDs))
		for _, id := range req.Params.IDs {
			rows = append(rows,map[string]any{
				"id":id,"hash_string":fmt.Sprintf("%040x",id),"status":6,
				"tracker_stats":[]map[string]any{{
					"announce":"https://tracker.example.com/announce",
					"has_announced":true,"last_announce_succeeded":true,"last_announce_time":100,
				}},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","result":map[string]any{"torrents":rows},"id":1})
	}))
	defer srv.Close()

	rows,totalActive,checked,truncated,err := transmissionActiveTrackerStats(context.Background(),DownloaderClientConfig{Enabled:true,URL:srv.URL},0)
	if err != nil { t.Fatal(err) }
	if totalActive != total || checked != total || len(rows) != total || truncated {
		t.Fatalf("health scan was truncated: active=%d checked=%d rows=%d truncated=%v",totalActive,checked,len(rows),truncated)
	}
	if got:=trackerStatLookups.Load();got!=total {
		t.Fatalf("tracker_stats looked up %d torrents, want %d",got,total)
	}
}

func TestTransmissionReannounceUsesReannounceAction(t *testing.T) {
	var action string
	var gotIDs []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id","reannounce-session")
			w.Header().Set("X-Transmission-Rpc-Version","6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params struct{ IDs []int `json:"ids"` } `json:"params"`
		}
		if err:=json.NewDecoder(r.Body).Decode(&req);err!=nil{t.Fatal(err)}
		action=req.Method
		gotIDs=append([]int(nil),req.Params.IDs...)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","result":map[string]any{},"id":1})
	}))
	defer srv.Close()

	if err:=transmissionReannounce(context.Background(),DownloaderClientConfig{Enabled:true,URL:srv.URL},[]int{12,11,12});err!=nil{t.Fatal(err)}
	if action!="torrent_reannounce" {
		t.Fatalf("action=%q, must use torrent_reannounce and never torrent_verify",action)
	}
	if len(gotIDs)!=2 || gotIDs[0]!=11 || gotIDs[1]!=12 {
		t.Fatalf("unexpected reannounce ids %#v",gotIDs)
	}
}

func TestTrackerKeepaliveObservesTwoMinutesAndKeepsThirtyMinuteReannounceInterval(t *testing.T) {
	oldInterval:=trackerKeepaliveInterval
	oldObservation:=trackerKeepaliveObservationDelay
	trackerKeepaliveInterval=time.Hour
	trackerKeepaliveObservationDelay=time.Hour
	defer func(){
		trackerKeepaliveInterval=oldInterval
		trackerKeepaliveObservationDelay=oldObservation
	}()

	var reannounceCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.Header.Get("X-Transmission-Session-Id")==""{
			w.Header().Set("X-Transmission-Session-Id","keepalive-session")
			w.Header().Set("X-Transmission-Rpc-Version","6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct{
			Method string `json:"method"`
			Params struct{
				Fields []string `json:"fields"`
				IDs []int `json:"ids"`
			} `json:"params"`
		}
		if err:=json.NewDecoder(r.Body).Decode(&req);err!=nil{t.Fatal(err)}
		switch req.Method{
		case "torrent_get":
			if !containsString(req.Params.Fields,"tracker_stats"){
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","result":map[string]any{"torrents":[]map[string]any{
					{"id":11,"hash_string":testHashA,"left_until_done":0,"is_finished":true,"status":6,"activity_date":500},
					{"id":12,"hash_string":testHashB,"left_until_done":0,"is_finished":true,"status":6,"activity_date":400},
				}},"id":1})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","result":map[string]any{"torrents":[]map[string]any{
				{"id":11,"hash_string":testHashA,"status":6,"tracker_stats":[]map[string]any{{
					"announce":"https://tracker.example.com/announce","has_announced":true,"last_announce_succeeded":true,"last_announce_time":100,
				}}},
				{"id":12,"hash_string":testHashB,"status":6,"tracker_stats":[]map[string]any{{
					"announce":"https://tracker.example.com/announce","has_announced":true,"last_announce_succeeded":false,
					"last_announce_result":"Could not connect to tracker","last_announce_time":200,
				}}},
			}},"id":1})
		case "torrent_reannounce":
			reannounceCalls.Add(1)
			if len(req.Params.IDs)!=1 || req.Params.IDs[0]!=12 {
				t.Fatalf("reannounce must target only failed torrent: %#v",req.Params.IDs)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","result":map[string]any{},"id":1})
		default:
			t.Fatalf("unexpected method %q",req.Method)
		}
	}))
	defer srv.Close()

	cfg:=defaultConfig()
	cfg.Tracker.Transmission=DownloaderClientConfig{Enabled:true,URL:srv.URL}
	cfg.Tracker.RealAnnounce=true
	cfg.Domains=[]Domain{{
		Host:"tracker.example.com",Class:"latency",Mode:"tracker",Enabled:true,
	}}
	samples:=map[string]TrackerSample{"tracker.example.com":{Domain:"tracker.example.com"}}
	a:=&App{state:RuntimeState{
		Mappings:map[string]string{"tracker.example.com":"104.16.0.10"},
		TrackerSamples:map[string]TrackerSampleRuntime{"tracker.example.com":{Available:true,Tested:true,Passed:true,LastTest:"2026-09-24T00:00:00Z"}},
		TrackerKeepalive:map[string]TrackerKeepaliveRuntime{},
		Logs:[]string{},
	}}

	// First failure starts reannounce 1/3 and enters an observation window.
	if failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples);len(failures)!=0{
		t.Fatalf("first reannounce escalated too early: %#v",failures)
	}
	state:=a.state.TrackerKeepalive["tracker.example.com"]
	if state.Attempts!=1 || state.Status!="observing" || state.NextCheck=="" || state.NextReannounce==""{
		t.Fatalf("first reannounce timing state is wrong: %#v",state)
	}
	if got:=reannounceCalls.Load();got!=1{t.Fatalf("reannounce calls=%d want 1",got)}

	// Even if the sample-test timestamp changes, the same keepalive cycle must
	// not reset while waiting for Transmission to update its announce state.
	sampleState:=a.state.TrackerSamples["tracker.example.com"]
	sampleState.LastTest="2026-09-24T00:01:00Z"
	a.state.TrackerSamples["tracker.example.com"]=sampleState
	if failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples);len(failures)!=0{
		t.Fatalf("observation window escalated early: %#v",failures)
	}
	state=a.state.TrackerKeepalive["tracker.example.com"]
	if state.Attempts!=1 || state.Status!="observing"{
		t.Fatalf("observation window or sample timestamp reset the cycle: %#v",state)
	}
	if got:=reannounceCalls.Load();got!=1{t.Fatalf("must not reannounce inside observation window, got %d",got)}

	// Observation is due, but 30-minute minimum interval is not. It should only
	// record that the status is still unhealthy and wait for NextReannounce.
	state.NextCheck=time.Now().Add(-time.Minute).Format(time.RFC3339)
	state.NextReannounce=time.Now().Add(time.Hour).Format(time.RFC3339)
	a.state.TrackerKeepalive["tracker.example.com"]=state
	if failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples);len(failures)!=0{
		t.Fatalf("first post-reannounce observation escalated early: %#v",failures)
	}
	state=a.state.TrackerKeepalive["tracker.example.com"]
	if state.Attempts!=1 || state.Status!="waiting" || state.NextCheck!=state.NextReannounce{
		t.Fatalf("must wait for low-frequency interval after observation: %#v",state)
	}
	if got:=reannounceCalls.Load();got!=1{t.Fatalf("observation must not trigger second reannounce, got %d",got)}

	// Reannounce 2/3.
	state.NextCheck=time.Now().Add(-time.Minute).Format(time.RFC3339)
	state.NextReannounce=time.Now().Add(-time.Minute).Format(time.RFC3339)
	a.state.TrackerKeepalive["tracker.example.com"]=state
	if failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples);len(failures)!=0{
		t.Fatalf("second reannounce escalated early: %#v",failures)
	}
	state=a.state.TrackerKeepalive["tracker.example.com"]
	if state.Attempts!=2 || state.Status!="observing"{t.Fatalf("second reannounce state: %#v",state)}

	// Observe attempt 2, then wait until the next low-frequency slot.
	state.NextCheck=time.Now().Add(-time.Minute).Format(time.RFC3339)
	state.NextReannounce=time.Now().Add(time.Hour).Format(time.RFC3339)
	a.state.TrackerKeepalive["tracker.example.com"]=state
	if failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples);len(failures)!=0{
		t.Fatalf("second observation escalated early: %#v",failures)
	}
	state=a.state.TrackerKeepalive["tracker.example.com"]
	if state.Status!="waiting" || state.Attempts!=2{t.Fatalf("second observation wait state: %#v",state)}

	// Reannounce 3/3.
	state.NextCheck=time.Now().Add(-time.Minute).Format(time.RFC3339)
	state.NextReannounce=time.Now().Add(-time.Minute).Format(time.RFC3339)
	a.state.TrackerKeepalive["tracker.example.com"]=state
	if failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples);len(failures)!=0{
		t.Fatalf("third reannounce escalated before observation: %#v",failures)
	}
	state=a.state.TrackerKeepalive["tracker.example.com"]
	if state.Attempts!=3 || state.Status!="observing"{t.Fatalf("third reannounce state: %#v",state)}
	if got:=reannounceCalls.Load();got!=3{t.Fatalf("reannounce calls=%d want 3",got)}

	// Only after the post-third observation window may Repair be triggered.
	state.NextCheck=time.Now().Add(-time.Minute).Format(time.RFC3339)
	a.state.TrackerKeepalive["tracker.example.com"]=state
	failures:=a.evaluateTransmissionTrackerKeepalive(context.Background(),cfg,samples)
	failure,ok:=failures["tracker.example.com"]
	if !ok || !strings.Contains(failure.Detail,"keepalive exhausted"){
		t.Fatalf("third post-reannounce observation must escalate to Repair: %#v",failures)
	}
	if failure.RejectedIP!="104.16.0.10"{
		t.Fatalf("Repair must receive the exhausted mapping as rejected IP, got %q",failure.RejectedIP)
	}
	state=a.state.TrackerKeepalive["tracker.example.com"]
	if state.RejectedIP!="104.16.0.10" || state.RejectedAt==""{
		t.Fatalf("rejected mapping must persist across Repair cycles: %#v",state)
	}
	if got:=reannounceCalls.Load();got!=3{t.Fatalf("must not reannounce a fourth time, got %d",got)}
}


func TestPostRepairKeepaliveTargetsRecoveredTracker(t *testing.T) {
	cfg := defaultConfig()
	cfg.Tracker.RealAnnounce = true
	cfg.Tracker.Transmission.Enabled = true
	cfg.Domains = []Domain{{
		Host: "tracker.example.com", Group: "example", Class: "latency",
		Mode: "tracker", Endpoint: "/announce", Enabled: true,
	}}
	samples := map[string]TrackerSample{
		"tracker.example.com": {Domain: "tracker.example.com"},
	}
	a := &App{state: RuntimeState{
		TrackerSamples: map[string]TrackerSampleRuntime{
			"tracker.example.com": {Available: true, Tested: true, Passed: true},
		},
		TrackerKeepalive: map[string]TrackerKeepaliveRuntime{
			"tracker.example.com": {Status: "repair", Attempts: 0},
		},
	}}

	targets := a.trackerKeepalivePostRepairTargets(
		cfg, samples,
		map[string]string{"tracker.example.com": "104.16.0.10"},
		map[string]string{"tracker.example.com": "104.16.0.11"},
	)
	if !targets["tracker.example.com"] {
		t.Fatal("a verified replacement mapping must trigger an immediate keepalive recheck")
	}

	// The same bug can occur when the sample recovers without changing the
	// mapping: repair/0 with no schedule must still be re-entered immediately.
	targets = a.trackerKeepalivePostRepairTargets(
		cfg, samples,
		map[string]string{"tracker.example.com": "104.16.0.11"},
		map[string]string{"tracker.example.com": "104.16.0.11"},
	)
	if !targets["tracker.example.com"] {
		t.Fatal("sample recovery must not leave repair/0 stranded without a schedule")
	}

	// An exhausted/rejected mapping is different: it must be replaced, not
	// silently restarted against the same rejected IP.
	a.state.TrackerKeepalive["tracker.example.com"] = TrackerKeepaliveRuntime{
		Status: "repair", Attempts: trackerKeepaliveMaxAttempts, RejectedIP: "104.16.0.11",
	}
	targets = a.trackerKeepalivePostRepairTargets(
		cfg, samples,
		map[string]string{"tracker.example.com": "104.16.0.11"},
		map[string]string{"tracker.example.com": "104.16.0.11"},
	)
	if targets["tracker.example.com"] {
		t.Fatal("exhausted rejected mapping must not restart keepalive without a replacement")
	}
}

func TestPostRepairKeepaliveReannouncesInSameRepairCycle(t *testing.T) {
	var reannounceCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "post-repair-session")
			w.Header().Set("X-Transmission-Rpc-Version", "6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params struct {
				Fields []string `json:"fields"`
				IDs    []int    `json:"ids"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		switch req.Method {
		case "torrent_get":
			if !containsString(req.Params.Fields, "tracker_stats") {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"result": map[string]any{"torrents": []map[string]any{{
						"id": 11, "hash_string": testHashA, "left_until_done": 0,
						"is_finished": true, "status": 6, "activity_date": 500,
					}}},
					"id": 1,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result": map[string]any{"torrents": []map[string]any{{
					"id": 11, "hash_string": testHashA, "status": 6,
					"tracker_stats": []map[string]any{{
						"announce": "https://tracker.example.com/announce",
						"has_announced": true,
						"last_announce_succeeded": false,
						"last_announce_result": "Could not connect to tracker",
						"last_announce_time": 200,
					}},
				}}},
				"id": 1,
			})
		case "torrent_reannounce":
			reannounceCalls.Add(1)
			if len(req.Params.IDs) != 1 || req.Params.IDs[0] != 11 {
				t.Fatalf("post-repair reannounce must target only the failed torrent: %#v", req.Params.IDs)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "result": map[string]any{}, "id": 1,
			})
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Tracker.RealAnnounce = true
	cfg.Tracker.Transmission = DownloaderClientConfig{Enabled: true, URL: srv.URL}
	cfg.Domains = []Domain{{
		Host: "tracker.example.com", Group: "example", Class: "latency",
		Mode: "tracker", Endpoint: "/announce", Enabled: true,
	}}
	samples := map[string]TrackerSample{
		"tracker.example.com": {Domain: "tracker.example.com"},
	}
	a := &App{state: RuntimeState{
		Mappings: map[string]string{"tracker.example.com": "104.16.0.11"},
		TrackerSamples: map[string]TrackerSampleRuntime{
			"tracker.example.com": {
				Available: true, Tested: true, Passed: true,
				LastTest: "2026-09-25T10:09:18Z",
			},
		},
		TrackerKeepalive: map[string]TrackerKeepaliveRuntime{
			"tracker.example.com": {Status: "repair", Attempts: 0},
		},
		Logs: []string{},
	}}

	a.reconcileTransmissionTrackerKeepaliveAfterRepair(
		context.Background(), cfg, samples,
		map[string]string{"tracker.example.com": "104.16.0.10"},
		map[string]string{"tracker.example.com": "104.16.0.11"},
	)

	if got := reannounceCalls.Load(); got != 1 {
		t.Fatalf("same Repair cycle must call torrent_reannounce once, got %d", got)
	}
	state := a.state.TrackerKeepalive["tracker.example.com"]
	if state.Attempts != 1 || state.Status != "observing" {
		t.Fatalf("post-repair keepalive must enter observing 1/3, got %#v", state)
	}
	if state.NextCheck == "" || state.NextReannounce == "" || state.LastReannounce == "" {
		t.Fatalf("post-repair reannounce must schedule observation and minimum interval, got %#v", state)
	}
	if !strings.Contains(state.LastEvent, "reannounce 1/3") {
		t.Fatalf("post-repair state must expose the actual reannounce action, got %q", state.LastEvent)
	}
}

func TestNormalTrackerNeverEntersPostRepairKeepalive(t *testing.T) {
	cfg:=defaultConfig()
	cfg.Tracker.RealAnnounce=true
	cfg.Tracker.Transmission.Enabled=true
	cfg.Domains=[]Domain{{
		Host:"tracker.normal.example",Class:"normal",Mode:"tracker",Enabled:true,
	}}
	samples:=map[string]TrackerSample{
		"tracker.normal.example":{Domain:"tracker.normal.example"},
	}
	a:=&App{state:RuntimeState{
		TrackerSamples:map[string]TrackerSampleRuntime{
			"tracker.normal.example":{Available:true,Tested:true,Passed:true},
		},
		TrackerKeepalive:map[string]TrackerKeepaliveRuntime{
			"tracker.normal.example":{Status:"repair",Attempts:0},
		},
	}}
	targets:=a.trackerKeepalivePostRepairTargets(
		cfg,samples,
		map[string]string{"tracker.normal.example":"104.16.0.10"},
		map[string]string{"tracker.normal.example":"104.16.0.11"},
	)
	if targets["tracker.normal.example"] {
		t.Fatal("Normal Tracker must never enter post-Repair keepalive/reannounce")
	}
}

func TestNormalTrackerSampleDoesNotTriggerTransmissionHealthScan(t *testing.T) {
	var requests atomic.Int32
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		requests.Add(1)
		t.Fatal("Normal-only Tracker sample must not trigger any Transmission health request")
	}))
	defer srv.Close()

	cfg:=defaultConfig()
	cfg.Tracker.RealAnnounce=true
	cfg.Tracker.Transmission=DownloaderClientConfig{Enabled:true,URL:srv.URL}
	cfg.Domains=[]Domain{{
		Host:"tracker.normal.example",Class:"normal",Mode:"tracker",Enabled:true,
	}}
	a:=&App{state:RuntimeState{
		Mappings:map[string]string{"tracker.normal.example":"104.16.0.10"},
		TrackerKeepalive:map[string]TrackerKeepaliveRuntime{},
	}}
	failures:=a.evaluateTransmissionTrackerKeepalive(
		context.Background(),cfg,
		map[string]TrackerSample{"tracker.normal.example":{Domain:"tracker.normal.example"}},
	)
	if len(failures)!=0 {
		t.Fatalf("Normal Tracker produced downloader failures: %#v",failures)
	}
	if got:=requests.Load();got!=0 {
		t.Fatalf("Transmission requests=%d want 0 for Normal-only Tracker",got)
	}
}

func TestRejectedTrackerIPIsExcludedFromCandidateOrder(t *testing.T) {
	cfg:=defaultConfig()
	cfg.Verify.CandidateLimit=10
	d:=Domain{Host:"tracker.example.com",Class:"latency",Mode:"tracker",Enabled:true}
	candidates:=[]Candidate{
		{IP:"104.16.0.10",DelayMS:5,LossRate:0,SpeedMB:100},
		{IP:"104.16.0.11",DelayMS:10,LossRate:0,SpeedMB:90},
	}
	order:=orderedCandidates(candidates,d,"","104.16.0.10",cfg)
	if len(order)!=1 || order[0].IP!="104.16.0.11"{
		t.Fatalf("rejected IP re-entered candidate order: %#v",order)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want { return true }
	}
	return false
}
