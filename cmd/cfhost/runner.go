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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Candidate struct {
	Source string `json:"source,omitempty"`
	mappingFallback bool
	IP         string  `json:"ip"`
	LossRate   float64 `json:"lossRate"`
	DelayMS    float64 `json:"delayMs"`
	SpeedMB    float64 `json:"speedMB"`
	LatencyOnly bool `json:"latencyOnly,omitempty"`
	Colo       string  `json:"colo"`
	ObservedAt string  `json:"observedAt"`
}

type cfstRateLimitSignal struct {
	StatusCode int
	RetryAfter string
	Origin string
	Blocked bool
}

func markerField(line,key string) string {
 pattern:=regexp.MustCompile(regexp.QuoteMeta(key)+`=("(?:\\.|[^"\\])*"|[^ ]*)`)
 match:=pattern.FindStringSubmatch(line)
 if len(match)<2 {return ""}
 if decoded,err:=strconv.Unquote(match[1]);err==nil {return decoded}
 return match[1]
}

func parseCFSTRateLimitSignal(output string) (cfstRateLimitSignal,bool) {
 for _,line:=range strings.Split(output,"\n") {
  if strings.Contains(line,"CFST_SOURCE_BLOCKED") {return cfstRateLimitSignal{Origin:markerField(line,"origin"),Blocked:true},true}
  if !strings.Contains(line,"CFST_RATE_LIMIT") {continue}
  status,err:=strconv.Atoi(markerField(line,"status"));if err!=nil {continue}
  return cfstRateLimitSignal{StatusCode:status,RetryAfter:markerField(line,"retry_after"),Origin:markerField(line,"origin")},true
 }
 return cfstRateLimitSignal{},false
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

func degradedDownloadURL(raw string, megabytes float64) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || megabytes <= 0 {
		return raw, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw, false
	}
	query := u.Query()
	if _, exists := query["bytes"]; !exists {
		if !strings.EqualFold(u.Hostname(), "speed.cloudflare.com") || !strings.Contains(u.Path, "/__down") {
			return raw, false
		}
	}
	bytes := int64(megabytes*1_000_000 + 0.5)
	if bytes < 1 {
		return raw, false
	}
	query.Set("bytes", strconv.FormatInt(bytes, 10))
	u.RawQuery = query.Encode()
	return u.String(), true
}

func buildCFSTArgs(cfg Config, ipFile, resultPath string, degraded bool) []string {
	downloadCount := cfg.CFST.DownloadCount
	downloadSeconds := cfg.CFST.DownloadSeconds
	minSpeed := cfg.CFST.MinSpeedMB
	downloadURL := strings.TrimSpace(cfg.CFST.DownloadURL)
	args := []string{
		"-n", strconv.Itoa(cfg.CFST.Threads),
		"-t", strconv.Itoa(cfg.CFST.PingTimes),
	}
	if degraded {
		downloadCount = minPositive(minPositive(cfg.CFST.DownloadCount, cfg.CFST.DegradedDownloadCount), 5)
		downloadSeconds = minPositive(cfg.CFST.DegradedDownloadSeconds, 3)
		minSpeed = 0
		if reducedURL, ok := degradedDownloadURL(downloadURL, cfg.CFST.DegradedDownloadMB); ok {
			downloadURL = reducedURL
		}
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
	if downloadURL != "" {
		args = append(args, "-url", downloadURL)
	}
	return args
}

func cfstRunTimeout(cfg Config) time.Duration {
	timeout := time.Duration(cfg.CFST.RunTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return timeout
}

func (a *App) runCFST(ctx context.Context, cfg Config) ([]Candidate, error) {
	return a.runCFSTFor(ctx, cfg, "manual or scheduled candidate refresh")
}

func (a *App) runCFSTMode(ctx context.Context, cfg Config, degraded bool) ([]Candidate, *cfstRateLimitSignal, error) {
	resultPath := filepath.Join(a.dataDir, "result.csv")
	_ = os.Remove(resultPath)

	ipFile := getenv("CFST_IP_FILE", "/app/ip.txt")
	if cfg.CFST.IPv6 {
		ipFile = getenv("CFST_IPV6_FILE", "/app/ipv6.txt")
	}
	args := buildCFSTArgs(cfg, ipFile, resultPath, degraded)
	latencyPath := filepath.Join(a.dataDir, "latency-candidates.csv")
	_ = os.Remove(latencyPath)
	args = append(args, "-latency-output", latencyPath)
	mode := "normal"
	if degraded {
		mode = "recovery"
	}
	safeArgs:=append([]string(nil),args...)
	for i:=0;i+1<len(safeArgs);i++ {if safeArgs[i]=="-url" {safeArgs[i+1]=sourceOrigin(safeArgs[i+1])}}
	a.appendLog("CFST mode=%s: %s", mode, strings.Join(safeArgs, " "))
	cmd := exec.CommandContext(ctx, a.cfstBin, args...)
	cmd.Dir = a.dataDir
	attemptLimit:=cfg.CFST.DownloadCount
	if attemptLimit<1 {attemptLimit=20}
	if degraded {attemptLimit=minPositive(attemptLimit,5)}
	cmd.Env=append(os.Environ(),"CFST_BLOCKED_ORIGINS="+a.blockedSourceOrigins(cfg),"CFST_MAX_DOWNLOAD_ATTEMPTS="+strconv.Itoa(attemptLimit))
	out, cmdErr := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			a.appendLog("cfst | %s", line)
		}
	}
	for _,line:=range strings.Split(output,"\n") {if strings.Contains(line,"CFST_SOURCE ") {a.noteSourceOrigin(cfg.CFST.DownloadURL,markerField(line,"origin"))}}
	var signal *cfstRateLimitSignal
	if detected, ok := parseCFSTRateLimitSignal(output); ok {
		signal = &detected
		if detected.Origin!="" {a.noteSourceOrigin(cfg.CFST.DownloadURL,detected.Origin)}
	}
		if cmdErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, signal, fmt.Errorf("CFST timed out")
		}
		return nil, signal, fmt.Errorf("CFST failed: %w", cmdErr)
	}
	candidates, err := parseCandidates(resultPath)
	if err != nil {
  if !errors.Is(err, os.ErrNotExist) { return nil, signal, err }
  candidates = nil
	}
	// Delay-only candidates are useful only after domain-specific verification.
	// Never silently convert a missing/failed bandwidth test into a speed result.
	if len(candidates) < 20 {
  if latency, latencyErr := parseCandidates(latencyPath); latencyErr == nil {
   seen := make(map[string]bool, len(candidates))
   for _, c := range candidates { seen[c.IP] = true }
   for _, c := range latency {
    if !seen[c.IP] { c.LatencyOnly = true; c.SpeedMB = 0; candidates = append(candidates,c); seen[c.IP]=true }
   }
   a.appendLog("CFST supplemented candidates with latency-only addresses; domain verification required")
  }
	}
	for i := range candidates { if candidates[i].SpeedMB <= 0 { candidates[i].LatencyOnly = true } }
	if len(candidates) == 0 {
		if signal != nil {
			return nil, signal, fmt.Errorf("CFST rate limited and returned no degraded candidates")
		}
		return nil, signal, errors.New("CFST returned no candidates")
	}
	observed := time.Now().Format(time.RFC3339)
	for i := range candidates {
		candidates[i].ObservedAt = observed
		candidates[i].Source=sourceOrigin(cfg.CFST.DownloadURL)
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
