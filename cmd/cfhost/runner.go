package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Candidate struct {
	IP         string  `json:"ip"`
	LossRate   float64 `json:"lossRate"`
	DelayMS    float64 `json:"delayMs"`
	SpeedMB    float64 `json:"speedMB"`
	Colo       string  `json:"colo"`
	ObservedAt string  `json:"observedAt"`
}

type cfstRateLimitSignal struct {
	StatusCode int
	RetryAfter string
}

func parseCFSTRateLimitSignal(output string) (cfstRateLimitSignal, bool) {
	for _, line := range strings.Split(output, "\n") {
		marker := strings.Index(line, "CFST_RATE_LIMIT")
		if marker < 0 {
			continue
		}
		rest := strings.TrimSpace(line[marker+len("CFST_RATE_LIMIT"):])
		statusPos := strings.Index(rest, "status=")
		retryPos := strings.Index(rest, "retry_after=")
		if statusPos < 0 || retryPos < 0 {
			continue
		}
		statusText := strings.TrimSpace(rest[statusPos+len("status="):retryPos])
		status, err := strconv.Atoi(statusText)
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(rest[retryPos+len("retry_after="):])
		retryAfter, err := strconv.Unquote(raw)
		if err != nil {
			retryAfter = strings.Trim(raw, "\"")
		}
		return cfstRateLimitSignal{StatusCode: status, RetryAfter: retryAfter}, true
	}
	return cfstRateLimitSignal{}, false
}

func retryAfterDeadline(now time.Time, raw string, fallback time.Duration) (time.Time, int) {
	raw = strings.TrimSpace(raw)
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second), seconds
	}
	if at, err := http.ParseTime(raw); err == nil {
		if at.Before(now) {
			return now, 0
		}
		d := at.Sub(now)
		seconds := int((d + time.Second - 1) / time.Second)
		return at, seconds
	}
	if fallback <= 0 {
		fallback = 15 * time.Minute
	}
	return now.Add(fallback), int(fallback / time.Second)
}

func (a *App) rateLimitMode(now time.Time, cfg Config) (bool, time.Time) {
	if !cfg.CFST.AdaptiveRateLimit {
		return false, time.Time{}
	}
	a.mu.RLock()
	state := a.state.CFSTRateLimit
	a.mu.RUnlock()
	if !state.Active {
		return false, time.Time{}
	}
	until, err := time.Parse(time.RFC3339, state.Until)
	if err != nil || until.IsZero() || !now.Before(until) {
		a.mu.Lock()
		a.state.CFSTRateLimit.Active = false
		a.state.CFSTRateLimit.Mode = "normal"
		a.state.CFSTRateLimit.LastEvent = "rate-limit cooldown expired; normal CFST allowed"
		a.mu.Unlock()
		return false, time.Time{}
	}
	return true, until
}

func (a *App) recordCFSTRateLimit(now time.Time, cfg Config, signal cfstRateLimitSignal) time.Time {
	fallback := time.Duration(cfg.CFST.RateLimitFallbackMinutes) * time.Minute
	until, seconds := retryAfterDeadline(now, signal.RetryAfter, fallback)
	a.mu.Lock()
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{
		Active:            true,
		Mode:              "degraded",
		StatusCode:        signal.StatusCode,
		RetryAfterSeconds: seconds,
		Until:             until.Format(time.RFC3339),
		DetectedAt:        now.Format(time.RFC3339),
		LastEvent:         fmt.Sprintf("HTTP %d rate limit; degraded CFST until %s", signal.StatusCode, until.Format(time.RFC3339)),
	}
	a.mu.Unlock()
	return until
}

func (a *App) clearCFSTRateLimit(event string) {
	a.mu.Lock()
	previous := a.state.CFSTRateLimit
	a.state.CFSTRateLimit = CFSTRateLimitRuntime{Mode: "normal", LastEvent: event}
	if previous.DetectedAt != "" && event == "" {
		a.state.CFSTRateLimit.LastEvent = "normal CFST restored"
	}
	a.mu.Unlock()
}

func minPositive(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func buildCFSTArgs(cfg Config, ipFile, resultPath string, degraded bool) []string {
	downloadCount := cfg.CFST.DownloadCount
	downloadSeconds := cfg.CFST.DownloadSeconds
	minSpeed := cfg.CFST.MinSpeedMB
	args := []string{
		"-n", strconv.Itoa(cfg.CFST.Threads),
		"-t", strconv.Itoa(cfg.CFST.PingTimes),
	}
	if degraded {
		downloadCount = minPositive(cfg.CFST.DownloadCount, cfg.CFST.DegradedDownloadCount)
		downloadSeconds = cfg.CFST.DegradedDownloadSeconds
		minSpeed = 0
	}
	args = append(args,
		"-dn", strconv.Itoa(downloadCount),
		"-dt", strconv.Itoa(downloadSeconds),
		"-tl", strconv.Itoa(cfg.CFST.MaxDelayMS),
		"-tlr", strconv.FormatFloat(cfg.CFST.MaxLossRate, 'f', -1, 64),
		"-sl", strconv.FormatFloat(minSpeed, 'f', -1, 64),
		"-p", "0",
		"-f", ipFile,
		"-o", resultPath,
	)
	if degraded {
		args = append(args, "-dr", strconv.FormatFloat(cfg.CFST.DegradedRateMbps, 'f', -1, 64))
	}
	if strings.TrimSpace(cfg.CFST.DownloadURL) != "" {
		args = append(args, "-url", cfg.CFST.DownloadURL)
	}
	return args
}

func (a *App) runCFST(ctx context.Context, cfg Config) ([]Candidate, error) {
	now := time.Now()
	degraded, until := a.rateLimitMode(now, cfg)
	if degraded {
		a.appendLog("CFST rate-limit cooldown active until %s; using degraded probe (%.2f Mbps cap)", until.Format(time.RFC3339), cfg.CFST.DegradedRateMbps)
	}

	candidates, signal, err := a.runCFSTMode(ctx, cfg, degraded)
	if signal != nil && cfg.CFST.AdaptiveRateLimit {
		until = a.recordCFSTRateLimit(time.Now(), cfg, *signal)
		a.appendLog("CFST rate limit detected: HTTP %d · retry-after=%q · until=%s", signal.StatusCode, signal.RetryAfter, until.Format(time.RFC3339))
		if !degraded {
			a.appendLog("CFST switching immediately to degraded probe: count=%d seconds=%d cap=%.2f Mbps", cfg.CFST.DegradedDownloadCount, cfg.CFST.DegradedDownloadSeconds, cfg.CFST.DegradedRateMbps)
			fallbackCandidates, fallbackSignal, fallbackErr := a.runCFSTMode(ctx, cfg, true)
			if fallbackSignal != nil {
				until = a.recordCFSTRateLimit(time.Now(), cfg, *fallbackSignal)
				a.appendLog("CFST degraded probe also reported rate limit until %s", until.Format(time.RFC3339))
			}
			if fallbackErr != nil {
				return nil, fallbackErr
			}
			return fallbackCandidates, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if !degraded {
		a.clearCFSTRateLimit("normal CFST succeeded")
	}
	return candidates, nil
}

func (a *App) runCFSTMode(ctx context.Context, cfg Config, degraded bool) ([]Candidate, *cfstRateLimitSignal, error) {
	resultPath := filepath.Join(a.dataDir, "result.csv")
	_ = os.Remove(resultPath)

	ipFile := getenv("CFST_IP_FILE", "/app/ip.txt")
	if cfg.CFST.IPv6 {
		ipFile = getenv("CFST_IPV6_FILE", "/app/ipv6.txt")
	}
	args := buildCFSTArgs(cfg, ipFile, resultPath, degraded)
	mode := "normal"
	if degraded {
		mode = "degraded"
	}
	a.appendLog("CFST mode=%s: %s", mode, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, a.cfstBin, args...)
	cmd.Dir = a.dataDir
	out, cmdErr := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			a.appendLog("cfst | %s", line)
		}
	}
	var signal *cfstRateLimitSignal
	if detected, ok := parseCFSTRateLimitSignal(output); ok {
		signal = &detected
	}
	if degraded && signal != nil {
		return nil, signal, fmt.Errorf("CFST degraded probe is still rate limited")
	}
	if cmdErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, signal, fmt.Errorf("CFST timed out")
		}
		return nil, signal, fmt.Errorf("CFST failed: %w", cmdErr)
	}
	candidates, err := parseCandidates(resultPath)
	if err != nil {
		return nil, signal, err
	}
	if len(candidates) == 0 {
		if signal != nil {
			return nil, signal, fmt.Errorf("CFST rate limited and returned no degraded candidates")
		}
		return nil, signal, errors.New("CFST returned no candidates")
	}
	observed := time.Now().Format(time.RFC3339)
	for i := range candidates {
		candidates[i].ObservedAt = observed
	}
	candidates = rankCandidatesForClass(candidates, "latency")
	a.appendLog("CFST mode=%s returned %d candidates", mode, len(candidates))
	return candidates, signal, nil
}

func parseCandidates(path string) ([]Candidate, error) {
	f, err := os.Open(path)
	if err != nil { return nil, fmt.Errorf("open CFST result: %w", err) }
	defer f.Close()
	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil { return nil, fmt.Errorf("read CFST result: %w", err) }
	if len(records) < 2 { return nil, nil }
	out := make([]Candidate, 0, len(records)-1)
	for _, row := range records[1:] {
		if len(row) < 6 { continue }
		loss, _ := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
		delay, _ := strconv.ParseFloat(strings.TrimSpace(row[4]), 64)
		speed, _ := strconv.ParseFloat(strings.TrimSpace(row[5]), 64)
		colo := ""
		if len(row) > 6 { colo = strings.TrimSpace(row[6]) }
		ip := strings.TrimSpace(row[0])
		if net.ParseIP(ip) == nil { continue }
		out = append(out, Candidate{IP: ip, LossRate: loss, DelayMS: delay, SpeedMB: speed, Colo: colo})
	}
	return out, nil
}

func decodeJSON(b []byte, v any) error { return json.NewDecoder(bytes.NewReader(b)).Decode(v) }
func logLine(format string, args ...any) string { return time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...) }
