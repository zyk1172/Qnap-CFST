package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	hostsBegin = "# >>> CFHOST MANAGED >>>"
	hostsEnd   = "# <<< CFHOST MANAGED <<<"
)

type Candidate struct {
	IP        string  `json:"ip"`
	LossRate  float64 `json:"lossRate"`
	DelayMS   float64 `json:"delayMs"`
	SpeedMB   float64 `json:"speedMB"`
	Colo      string  `json:"colo"`
}

func (a *App) runJob(ctx context.Context, kind string, cfg Config) error {
	candidates, err := a.runCFST(ctx, cfg)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.state.Candidates = candidates
	a.mu.Unlock()

	if kind == "run" {
		return nil
	}
	mappings, statuses := a.resolveDomains(ctx, cfg, candidates)
	a.mu.Lock()
	a.state.Mappings = mappings
	a.state.DomainStatus = statuses
	a.mu.Unlock()

	if cfg.AutoApply {
		return a.applyMappings(cfg.HostsPath, mappings)
	}
	return nil
}

func (a *App) runCFST(ctx context.Context, cfg Config) ([]Candidate, error) {
	resultPath := filepath.Join(a.dataDir, "result.csv")
	_ = os.Remove(resultPath)

	ipFile := getenv("CFST_IP_FILE", "/app/ip.txt")
	if cfg.CFST.IPv6 {
		ipFile = getenv("CFST_IPV6_FILE", "/app/ipv6.txt")
	}
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
	if strings.TrimSpace(cfg.CFST.DownloadURL) != "" {
		args = append(args, "-url", cfg.CFST.DownloadURL)
	}

	a.appendLog("CFST: %s", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, a.cfstBin, args...)
	cmd.Dir = a.dataDir
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			a.appendLog("cfst | %s", line)
		}
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("CFST timed out")
		}
		return nil, fmt.Errorf("CFST failed: %w", err)
	}
	candidates, err := parseCandidates(resultPath)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("CFST returned no candidates")
	}
	a.appendLog("CFST returned %d candidates", len(candidates))
	return candidates, nil
}

func parseCandidates(path string) ([]Candidate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CFST result: %w", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CFST result: %w", err)
	}
	if len(records) < 2 {
		return nil, nil
	}
	out := make([]Candidate, 0, len(records)-1)
	for _, row := range records[1:] {
		if len(row) < 6 {
			continue
		}
		loss, _ := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
		delay, _ := strconv.ParseFloat(strings.TrimSpace(row[4]), 64)
		speed, _ := strconv.ParseFloat(strings.TrimSpace(row[5]), 64)
		colo := ""
		if len(row) > 6 {
			colo = strings.TrimSpace(row[6])
		}
		ip := strings.TrimSpace(row[0])
		if net.ParseIP(ip) == nil {
			continue
		}
		out = append(out, Candidate{IP: ip, LossRate: loss, DelayMS: delay, SpeedMB: speed, Colo: colo})
	}
	return out, nil
}

func (a *App) resolveDomains(ctx context.Context, cfg Config, candidates []Candidate) (map[string]string, map[string]string) {
	mappings := make(map[string]string)
	statuses := make(map[string]string)
	groupIP := make(map[string]string)

	for _, d := range cfg.Domains {
		if !d.Enabled {
			statuses[d.Host] = "disabled"
			continue
		}

		order := candidateOrder(candidates, groupIP[d.Group])
		for _, c := range order {
			ok, detail := verifyDomain(ctx, d, c.IP, cfg.VerifyTimeoutSeconds)
			if ok {
				mappings[d.Host] = c.IP
				statuses[d.Host] = "ok · " + c.IP + " · " + detail
				if d.Group != "" && groupIP[d.Group] == "" {
					groupIP[d.Group] = c.IP
				}
				a.appendLog("%s -> %s (%s)", d.Host, c.IP, detail)
				break
			}
		}
		if _, ok := mappings[d.Host]; !ok {
			statuses[d.Host] = "no verified candidate"
			a.appendLog("%s: no verified candidate", d.Host)
		}
	}
	return mappings, statuses
}

func candidateOrder(all []Candidate, preferred string) []Candidate {
	if preferred == "" {
		return all
	}
	out := make([]Candidate, 0, len(all))
	for _, c := range all {
		if c.IP == preferred {
			out = append(out, c)
			break
		}
	}
	for _, c := range all {
		if c.IP != preferred {
			out = append(out, c)
		}
	}
	return out
}

func verifyDomain(parent context.Context, d Domain, ip string, timeoutSeconds int) (bool, string) {
	timeout := time.Duration(timeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, "443"))
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.Host+endpoint, nil)
	if err != nil {
		return false, "request error"
	}
	req.Header.Set("User-Agent", "CFHost/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode < 100 || resp.StatusCode > 599 {
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if d.Mode == "tracker" {
		return true, fmt.Sprintf("tracker endpoint HTTP %d", resp.StatusCode)
	}
	return true, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

func (a *App) applyMappings(hostsPath string, mappings map[string]string) error {
	if len(mappings) == 0 {
		return errors.New("no mappings to apply")
	}
	current, err := os.ReadFile(hostsPath)
	if err != nil {
		return fmt.Errorf("read hosts: %w", err)
	}
	rendered := renderHosts(string(current), mappings)
	if rendered == string(current) {
		a.appendLog("hosts unchanged")
		return nil
	}

	backup := filepath.Join(a.dataDir, "hosts-backup-"+time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, current, 0644); err != nil {
		return fmt.Errorf("backup hosts: %w", err)
	}
	f, err := os.OpenFile(hostsPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("open hosts for write: %w", err)
	}
	if _, err := io.Copy(f, strings.NewReader(rendered)); err != nil {
		_ = f.Close()
		return fmt.Errorf("write hosts: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	a.appendLog("hosts applied: %d mappings", len(mappings))
	return nil
}

func renderHosts(existing string, mappings map[string]string) string {
	lines := strings.Split(strings.ReplaceAll(existing, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+len(mappings)+3)
	skipping := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case hostsBegin:
			skipping = true
			continue
		case hostsEnd:
			skipping = false
			continue
		}
		if !skipping {
			out = append(out, line)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, "", hostsBegin)
	hosts := make([]string, 0, len(mappings))
	for host := range mappings {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		out = append(out, mappings[host]+" "+host)
	}
	out = append(out, hostsEnd, "")
	return strings.Join(out, "\n")
}

func decodeJSON(b []byte, v any) error {
	return json.NewDecoder(bytes.NewReader(b)).Decode(v)
}

func logLine(format string, args ...any) string {
	return time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
}
