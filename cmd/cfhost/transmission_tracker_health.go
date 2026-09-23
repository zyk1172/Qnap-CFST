package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type downloaderTrackerFailure struct {
	Source           string
	Detail           string
	LastAnnounceTime int64
}

type transmissionTrackerStat struct {
	Announce              string
	Host                  string
	AnnounceState         int
	HasAnnounced          bool
	LastAnnounceResult    string
	LastAnnounceSucceeded bool
	LastAnnounceTimedOut  bool
	LastAnnounceTime      int64
	NextAnnounceTime      int64
	LastAnnouncePeerCount int
	SeederCount           int
	LeecherCount          int
}

type transmissionTrackerStatsRow struct {
	ID           int
	HashString   string
	Status       int
	TrackerStats []transmissionTrackerStat
}

const (
	trackerKeepaliveMaxAttempts       = 3
	trackerKeepaliveMinConnectedPct   = 90.0
	trackerKeepaliveMaxFailures       = 5
)
var trackerKeepaliveInterval = 30 * time.Minute

type transmissionDomainHealth struct {
	Matched            int
	Evaluated          int
	Connected          int
	Working            int
	BusinessErrors     int
	ConnectionFailures int
	Waiting            int
	ConnectedPercent   float64
	FailureTorrentIDs  []int
	LatestFailure      downloaderTrackerFailure
}

type transmissionTrackerRuntimeIssue struct {
	Result           string `json:"result"`
	LastAnnounceTime int64  `json:"lastAnnounceTime"`
	TorrentStatus    int    `json:"torrentStatus"`
	TimedOut         bool   `json:"timedOut"`
}

type transmissionTrackerRuntime struct {
	Available             bool                              `json:"available"`
	Source                string                            `json:"source"`
	TrackerStatus         string                            `json:"trackerStatus"`
	TorrentStatus         int                               `json:"torrentStatus"`
	Seeding               bool                              `json:"seeding"`
	ActiveTorrents        int                               `json:"activeTorrents"`
	MatchedTorrents       int                               `json:"matchedTorrents"`
	SeedingTorrents       int                               `json:"seedingTorrents"`
	QueuedTorrents        int                               `json:"queuedTorrents"`
	WorkingTorrents       int                               `json:"workingTorrents"`
	BusinessErrorTorrents int                               `json:"businessErrorTorrents"`
	ConnectedTorrents     int                               `json:"connectedTorrents"`
	ConnectionFailures    int                               `json:"connectionFailures"`
	ConnectedPercent      float64                           `json:"connectedPercent"`
	ErrorTorrents         int                               `json:"errorTorrents"`
	TimeoutTorrents       int                               `json:"timeoutTorrents"`
	WaitingTorrents       int                               `json:"waitingTorrents"`
	CheckedTorrents       int                               `json:"checkedTorrents"`
	Truncated             bool                              `json:"truncated"`
	AnnounceState         int                               `json:"announceState"`
	HasAnnounced          bool                              `json:"hasAnnounced"`
	LastAnnounceResult    string                            `json:"lastAnnounceResult"`
	LastAnnounceSucceeded bool                              `json:"lastAnnounceSucceeded"`
	LastAnnounceTimedOut  bool                              `json:"lastAnnounceTimedOut"`
	LastAnnounceTime      int64                             `json:"lastAnnounceTime"`
	NextAnnounceTime      int64                             `json:"nextAnnounceTime"`
	LastAnnouncePeerCount int                               `json:"lastAnnouncePeerCount"`
	SeederCount           int                               `json:"seederCount"`
	LeecherCount          int                               `json:"leecherCount"`
	Issues                []transmissionTrackerRuntimeIssue `json:"issues,omitempty"`
	Keepalive             TrackerKeepaliveRuntime           `json:"keepalive"`
	CheckedAt             string                            `json:"checkedAt"`
}

func parseTransmissionTrackerStats(body []byte, modern bool) ([]transmissionTrackerStatsRow, error) {
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
			return nil, fmt.Errorf("Transmission tracker stats JSON-RPC response: %w", err)
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
			return nil, fmt.Errorf("Transmission tracker stats response: %w", err)
		}
		if response.Result != "" && response.Result != "success" {
			return nil, fmt.Errorf("Transmission RPC: %s", response.Result)
		}
		rows = response.Arguments.Torrents
	}

	out := make([]transmissionTrackerStatsRow, 0, len(rows))
	for _, row := range rows {
		if modern {
			var v struct {
				ID           int    `json:"id"`
				HashString   string `json:"hash_string"`
				Status       int    `json:"status"`
				TrackerStats []struct {
					Announce              string `json:"announce"`
					Host                  string `json:"host"`
					AnnounceState         int    `json:"announce_state"`
					HasAnnounced          bool   `json:"has_announced"`
					LastAnnounceResult    string `json:"last_announce_result"`
					LastAnnounceSucceeded bool   `json:"last_announce_succeeded"`
					LastAnnounceTimedOut  bool   `json:"last_announce_timed_out"`
					LastAnnounceTime      int64  `json:"last_announce_time"`
					NextAnnounceTime      int64  `json:"next_announce_time"`
					LastAnnouncePeerCount int    `json:"last_announce_peer_count"`
					SeederCount           int    `json:"seeder_count"`
					LeecherCount          int    `json:"leecher_count"`
				} `json:"tracker_stats"`
			}
			if json.Unmarshal(row, &v) != nil {
				continue
			}
			item := transmissionTrackerStatsRow{ID: v.ID, HashString: strings.ToLower(v.HashString), Status: v.Status}
			for _, stat := range v.TrackerStats {
				item.TrackerStats = append(item.TrackerStats, transmissionTrackerStat{
					Announce:              stat.Announce,
					Host:                  stat.Host,
					AnnounceState:         stat.AnnounceState,
					HasAnnounced:          stat.HasAnnounced,
					LastAnnounceResult:    stat.LastAnnounceResult,
					LastAnnounceSucceeded: stat.LastAnnounceSucceeded,
					LastAnnounceTimedOut:  stat.LastAnnounceTimedOut,
					LastAnnounceTime:      stat.LastAnnounceTime,
					NextAnnounceTime:      stat.NextAnnounceTime,
					LastAnnouncePeerCount: stat.LastAnnouncePeerCount,
					SeederCount:           stat.SeederCount,
					LeecherCount:          stat.LeecherCount,
				})
			}
			out = append(out, item)
			continue
		}

		var v struct {
			ID           int    `json:"id"`
			HashString   string `json:"hashString"`
			Status       int    `json:"status"`
			TrackerStats []struct {
				Announce              string `json:"announce"`
				Host                  string `json:"host"`
				AnnounceState         int    `json:"announceState"`
				HasAnnounced          bool   `json:"hasAnnounced"`
				LastAnnounceResult    string `json:"lastAnnounceResult"`
				LastAnnounceSucceeded bool   `json:"lastAnnounceSucceeded"`
				LastAnnounceTimedOut  bool   `json:"lastAnnounceTimedOut"`
				LastAnnounceTime      int64  `json:"lastAnnounceTime"`
				NextAnnounceTime      int64  `json:"nextAnnounceTime"`
				LastAnnouncePeerCount int    `json:"lastAnnouncePeerCount"`
				SeederCount           int    `json:"seederCount"`
				LeecherCount          int    `json:"leecherCount"`
			} `json:"trackerStats"`
		}
		if json.Unmarshal(row, &v) != nil {
			continue
		}
		item := transmissionTrackerStatsRow{ID: v.ID, HashString: strings.ToLower(v.HashString), Status: v.Status}
		for _, stat := range v.TrackerStats {
			item.TrackerStats = append(item.TrackerStats, transmissionTrackerStat{
				Announce:              stat.Announce,
				Host:                  stat.Host,
				AnnounceState:         stat.AnnounceState,
				HasAnnounced:          stat.HasAnnounced,
				LastAnnounceResult:    stat.LastAnnounceResult,
				LastAnnounceSucceeded: stat.LastAnnounceSucceeded,
				LastAnnounceTimedOut:  stat.LastAnnounceTimedOut,
				LastAnnounceTime:      stat.LastAnnounceTime,
				NextAnnounceTime:      stat.NextAnnounceTime,
				LastAnnouncePeerCount: stat.LastAnnouncePeerCount,
				SeederCount:           stat.SeederCount,
				LeecherCount:          stat.LeecherCount,
			})
		}
		out = append(out, item)
	}
	return out, nil
}


func transmissionTrackerStatusLabel(stat transmissionTrackerStat) string {
	if stat.LastAnnounceSucceeded {
		return "Working"
	}
	if stat.LastAnnounceTimedOut {
		return "Timeout"
	}
	if stat.HasAnnounced {
		reason := sanitizeTrackerReason([]byte(stat.LastAnnounceResult))
		if trackerFailureIndicatesUnreachable(reason) {
			return "Disconnected"
		}
		return "ConnectedError"
	}
	return "Waiting"
}

func selectedTrackerStatForDomain(row transmissionTrackerStatsRow, target string) *transmissionTrackerStat {
	var selected *transmissionTrackerStat
	for i := range row.TrackerStats {
		stat := row.TrackerStats[i]
		if transmissionTrackerStatDomain(stat) != target {
			continue
		}
		if selected == nil || stat.LastAnnounceTime >= selected.LastAnnounceTime {
			copyStat := stat
			selected = &copyStat
		}
	}
	return selected
}

func transmissionDomainHealthForTarget(rows []transmissionTrackerStatsRow, target string) transmissionDomainHealth {
	target = strings.ToLower(strings.TrimSpace(target))
	out := transmissionDomainHealth{}
	seenIDs := map[int]bool{}
	for _, row := range rows {
		if row.Status != 5 && row.Status != 6 {
			continue
		}
		stat := selectedTrackerStatForDomain(row, target)
		if stat == nil {
			continue
		}
		out.Matched++
		switch transmissionTrackerStatusLabel(*stat) {
		case "Working":
			out.Evaluated++
			out.Connected++
			out.Working++
		case "ConnectedError":
			out.Evaluated++
			out.Connected++
			out.BusinessErrors++
		case "Timeout", "Disconnected":
			out.Evaluated++
			out.ConnectionFailures++
			if row.ID > 0 && !seenIDs[row.ID] {
				out.FailureTorrentIDs = append(out.FailureTorrentIDs, row.ID)
				seenIDs[row.ID] = true
			}
			reason := sanitizeTrackerReason([]byte(stat.LastAnnounceResult))
			if reason == "" {
				reason = "tracker announce timed out"
			}
			if stat.LastAnnounceTime >= out.LatestFailure.LastAnnounceTime {
				out.LatestFailure = downloaderTrackerFailure{
					Source:           "Transmission",
					Detail:           "Transmission · " + reason,
					LastAnnounceTime: stat.LastAnnounceTime,
				}
			}
		default:
			out.Waiting++
		}
	}
	if out.Evaluated > 0 {
		out.ConnectedPercent = float64(out.Connected) * 100 / float64(out.Evaluated)
	}
	sort.Ints(out.FailureTorrentIDs)
	return out
}

func trackerDomainHealthAcceptable(h transmissionDomainHealth) bool {
	if h.ConnectionFailures == 0 {
		return true
	}
	if h.Evaluated == 0 {
		return true
	}
	return h.ConnectedPercent >= trackerKeepaliveMinConnectedPct && h.ConnectionFailures <= trackerKeepaliveMaxFailures
}

func aggregateTransmissionTrackerRuntime(rows []transmissionTrackerStatsRow, target string, activeTorrents, checkedTorrents int, truncated bool) transmissionTrackerRuntime {
	out := transmissionTrackerRuntime{
		Available:       true,
		Source:          "Transmission",
		ActiveTorrents:  activeTorrents,
		CheckedTorrents: checkedTorrents,
		Truncated:       truncated,
		CheckedAt:       time.Now().Format(time.RFC3339),
	}
	target = strings.ToLower(strings.TrimSpace(target))
	health := transmissionDomainHealthForTarget(rows, target)
	out.MatchedTorrents = health.Matched
	out.WorkingTorrents = health.Working
	out.BusinessErrorTorrents = health.BusinessErrors
	out.ConnectedTorrents = health.Connected
	out.ConnectionFailures = health.ConnectionFailures
	out.ConnectedPercent = health.ConnectedPercent
	out.WaitingTorrents = health.Waiting

	var latest *transmissionTrackerStat
	issues := make([]transmissionTrackerRuntimeIssue, 0)
	for _, row := range rows {
		if row.Status != 5 && row.Status != 6 {
			continue
		}
		selected := selectedTrackerStatForDomain(row, target)
		if selected == nil {
			continue
		}
		if row.Status == 6 {
			out.SeedingTorrents++
		} else {
			out.QueuedTorrents++
		}

		label := transmissionTrackerStatusLabel(*selected)
		if label == "Timeout" || label == "Disconnected" {
			if label == "Timeout" {
				out.TimeoutTorrents++
			} else {
				out.ErrorTorrents++
			}
			reason := sanitizeTrackerReason([]byte(selected.LastAnnounceResult))
			if reason == "" {
				reason = "Tracker announce timed out"
			}
			issues = append(issues, transmissionTrackerRuntimeIssue{
				Result:           reason,
				LastAnnounceTime: selected.LastAnnounceTime,
				TorrentStatus:    row.Status,
				TimedOut:         label == "Timeout",
			})
		}
		if latest == nil || selected.LastAnnounceTime >= latest.LastAnnounceTime {
			copyStat := *selected
			latest = &copyStat
			out.TorrentStatus = row.Status
		}
	}

	out.Seeding = out.SeedingTorrents > 0
	switch {
	case out.MatchedTorrents == 0:
		out.TrackerStatus = "NoActive"
	case out.ConnectionFailures > 0 && out.ConnectedTorrents > 0:
		out.TrackerStatus = "Partial"
	case out.TimeoutTorrents > 0 && out.ErrorTorrents == 0 && out.ConnectedTorrents == 0:
		out.TrackerStatus = "Timeout"
	case out.ConnectionFailures > 0:
		out.TrackerStatus = "Error"
	case out.BusinessErrorTorrents > 0:
		out.TrackerStatus = "Connected"
	case out.WorkingTorrents > 0:
		out.TrackerStatus = "Working"
	default:
		out.TrackerStatus = "Waiting"
	}

	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].LastAnnounceTime > issues[j].LastAnnounceTime
	})
	if len(issues) > 5 {
		issues = issues[:5]
	}
	out.Issues = issues

	if latest != nil {
		out.AnnounceState = latest.AnnounceState
		out.HasAnnounced = latest.HasAnnounced
		out.LastAnnounceResult = sanitizeTrackerReason([]byte(latest.LastAnnounceResult))
		out.LastAnnounceSucceeded = out.ConnectionFailures == 0 && out.ConnectedTorrents > 0
		out.LastAnnounceTimedOut = out.TimeoutTorrents > 0
		out.LastAnnounceTime = latest.LastAnnounceTime
		out.NextAnnounceTime = latest.NextAnnounceTime
		out.LastAnnouncePeerCount = latest.LastAnnouncePeerCount
		out.SeederCount = latest.SeederCount
		out.LeecherCount = latest.LeecherCount
	}
	if len(out.Issues) > 0 {
		out.LastAnnounceResult = out.Issues[0].Result
		out.LastAnnounceTime = out.Issues[0].LastAnnounceTime
	}
	return out
}

func transmissionActiveTrackerStats(ctx context.Context, cfg DownloaderClientConfig, maxTrackerLookups int) ([]transmissionTrackerStatsRow, int, int, bool, error) {
	if !cfg.Enabled {
		return nil, 0, 0, false, fmt.Errorf("Transmission is disabled")
	}
	endpoint, err := normalizeTransmissionURL(cfg.URL)
	if err != nil {
		return nil, 0, 0, false, err
	}
	rpc := &transmissionRPCClient{
		endpoint: endpoint,
		cfg:      cfg,
		client:   &http.Client{},
	}
	body, err := rpc.call(
		ctx,
		"torrent-get",
		"torrent_get",
		map[string]any{"fields": []string{"id", "hashString", "leftUntilDone", "isFinished", "status", "activityDate"}},
		map[string]any{"fields": []string{"id", "hash_string", "left_until_done", "is_finished", "status", "activity_date"}},
	)
	if err != nil {
		return nil, 0, 0, false, err
	}
	torrents, err := parseTransmissionTorrentList(body, rpc.modern)
	if err != nil {
		return nil, 0, 0, false, err
	}

	active := make([]transmissionTorrentSummary, 0)
	for _, torrent := range torrents {
		if torrent.Status == 5 || torrent.Status == 6 {
			active = append(active, torrent)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		iSeed := active[i].Status == 6
		jSeed := active[j].Status == 6
		if iSeed != jSeed {
			return iSeed
		}
		return active[i].ActivityDate > active[j].ActivityDate
	})

	totalActive := len(active)
	limit := totalActive
	truncated := false
	if maxTrackerLookups > 0 && limit > maxTrackerLookups {
		limit = maxTrackerLookups
		truncated = true
	}
	active = active[:limit]

	const batchSize = 16
	rows := make([]transmissionTrackerStatsRow, 0, len(active))
	for offset := 0; offset < len(active); offset += batchSize {
		end := offset + batchSize
		if end > len(active) {
			end = len(active)
		}
		ids := make([]int, 0, end-offset)
		for _, torrent := range active[offset:end] {
			ids = append(ids, torrent.ID)
		}
		body, err := rpc.call(
			ctx,
			"torrent-get",
			"torrent_get",
			map[string]any{
				"ids":    ids,
				"fields": []string{"id", "hashString", "status", "trackerStats"},
			},
			map[string]any{
				"ids":    ids,
				"fields": []string{"id", "hash_string", "status", "tracker_stats"},
			},
		)
		if err != nil {
			return nil, totalActive, len(active), truncated, err
		}
		batchRows, err := parseTransmissionTrackerStats(body, rpc.modern)
		if err != nil {
			return nil, totalActive, len(active), truncated, err
		}
		rows = append(rows, batchRows...)
	}
	return rows, totalActive, len(active), truncated, nil
}

func transmissionTrackerRuntimeForDomain(ctx context.Context, cfg DownloaderClientConfig, domain string, maxTrackerLookups int) (transmissionTrackerRuntime, error) {
	out := transmissionTrackerRuntime{Source: "Transmission", CheckedAt: time.Now().Format(time.RFC3339)}
	rows, totalActive, checked, truncated, err := transmissionActiveTrackerStats(ctx, cfg, maxTrackerLookups)
	if err != nil {
		return out, err
	}
	return aggregateTransmissionTrackerRuntime(rows, domain, totalActive, checked, truncated), nil
}

func transmissionTrackerStatDomain(stat transmissionTrackerStat) string {
	if parsed, err := url.Parse(strings.TrimSpace(stat.Announce)); err == nil {
		if host := strings.ToLower(parsed.Hostname()); host != "" {
			return host
		}
	}
	host := strings.ToLower(strings.TrimSpace(stat.Host))
	if colon := strings.IndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	return host
}

func parseTransmissionActionResult(body []byte, modern bool) error {
	if modern {
		var response struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return fmt.Errorf("Transmission action JSON-RPC response: %w", err)
		}
		if response.Error != nil {
			return fmt.Errorf("Transmission JSON-RPC: %s", response.Error.Message)
		}
		return nil
	}
	var response struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("Transmission action response: %w", err)
	}
	if response.Result != "" && response.Result != "success" {
		return fmt.Errorf("Transmission RPC: %s", response.Result)
	}
	return nil
}

func transmissionReannounce(ctx context.Context, cfg DownloaderClientConfig, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	endpoint, err := normalizeTransmissionURL(cfg.URL)
	if err != nil {
		return err
	}
	uniq := make([]int, 0, len(ids))
	seen := map[int]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return nil
	}
	sort.Ints(uniq)
	rpc := &transmissionRPCClient{
		endpoint: endpoint,
		cfg:      cfg,
		client:   &http.Client{},
	}
	body, err := rpc.call(
		ctx,
		"torrent-reannounce",
		"torrent_reannounce",
		map[string]any{"ids": uniq},
		map[string]any{"ids": uniq},
	)
	if err != nil {
		return err
	}
	return parseTransmissionActionResult(body, rpc.modern)
}

func (a *App) trackerSamplePassInfo(domain string) (bool, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	sample, ok := a.state.TrackerSamples[domain]
	if !ok {
		return false, ""
	}
	return sample.Tested && sample.Passed, sample.LastTest
}

func copyTrackerKeepalive(in map[string]TrackerKeepaliveRuntime) map[string]TrackerKeepaliveRuntime {
	out := make(map[string]TrackerKeepaliveRuntime, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func trackerKeepaliveNextDue(state TrackerKeepaliveRuntime, now time.Time) bool {
	if strings.TrimSpace(state.NextCheck) == "" {
		return true
	}
	next, err := time.Parse(time.RFC3339, state.NextCheck)
	return err != nil || !now.Before(next)
}

func (a *App) evaluateTransmissionTrackerKeepalive(ctx context.Context, cfg Config, samples map[string]TrackerSample) map[string]downloaderTrackerFailure {
	out := make(map[string]downloaderTrackerFailure)
	if !cfg.Tracker.RealAnnounce || !cfg.Tracker.Transmission.Enabled || len(samples) == 0 {
		return out
	}

	// Runtime health is deliberately uncapped. maxTrackerLookups=200 remains
	// limited to sample discovery; health/keepalive must see every active seed.
	timeout := 60 * time.Second
	if configured := time.Duration(cfg.Tracker.DiscoveryTimeoutSeconds) * time.Second; configured > timeout {
		timeout = configured
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rows, totalActive, checked, _, err := transmissionActiveTrackerStats(probeCtx, cfg.Tracker.Transmission, 0)
	if err != nil {
		a.appendLog("Transmission tracker health warning: %v", err)
		return out
	}
	a.appendLog("Transmission tracker health: scanned %d/%d active seeding torrents", checked, totalActive)

	a.mu.RLock()
	states := copyTrackerKeepalive(a.state.TrackerKeepalive)
	a.mu.RUnlock()

	now := time.Now()
	targets := make(map[string]bool, len(samples))
	dueDomains := make([]string, 0)
	dueTorrentIDs := make([]int, 0)
	healthByDomain := make(map[string]transmissionDomainHealth, len(samples))
	samplePassedByDomain := make(map[string]bool, len(samples))

	for rawDomain := range samples {
		domain := strings.ToLower(strings.TrimSpace(rawDomain))
		if domain == "" {
			continue
		}
		targets[domain] = true
		health := transmissionDomainHealthForTarget(rows, domain)
		healthByDomain[domain] = health
		samplePassed, sampleTestAt := a.trackerSamplePassInfo(domain)
		samplePassedByDomain[domain] = samplePassed

		state := states[domain]
		if state.SampleTestAt != sampleTestAt {
			state = TrackerKeepaliveRuntime{SampleTestAt: sampleTestAt}
		}
		state.MatchedTorrents = health.Matched
		state.EvaluatedTorrents = health.Evaluated
		state.ConnectedTorrents = health.Connected
		state.ConnectionFailures = health.ConnectionFailures
		state.ConnectedPercent = health.ConnectedPercent

		if health.Matched == 0 || health.Evaluated == 0 {
			state.Attempts = 0
			state.Status = "idle"
			state.NextCheck = ""
			state.LastEvent = "no announced active torrents for this Tracker"
			states[domain] = state
			continue
		}

		if trackerDomainHealthAcceptable(health) {
			state.Attempts = 0
			state.NextCheck = ""
			if health.ConnectionFailures > 0 {
				state.Status = "acceptable"
				state.LastEvent = fmt.Sprintf("acceptable residual failures · connected %.1f%% · failures %d", health.ConnectedPercent, health.ConnectionFailures)
			} else {
				state.Status = "healthy"
				state.LastEvent = fmt.Sprintf("connected %.1f%% · no connection failures", health.ConnectedPercent)
			}
			states[domain] = state
			continue
		}

		if !samplePassed {
			state.Status = "repair"
			state.NextCheck = ""
			state.LastEvent = fmt.Sprintf("sample not confirmed healthy · connected %.1f%% · failures %d", health.ConnectedPercent, health.ConnectionFailures)
			states[domain] = state
			failure := health.LatestFailure
			if failure.Detail == "" {
				failure = downloaderTrackerFailure{
					Source:           "Transmission",
					Detail:           fmt.Sprintf("Transmission · connection health below threshold · %.1f%% connected · %d failures", health.ConnectedPercent, health.ConnectionFailures),
					LastAnnounceTime: now.Unix(),
				}
			}
			out[domain] = failure
			continue
		}

		if state.Attempts >= trackerKeepaliveMaxAttempts {
			if !trackerKeepaliveNextDue(state, now) {
				state.Status = "waiting-after-third"
				state.LastEvent = fmt.Sprintf("waiting for third reannounce result · connected %.1f%% · failures %d", health.ConnectedPercent, health.ConnectionFailures)
				states[domain] = state
				continue
			}
			state.Status = "repair"
			state.LastEvent = fmt.Sprintf("keepalive exhausted · connected %.1f%% · failures %d", health.ConnectedPercent, health.ConnectionFailures)
			states[domain] = state
			out[domain] = downloaderTrackerFailure{
				Source:           "Transmission keepalive",
				Detail:           fmt.Sprintf("Transmission keepalive exhausted after %d reannounce attempts · %.1f%% connected · %d connection failures", trackerKeepaliveMaxAttempts, health.ConnectedPercent, health.ConnectionFailures),
				LastAnnounceTime: now.Unix(),
			}
			continue
		}

		if !trackerKeepaliveNextDue(state, now) {
			state.Status = "waiting"
			state.LastEvent = fmt.Sprintf("waiting for next low-frequency reannounce · connected %.1f%% · failures %d", health.ConnectedPercent, health.ConnectionFailures)
			states[domain] = state
			continue
		}

		if len(health.FailureTorrentIDs) > 0 {
			dueDomains = append(dueDomains, domain)
			dueTorrentIDs = append(dueTorrentIDs, health.FailureTorrentIDs...)
			state.Status = "reannounce-due"
			state.LastEvent = fmt.Sprintf("sample passed; %d connection-failed torrents are due for reannounce", len(health.FailureTorrentIDs))
			states[domain] = state
		}
	}

	if len(dueDomains) > 0 {
		reannounceCtx, reannounceCancel := context.WithTimeout(ctx, 20*time.Second)
		err := transmissionReannounce(reannounceCtx, cfg.Tracker.Transmission, dueTorrentIDs)
		reannounceCancel()
		stamp := now.Format(time.RFC3339)
		next := now.Add(trackerKeepaliveInterval).Format(time.RFC3339)
		for _, domain := range dueDomains {
			state := states[domain]
			state.LastReannounce = stamp
			state.NextCheck = next
			if state.StartedAt == "" {
				state.StartedAt = stamp
			}
			health := healthByDomain[domain]
			if err != nil {
				state.Status = "reannounce-error"
				state.LastEvent = "Transmission reannounce RPC failed · " + err.Error()
				a.appendLog("tracker keepalive %s: reannounce RPC failed: %v", domain, err)
			} else {
				state.Attempts++
				state.Status = "waiting"
				state.LastEvent = fmt.Sprintf("reannounce %d/%d sent for %d connection-failed torrents; next check after %s", state.Attempts, trackerKeepaliveMaxAttempts, len(health.FailureTorrentIDs), next)
				a.appendLog("tracker keepalive %s: torrent-reannounce %d/%d sent for %d torrent(s); next check %s", domain, state.Attempts, trackerKeepaliveMaxAttempts, len(health.FailureTorrentIDs), next)
			}
			states[domain] = state
		}
	}

	a.mu.Lock()
	if a.state.TrackerKeepalive == nil {
		a.state.TrackerKeepalive = map[string]TrackerKeepaliveRuntime{}
	}
	for domain := range a.state.TrackerKeepalive {
		if !targets[domain] {
			delete(a.state.TrackerKeepalive, domain)
		}
	}
	for domain, state := range states {
		if targets[domain] {
			a.state.TrackerKeepalive[domain] = state
		}
	}
	a.mu.Unlock()

	if len(out) > 0 {
		a.appendLog("Transmission tracker health: %d domain(s) crossed keepalive threshold and require Repair", len(out))
	}
	return out
}

// downloaderFailureIsNewer prevents a stale Transmission error from repeatedly
// invalidating a mapping that CFHost has already repaired since that announce.
// DomainHealth.LastSuccess is refreshed when the current mapping is retained or
// a replacement is committed.
func downloaderFailureIsNewer(failure downloaderTrackerFailure, lastSuccess string) bool {
	if failure.LastAnnounceTime <= 0 {
		return strings.TrimSpace(lastSuccess) == ""
	}
	if strings.TrimSpace(lastSuccess) == "" {
		return true
	}
	successAt, err := time.Parse(time.RFC3339, lastSuccess)
	if err != nil {
		return true
	}
	return failure.LastAnnounceTime > successAt.Unix()
}

func (a *App) loadDownloaderTrackerFailures(ctx context.Context, cfg Config, samples map[string]TrackerSample) map[string]downloaderTrackerFailure {
	return a.evaluateTransmissionTrackerKeepalive(ctx, cfg, samples)
}
