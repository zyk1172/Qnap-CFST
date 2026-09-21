package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TrackerDiscoveryReport struct {
	Transmission int      `json:"transmission"`
	QBittorrent  int      `json:"qbittorrent"`
	Domains      []string `json:"domains"`
	Errors       []string `json:"errors,omitempty"`
}

type transmissionTorrentSummary struct {
	ID            int
	HashString    string
	LeftUntilDone int64
	IsFinished    bool
	Status        int
	ActivityDate  int64
}

type transmissionTracker struct {
	Announce string `json:"announce"`
	Tier     int    `json:"tier"`
}

type transmissionRPCClient struct {
	endpoint      string
	cfg           DownloaderClientConfig
	client        *http.Client
	sessionID     string
	modern        bool
	protocolKnown bool
}

func transmissionRPCVersionIsModern(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	var major int
	_, err := fmt.Sscanf(version, "%d", &major)
	return err == nil && major >= 6
}

func (c *transmissionRPCClient) call(ctx context.Context, legacyMethod, modernMethod string, legacyArgs, modernParams map[string]any) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var payload []byte
		if c.modern {
			payload, _ = json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  modernMethod,
				"params":  modernParams,
				"id":      1,
			})
		} else {
			payload, _ = json.Marshal(map[string]any{
				"method":    legacyMethod,
				"arguments": legacyArgs,
			})
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", c.sessionID)
		}
		if c.cfg.Username != "" || c.cfg.Password != "" {
			req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Transmission request: %w", err)
		}
		if resp.StatusCode == http.StatusConflict {
			c.sessionID = resp.Header.Get("X-Transmission-Session-Id")
			if !c.protocolKnown {
				if version := resp.Header.Get("X-Transmission-Rpc-Version"); version != "" {
					c.modern = transmissionRPCVersionIsModern(version)
					c.protocolKnown = true
				}
			}
			_ = resp.Body.Close()
			if c.sessionID == "" {
				return nil, fmt.Errorf("Transmission did not return X-Transmission-Session-Id")
			}
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Transmission HTTP %d", resp.StatusCode)
		}
		return body, nil
	}
	return nil, fmt.Errorf("Transmission RPC session negotiation failed")
}

func parseTransmissionTorrentList(body []byte, modern bool) ([]transmissionTorrentSummary, error) {
	var rows []json.RawMessage
	if modern {
		var response struct {
			Result struct {
				Torrents []json.RawMessage `json:"torrents"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("Transmission JSON-RPC response: %w", err)
		}
		if response.Error != nil {
			return nil, fmt.Errorf("Transmission JSON-RPC: %s", response.Error.Message)
		}
		rows = response.Result.Torrents
	} else {
		var response struct {
			Result    string `json:"result"`
			Arguments struct {
				Torrents []json.RawMessage `json:"torrents"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("Transmission response: %w", err)
		}
		if response.Result != "" && response.Result != "success" {
			return nil, fmt.Errorf("Transmission RPC: %s", response.Result)
		}
		rows = response.Arguments.Torrents
	}

	out := make([]transmissionTorrentSummary, 0, len(rows))
	for _, row := range rows {
		if modern {
			var v struct {
				ID            int    `json:"id"`
				HashString    string `json:"hash_string"`
				LeftUntilDone int64  `json:"left_until_done"`
				IsFinished    bool   `json:"is_finished"`
				Status        int    `json:"status"`
				ActivityDate  int64  `json:"activity_date"`
			}
			if json.Unmarshal(row, &v) != nil {
				continue
			}
			out = append(out, transmissionTorrentSummary{v.ID, v.HashString, v.LeftUntilDone, v.IsFinished, v.Status, v.ActivityDate})
		} else {
			var v struct {
				ID            int    `json:"id"`
				HashString    string `json:"hashString"`
				LeftUntilDone int64  `json:"leftUntilDone"`
				IsFinished    bool   `json:"isFinished"`
				Status        int    `json:"status"`
				ActivityDate  int64  `json:"activityDate"`
			}
			if json.Unmarshal(row, &v) != nil {
				continue
			}
			out = append(out, transmissionTorrentSummary{v.ID, v.HashString, v.LeftUntilDone, v.IsFinished, v.Status, v.ActivityDate})
		}
	}
	return out, nil
}

func parseTransmissionTrackers(body []byte, modern bool) ([]transmissionTracker, error) {
	var rows []struct {
		Trackers []transmissionTracker `json:"trackers"`
	}
	if modern {
		var response struct {
			Result struct {
				Torrents []struct {
					Trackers []transmissionTracker `json:"trackers"`
				} `json:"torrents"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		if response.Error != nil {
			return nil, fmt.Errorf("Transmission JSON-RPC: %s", response.Error.Message)
		}
		rows = response.Result.Torrents
	} else {
		var response struct {
			Result    string `json:"result"`
			Arguments struct {
				Torrents []struct {
					Trackers []transmissionTracker `json:"trackers"`
				} `json:"torrents"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		if response.Result != "" && response.Result != "success" {
			return nil, fmt.Errorf("Transmission RPC: %s", response.Result)
		}
		rows = response.Arguments.Torrents
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].Trackers, nil
}

type qbitTorrent struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	AmountLeft   int64   `json:"amount_left"`
	Progress     float64 `json:"progress"`
	State        string  `json:"state"`
	Tracker      string  `json:"tracker"`
	LastActivity int64   `json:"last_activity"`
}

type qbitTracker struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Tier   int    `json:"tier"`
}

type rankedTrackerSample struct {
	Sample TrackerSample
	Score  int64
}

func trackerTargetDomains(cfg Config) map[string]bool {
	out := make(map[string]bool)
	for _, d := range cfg.Domains {
		if d.Enabled && d.Mode == "tracker" {
			out[strings.ToLower(strings.TrimSpace(d.Host))] = true
		}
	}
	return out
}

func sampleFromAnnounce(hashHex, rawURL string, targets map[string]bool) (TrackerSample, bool) {
	hashHex = strings.ToLower(strings.TrimSpace(hashHex))
	if len(hashHex) != 40 {
		return TrackerSample{}, false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != "https" {
		return TrackerSample{}, false
	}
	domain := strings.ToLower(u.Hostname())
	if domain == "" || !targets[domain] {
		return TrackerSample{}, false
	}
	hashBytes, err := decodeInfoHash(hashHex)
	if err != nil {
		return TrackerSample{}, false
	}
	pathValue := u.EscapedPath()
	if pathValue == "" {
		pathValue = "/"
	}
	// Existing TrackerSample parser compares URL.Path, not EscapedPath.
	pathValue = u.Path
	if pathValue == "" {
		pathValue = "/"
	}
	return TrackerSample{
		Domain:  domain,
		Path:    pathValue,
		HashHex: hashHex,
		Hash:    hashBytes,
		URL:     u.String(),
	}, true
}

func decodeInfoHash(hashHex string) ([]byte, error) {
	if len(hashHex) != 40 {
		return nil, fmt.Errorf("info hash must be 40 hex chars")
	}
	out := make([]byte, 20)
	for i := 0; i < 20; i++ {
		var v byte
		for j := 0; j < 2; j++ {
			c := hashHex[i*2+j]
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= c - '0'
			case c >= 'a' && c <= 'f':
				v |= c - 'a' + 10
			case c >= 'A' && c <= 'F':
				v |= c - 'A' + 10
			default:
				return nil, fmt.Errorf("invalid info hash")
			}
		}
		out[i] = v
	}
	return out, nil
}

func missingTargets(targets map[string]bool, found map[string]rankedTrackerSample) int {
	n := 0
	for domain := range targets {
		if _, ok := found[domain]; !ok {
			n++
		}
	}
	return n
}

func chooseSample(found map[string]rankedTrackerSample, sample TrackerSample, score int64) {
	old, ok := found[sample.Domain]
	if !ok || score > old.Score {
		found[sample.Domain] = rankedTrackerSample{Sample: sample, Score: score}
	}
}

func flattenRankedSamples(found map[string]rankedTrackerSample) map[string]TrackerSample {
	out := make(map[string]TrackerSample, len(found))
	for domain, item := range found {
		out[domain] = item.Sample
	}
	return out
}

func discoverTransmissionSamples(ctx context.Context, cfg DownloaderClientConfig, targets map[string]bool, maxTrackerLookups int) (map[string]TrackerSample, error) {
	if !cfg.Enabled {
		return map[string]TrackerSample{}, nil
	}
	endpoint, err := normalizeTransmissionURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if maxTrackerLookups < 1 {
		maxTrackerLookups = 200
	}

	rpc := &transmissionRPCClient{
		endpoint: endpoint,
		cfg:      cfg,
		client:   &http.Client{},
	}

	// Stage 1: fetch only lightweight torrent metadata. Do not pull tracker
	// arrays for every torrent; large PT libraries can make that response huge.
	body, err := rpc.call(
		ctx,
		"torrent-get",
		"torrent_get",
		map[string]any{"fields": []string{"id", "hashString", "leftUntilDone", "isFinished", "status", "activityDate"}},
		map[string]any{"fields": []string{"id", "hash_string", "left_until_done", "is_finished", "status", "activity_date"}},
	)
	if err != nil {
		return nil, err
	}
	torrents, err := parseTransmissionTorrentList(body, rpc.modern)
	if err != nil {
		return nil, err
	}

	eligible := torrents[:0]
	for _, torrent := range torrents {
		if len(torrent.HashString) != 40 {
			continue
		}
		if torrent.LeftUntilDone != 0 && !torrent.IsFinished {
			continue
		}
		eligible = append(eligible, torrent)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		iSeed := eligible[i].Status == 6
		jSeed := eligible[j].Status == 6
		if iSeed != jSeed {
			return iSeed
		}
		return eligible[i].ActivityDate > eligible[j].ActivityDate
	})

	// Stage 2: ask for trackers only for recent completed/seeding torrents.
	// One sample per target Tracker domain is enough; after a domain is found,
	// later torrents for that domain are ignored.
	found := make(map[string]rankedTrackerSample)
	lookups := 0
	for _, torrent := range eligible {
		if missingTargets(targets, found) == 0 || lookups >= maxTrackerLookups {
			break
		}
		lookups++
		body, err := rpc.call(
			ctx,
			"torrent-get",
			"torrent_get",
			map[string]any{
				"ids":    []int{torrent.ID},
				"fields": []string{"id", "hashString", "trackers"},
			},
			map[string]any{
				"ids":    []int{torrent.ID},
				"fields": []string{"id", "hash_string", "trackers"},
			},
		)
		if err != nil {
			return nil, err
		}
		trackers, err := parseTransmissionTrackers(body, rpc.modern)
		if err != nil {
			return nil, fmt.Errorf("Transmission trackers for torrent %d: %w", torrent.ID, err)
		}
		for _, tracker := range trackers {
			sample, ok := sampleFromAnnounce(torrent.HashString, tracker.Announce, targets)
			if !ok {
				continue
			}
			if _, exists := found[sample.Domain]; exists {
				continue
			}
			found[sample.Domain] = rankedTrackerSample{Sample: sample, Score: torrent.ActivityDate}
		}
	}
	return flattenRankedSamples(found), nil
}

func normalizeTransmissionURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid Transmission URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("Transmission URL must use http or https")
	}
	pathValue := strings.TrimRight(u.Path, "/")
	switch {
	case pathValue == "":
		u.Path = "/transmission/rpc"
	case strings.HasSuffix(pathValue, "/rpc"):
		u.Path = pathValue
	case strings.HasSuffix(pathValue, "/transmission"):
		u.Path = pathValue + "/rpc"
	default:
		u.Path = pathValue + "/rpc"
	}
	return u.String(), nil
}

func discoverQBittorrentSamples(ctx context.Context, cfg DownloaderClientConfig, targets map[string]bool, maxTrackerLookups int) (map[string]TrackerSample, error) {
	if !cfg.Enabled {
		return map[string]TrackerSample{}, nil
	}
	base, err := normalizeBaseURL(cfg.URL, "qBittorrent")
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	if cfg.Username != "" || cfg.Password != "" {
		form := url.Values{"username": {cfg.Username}, "password": {cfg.Password}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", base+"/")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qBittorrent login: %w", err)
		}
		loginBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.EqualFold(strings.TrimSpace(string(loginBody)), "Ok.") {
			return nil, fmt.Errorf("qBittorrent login failed (HTTP %d)", resp.StatusCode)
		}
	}

	infoURL := base + "/api/v2/torrents/info?filter=completed&sort=last_activity&reverse=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", base+"/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qBittorrent torrent list: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qBittorrent torrent list HTTP %d", resp.StatusCode)
	}
	var torrents []qbitTorrent
	if err := json.Unmarshal(body, &torrents); err != nil {
		return nil, fmt.Errorf("qBittorrent torrent list: %w", err)
	}

	sort.SliceStable(torrents, func(i, j int) bool { return torrents[i].LastActivity > torrents[j].LastActivity })
	found := make(map[string]rankedTrackerSample)
	for _, torrent := range torrents {
		if torrent.AmountLeft != 0 || len(torrent.Hash) != 40 || torrent.Tracker == "" {
			continue
		}
		if sample, ok := sampleFromAnnounce(torrent.Hash, torrent.Tracker, targets); ok {
			chooseSample(found, sample, torrent.LastActivity)
		}
	}
	if missingTargets(targets, found) == 0 {
		return flattenRankedSamples(found), nil
	}

	if maxTrackerLookups < 1 {
		maxTrackerLookups = 200
	}
	lookups := 0
	for _, torrent := range torrents {
		if missingTargets(targets, found) == 0 || lookups >= maxTrackerLookups {
			break
		}
		if torrent.AmountLeft != 0 || len(torrent.Hash) != 40 {
			continue
		}
		lookups++
		trackerURL := base + "/api/v2/torrents/trackers?hash=" + url.QueryEscape(torrent.Hash)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, trackerURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Referer", base+"/")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qBittorrent tracker lookup: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var trackers []qbitTracker
		if json.Unmarshal(body, &trackers) != nil {
			continue
		}
		for _, tracker := range trackers {
			if tracker.Status == 0 {
				continue
			}
			if sample, ok := sampleFromAnnounce(torrent.Hash, tracker.URL, targets); ok {
				chooseSample(found, sample, torrent.LastActivity-int64(tracker.Tier))
			}
		}
	}
	return flattenRankedSamples(found), nil
}

func normalizeBaseURL(raw, name string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid %s URL", name)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s URL must use http or https", name)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func discoverTrackerSamples(ctx context.Context, cfg Config) (map[string]TrackerSample, TrackerDiscoveryReport) {
	targets := trackerTargetDomains(cfg)
	out := make(map[string]TrackerSample)
	report := TrackerDiscoveryReport{}
	if len(targets) == 0 {
		return out, report
	}

	timeout := time.Duration(cfg.Tracker.DiscoveryTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if cfg.Tracker.Transmission.Enabled {
		samples, err := discoverTransmissionSamples(discoveryCtx, cfg.Tracker.Transmission, targets, cfg.Tracker.MaxTrackerLookups)
		if err != nil {
			report.Errors = append(report.Errors, "Transmission: "+err.Error())
		} else {
			report.Transmission = len(samples)
			for domain, sample := range samples {
				out[domain] = sample
			}
		}
	}
	if cfg.Tracker.QBittorrent.Enabled {
		samples, err := discoverQBittorrentSamples(discoveryCtx, cfg.Tracker.QBittorrent, targets, cfg.Tracker.MaxTrackerLookups)
		if err != nil {
			report.Errors = append(report.Errors, "qBittorrent: "+err.Error())
		} else {
			report.QBittorrent = len(samples)
			// qB only fills domains that Transmission did not already provide.
			for domain, sample := range samples {
				if _, exists := out[domain]; !exists {
					out[domain] = sample
				}
			}
		}
	}

	report.Domains = make([]string, 0, len(out))
	for domain := range out {
		report.Domains = append(report.Domains, domain)
	}
	sort.Strings(report.Domains)
	return out, report
}

func mergeTrackerSamples(dst, src map[string]TrackerSample, overwrite bool) {
	for domain, sample := range src {
		if _, exists := dst[domain]; !exists || overwrite {
			dst[domain] = sample
		}
	}
}

func saveTrackerSamples(path string, samples map[string]TrackerSample) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("tracker auto samples path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	domains := make([]string, 0, len(samples))
	for domain := range samples {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	var b strings.Builder
	b.WriteString("# generated by CFHost tracker auto-discovery\n")
	for _, domain := range domains {
		s := samples[domain]
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", s.Domain, s.Path, s.HashHex, s.URL)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *App) refreshAutoTrackerSamples(ctx context.Context, cfg Config) (map[string]TrackerSample, TrackerDiscoveryReport) {
	cache := map[string]TrackerSample{}
	if loaded, err := loadTrackerSamples(cfg.Tracker.AutoSamplesPath); loaded != nil {
		cache = loaded
		_ = err
	}
	discovered, report := discoverTrackerSamples(ctx, cfg)
	if len(discovered) > 0 {
		mergeTrackerSamples(cache, discovered, true)
		if err := saveTrackerSamples(cfg.Tracker.AutoSamplesPath, cache); err != nil {
			report.Errors = append(report.Errors, "persist auto samples: "+err.Error())
		}
	}
	return cache, report
}
