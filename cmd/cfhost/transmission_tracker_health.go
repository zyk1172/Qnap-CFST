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
	HashString   string
	Status       int
	TrackerStats []transmissionTrackerStat
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
	Issues                []transmissionTrackerRuntimeIssue `json:"issues,omitempty"`
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
			item := transmissionTrackerStatsRow{HashString: strings.ToLower(v.HashString), Status: v.Status}
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
		item := transmissionTrackerStatsRow{HashString: strings.ToLower(v.HashString), Status: v.Status}
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
		return "Error"
	}
	return "Waiting"
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

	var latest *transmissionTrackerStat
	issues := make([]transmissionTrackerRuntimeIssue, 0)
	for _, row := range rows {
		if row.Status != 5 && row.Status != 6 {
			continue
		}
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
		if selected == nil {
			continue
		}

		out.MatchedTorrents++
		if row.Status == 6 {
			out.SeedingTorrents++
		} else {
			out.QueuedTorrents++
		}

		label := transmissionTrackerStatusLabel(*selected)
		switch label {
		case "Working":
			out.WorkingTorrents++
		case "Timeout":
			out.TimeoutTorrents++
		case "Error":
			out.ErrorTorrents++
		default:
			out.WaitingTorrents++
		}
		reason := sanitizeTrackerReason([]byte(selected.LastAnnounceResult))
		if label == "Timeout" || label == "Error" {
			if reason == "" {
				if label == "Timeout" {
					reason = "Tracker announce timed out"
				} else {
					reason = "Tracker announce failed"
				}
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
	case out.ErrorTorrents+out.TimeoutTorrents > 0 && out.WorkingTorrents > 0:
		out.TrackerStatus = "Partial"
	case out.TimeoutTorrents > 0 && out.ErrorTorrents == 0 && out.WorkingTorrents == 0:
		out.TrackerStatus = "Timeout"
	case out.ErrorTorrents+out.TimeoutTorrents > 0:
		out.TrackerStatus = "Error"
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
		out.LastAnnounceSucceeded = out.ErrorTorrents == 0 && out.TimeoutTorrents == 0 && out.WorkingTorrents > 0
		out.LastAnnounceTimedOut = out.TimeoutTorrents > 0
		out.LastAnnounceTime = latest.LastAnnounceTime
		out.NextAnnounceTime = latest.NextAnnounceTime
	}
	if len(out.Issues) > 0 {
		out.LastAnnounceResult = out.Issues[0].Result
		out.LastAnnounceTime = out.Issues[0].LastAnnounceTime
	}
	return out
}

func transmissionTrackerRuntimeForDomain(ctx context.Context, cfg DownloaderClientConfig, domain string, maxTrackerLookups int) (transmissionTrackerRuntime, error) {
	out := transmissionTrackerRuntime{Source: "Transmission", CheckedAt: time.Now().Format(time.RFC3339)}
	if !cfg.Enabled {
		return out, fmt.Errorf("Transmission is disabled")
	}
	endpoint, err := normalizeTransmissionURL(cfg.URL)
	if err != nil {
		return out, err
	}
	if maxTrackerLookups < 1 {
		maxTrackerLookups = 200
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
		return out, err
	}
	torrents, err := parseTransmissionTorrentList(body, rpc.modern)
	if err != nil {
		return out, err
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
	if limit > maxTrackerLookups {
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
				"fields": []string{"hashString", "status", "trackerStats"},
			},
			map[string]any{
				"ids":    ids,
				"fields": []string{"hash_string", "status", "tracker_stats"},
			},
		)
		if err != nil {
			return out, err
		}
		batchRows, err := parseTransmissionTrackerStats(body, rpc.modern)
		if err != nil {
			return out, err
		}
		rows = append(rows, batchRows...)
	}

	return aggregateTransmissionTrackerRuntime(rows, domain, totalActive, len(active), truncated), nil
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

func transmissionTrackerConnectionFailures(ctx context.Context, cfg DownloaderClientConfig, samples map[string]TrackerSample) (map[string]downloaderTrackerFailure, error) {
	out := make(map[string]downloaderTrackerFailure)
	if !cfg.Enabled || len(samples) == 0 {
		return out, nil
	}
	endpoint, err := normalizeTransmissionURL(cfg.URL)
	if err != nil {
		return out, err
	}

	hashes := make([]string, 0, len(samples))
	targetByHash := make(map[string]map[string]bool)
	for domain, sample := range samples {
		hash := strings.ToLower(strings.TrimSpace(sample.HashHex))
		if len(hash) != 40 {
			continue
		}
		if targetByHash[hash] == nil {
			targetByHash[hash] = make(map[string]bool)
			hashes = append(hashes, hash)
		}
		targetByHash[hash][strings.ToLower(strings.TrimSpace(domain))] = true
	}
	if len(hashes) == 0 {
		return out, nil
	}
	sort.Strings(hashes)

	rpc := &transmissionRPCClient{
		endpoint: endpoint,
		cfg:      cfg,
		client:   &http.Client{},
	}
	body, err := rpc.call(
		ctx,
		"torrent-get",
		"torrent_get",
		map[string]any{
			"ids":    hashes,
			"fields": []string{"hashString", "trackerStats"},
		},
		map[string]any{
			"ids":    hashes,
			"fields": []string{"hash_string", "tracker_stats"},
		},
	)
	if err != nil {
		return out, err
	}
	rows, err := parseTransmissionTrackerStats(body, rpc.modern)
	if err != nil {
		return out, err
	}

	for _, row := range rows {
		targets := targetByHash[strings.ToLower(row.HashString)]
		if len(targets) == 0 {
			continue
		}
		for _, stat := range row.TrackerStats {
			domain := transmissionTrackerStatDomain(stat)
			if !targets[domain] || !stat.HasAnnounced {
				continue
			}

			reason := sanitizeTrackerReason([]byte(stat.LastAnnounceResult))
			connectionFailed := stat.LastAnnounceTimedOut || trackerFailureIndicatesUnreachable(reason)
			if !connectionFailed {
				continue
			}
			if reason == "" {
				reason = "tracker announce timed out"
			}
			detail := "Transmission · " + reason
			old, exists := out[domain]
			if !exists || stat.LastAnnounceTime >= old.LastAnnounceTime {
				out[domain] = downloaderTrackerFailure{
					Source:           "Transmission",
					Detail:           detail,
					LastAnnounceTime: stat.LastAnnounceTime,
				}
			}
		}
	}
	return out, nil
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
	out := make(map[string]downloaderTrackerFailure)
	if !cfg.Tracker.RealAnnounce || !cfg.Tracker.Transmission.Enabled {
		return out
	}
	timeout := time.Duration(cfg.Tracker.DiscoveryTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failures, err := transmissionTrackerConnectionFailures(probeCtx, cfg.Tracker.Transmission, samples)
	if err != nil {
		a.appendLog("Transmission tracker health warning: %v", err)
		return out
	}
	if len(failures) > 0 {
		a.appendLog("Transmission tracker health: %d domain(s) report connection failure", len(failures))
	}
	return failures
}
