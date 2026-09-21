package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

func (a *App) runCFST(ctx context.Context, cfg Config) ([]Candidate, error) {
	resultPath := filepath.Join(a.dataDir, "result.csv")
	_ = os.Remove(resultPath)

	ipFile := getenv("CFST_IP_FILE", "/app/ip.txt")
	if cfg.CFST.IPv6 { ipFile = getenv("CFST_IPV6_FILE", "/app/ipv6.txt") }
	args := []string{
		"-n", strconv.Itoa(cfg.CFST.Threads),
		"-t", strconv.Itoa(cfg.CFST.PingTimes),
		"-dn", strconv.Itoa(cfg.CFST.DownloadCount),
		"-dt", strconv.Itoa(cfg.CFST.DownloadSeconds),
		"-tl", strconv.Itoa(cfg.CFST.MaxDelayMS),
		"-tlr", strconv.FormatFloat(cfg.CFST.MaxLossRate, 'f', -1, 64),
		"-sl", strconv.FormatFloat(cfg.CFST.MinSpeedMB, 'f', -1, 64),
		"-p", "0",
		"-f", ipFile,
		"-o", resultPath,
	}
	if strings.TrimSpace(cfg.CFST.DownloadURL) != "" { args = append(args, "-url", cfg.CFST.DownloadURL) }

	a.appendLog("CFST: %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, a.cfstBin, args...)
	cmd.Dir = a.dataDir
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" { a.appendLog("cfst | %s", line) }
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) { return nil, fmt.Errorf("CFST timed out") }
		return nil, fmt.Errorf("CFST failed: %w", err)
	}
	candidates, err := parseCandidates(resultPath)
	if err != nil { return nil, err }
	if len(candidates) == 0 { return nil, errors.New("CFST returned no candidates") }
	observed := time.Now().Format(time.RFC3339)
	for i := range candidates { candidates[i].ObservedAt = observed }
	candidates = rankCandidatesForClass(candidates, "latency")
	a.appendLog("CFST returned %d candidates", len(candidates))
	return candidates, nil
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
