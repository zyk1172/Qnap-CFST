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


func TestTransmissionTrackerConnectionFailureFeedsRepairSignal(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "runtime-session")
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
			t.Fatalf("unexpected modern request %#v", req)
		}
		if !containsString(req.Params.Fields, "tracker_stats") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"result":{"torrents":[
					{"id":11,"hash_string":"%s","left_until_done":0,"is_finished":true,"status":6,"activity_date":500},
					{"id":12,"hash_string":"%s","left_until_done":0,"is_finished":true,"status":6,"activity_date":400}
				]},
				"id":1
			}`, testHashA, testHashB)))
			return
		}
		now := time.Now().Unix()
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"jsonrpc":"2.0",
			"result":{"torrents":[
				{
					"hash_string":"%s",
					"status":6,
					"tracker_stats":[{
						"announce":"https://tracker.m-team.cc/announce?passkey=sample",
						"host":"tracker.m-team.cc",
						"has_announced":true,
						"last_announce_result":"Working",
						"last_announce_succeeded":true,
						"last_announce_timed_out":false,
						"last_announce_time":%d
					}]
				},
				{
					"hash_string":"%s",
					"status":6,
					"tracker_stats":[{
						"announce":"https://tracker.m-team.cc/announce?passkey=other",
						"host":"tracker.m-team.cc",
						"has_announced":true,
						"last_announce_result":"Could not connect to tracker",
						"last_announce_succeeded":false,
						"last_announce_timed_out":false,
						"last_announce_time":%d
					}]
				}
			]},
			"id":1
		}`, testHashA, now-10, testHashB, now)))
	}))
	defer srv.Close()

	failures, err := transmissionTrackerConnectionFailures(context.Background(), DownloaderClientConfig{
		Enabled: true,
		URL: srv.URL,
	}, map[string]TrackerSample{
		"tracker.m-team.cc": {Domain:"tracker.m-team.cc", HashHex:testHashA},
	}, 200)
	if err != nil { t.Fatal(err) }
	failure, ok := failures["tracker.m-team.cc"]
	if !ok {
		t.Fatal("non-sample active torrent connection failure was not surfaced")
	}
	if !strings.Contains(failure.Detail, "Could not connect to tracker") {
		t.Fatalf("unexpected failure detail %q", failure.Detail)
	}
	if requests.Load() != 3 {
		t.Fatalf("expected 409 negotiation + active list + tracker_stats batch, got %d", requests.Load())
	}
}


func TestTransmissionBusinessTrackerErrorDoesNotInvalidateCandidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "runtime-session")
			w.Header().Set("X-Transmission-Rpc-Version", "6.0.0")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Params struct {
				Fields []string `json:"fields"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil { t.Fatal(err) }
		if !containsString(req.Params.Fields, "tracker_stats") {
			_, _ = w.Write([]byte(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"result":{"torrents":[{"id":11,"hash_string":"%s","left_until_done":0,"is_finished":true,"status":6,"activity_date":500}]},
				"id":1
			}`, testHashA)))
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"jsonrpc":"2.0",
			"result":{"torrents":[{
				"hash_string":"%s",
				"status":6,
				"tracker_stats":[{
					"announce":"https://tracker.m-team.cc/announce?passkey=secret",
					"host":"tracker.m-team.cc",
					"has_announced":true,
					"last_announce_result":"Missing key peer_id",
					"last_announce_succeeded":false,
					"last_announce_timed_out":false,
					"last_announce_time":%d
				}]
			}]},
			"id":1
		}`, testHashA, time.Now().Unix())))
	}))
	defer srv.Close()

	failures, err := transmissionTrackerConnectionFailures(context.Background(), DownloaderClientConfig{
		Enabled:true, URL:srv.URL,
	}, map[string]TrackerSample{
		"tracker.m-team.cc": {Domain:"tracker.m-team.cc", HashHex:testHashA},
	}, 200)
	if err != nil { t.Fatal(err) }
	if _, exists := failures["tracker.m-team.cc"]; exists {
		t.Fatal("business-level Tracker rejection must not invalidate the candidate")
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
	if got := transmissionTrackerStatusLabel(transmissionTrackerStat{HasAnnounced:true, LastAnnounceResult:"HTTP 403"}); got != "Error" {
		t.Fatalf("error label=%q", got)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want { return true }
	}
	return false
}
