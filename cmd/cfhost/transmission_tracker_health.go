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
	HasAnnounced          bool
	LastAnnounceResult    string
	LastAnnounceSucceeded bool
	LastAnnounceTimedOut  bool
	LastAnnounceTime      int64
}

type transmissionTrackerStatsRow struct {
	HashString   string
	TrackerStats []transmissionTrackerStat
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
				TrackerStats []struct {
					Announce              string `json:"announce"`
					Host                  string `json:"host"`
					HasAnnounced          bool   `json:"has_announced"`
					LastAnnounceResult    string `json:"last_announce_result"`
					LastAnnounceSucceeded bool   `json:"last_announce_succeeded"`
					LastAnnounceTimedOut  bool   `json:"last_announce_timed_out"`
					LastAnnounceTime      int64  `json:"last_announce_time"`
				} `json:"tracker_stats"`
			}
			if json.Unmarshal(row, &v) != nil {
				continue
			}
			item := transmissionTrackerStatsRow{HashString: strings.ToLower(v.HashString)}
			for _, stat := range v.TrackerStats {
				item.TrackerStats = append(item.TrackerStats, transmissionTrackerStat{
					Announce:              stat.Announce,
					Host:                  stat.Host,
					HasAnnounced:          stat.HasAnnounced,
					LastAnnounceResult:    stat.LastAnnounceResult,
					LastAnnounceSucceeded: stat.LastAnnounceSucceeded,
					LastAnnounceTimedOut:  stat.LastAnnounceTimedOut,
					LastAnnounceTime:      stat.LastAnnounceTime,
				})
			}
			out = append(out, item)
			continue
		}

		var v struct {
			HashString   string `json:"hashString"`
			TrackerStats []struct {
				Announce              string `json:"announce"`
				Host                  string `json:"host"`
				HasAnnounced          bool   `json:"hasAnnounced"`
				LastAnnounceResult    string `json:"lastAnnounceResult"`
				LastAnnounceSucceeded bool   `json:"lastAnnounceSucceeded"`
				LastAnnounceTimedOut  bool   `json:"lastAnnounceTimedOut"`
				LastAnnounceTime      int64  `json:"lastAnnounceTime"`
			} `json:"trackerStats"`
		}
		if json.Unmarshal(row, &v) != nil {
			continue
		}
		item := transmissionTrackerStatsRow{HashString: strings.ToLower(v.HashString)}
		for _, stat := range v.TrackerStats {
			item.TrackerStats = append(item.TrackerStats, transmissionTrackerStat{
				Announce:              stat.Announce,
				Host:                  stat.Host,
				HasAnnounced:          stat.HasAnnounced,
				LastAnnounceResult:    stat.LastAnnounceResult,
				LastAnnounceSucceeded: stat.LastAnnounceSucceeded,
				LastAnnounceTimedOut:  stat.LastAnnounceTimedOut,
				LastAnnounceTime:      stat.LastAnnounceTime,
			})
		}
		out = append(out, item)
	}
	return out, nil
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

func loadDownloaderTrackerFailures(ctx context.Context, cfg Config, samples map[string]TrackerSample) map[string]downloaderTrackerFailure {
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
		return out
	}
	return failures
}
