package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SyncRuntimeState struct {
	LastAttempt   string `json:"lastAttempt"`
	LastSuccess   string `json:"lastSuccess"`
	LastError     string `json:"lastError"`
	LastCommit    string `json:"lastCommit"`
	LastSignature string `json:"lastSignature"`
}

type syncPayload struct {
	MapText    string
	StatusText string
	Signature  string
}

type githubClient struct {
	baseURL string
	token   string
	http    *http.Client
}

var httpCodePattern = regexp.MustCompile(`HTTP ([1-5][0-9][0-9])`)

func (a *App) publishSync(ctx context.Context, cfg Config, force bool) error {
	if !cfg.Sync.Enabled {
		return errors.New("github sync is disabled")
	}
	payload, err := a.buildSyncPayload(cfg, time.Now())
	if err != nil {
		return err
	}

	a.mu.RLock()
	lastSignature := a.state.Sync.LastSignature
	a.mu.RUnlock()
	if !force && payload.Signature == lastSignature {
		a.appendLog("github sync skipped: mapping signature unchanged")
		return nil
	}

	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		b, readErr := os.ReadFile(cfg.Sync.TokenFile)
		if readErr != nil {
			return fmt.Errorf("read github token: %w", readErr)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return errors.New("github token is empty")
	}

	now := time.Now().Format(time.RFC3339)
	a.mu.Lock()
	a.state.Sync.LastAttempt = now
	a.state.Sync.LastError = ""
	a.mu.Unlock()

	client := &githubClient{
		baseURL: "https://api.github.com",
		token:   token,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
	commit, err := client.publishAtomic(ctx, cfg.Sync.Repository, cfg.Sync.Branch, cfg.Sync.CommitMessage, payload.MapText, payload.StatusText)
	if err != nil {
		a.mu.Lock()
		a.state.Sync.LastError = err.Error()
		a.mu.Unlock()
		return err
	}

	a.mu.Lock()
	a.state.Sync.LastSuccess = time.Now().Format(time.RFC3339)
	a.state.Sync.LastError = ""
	a.state.Sync.LastCommit = commit
	a.state.Sync.LastSignature = payload.Signature
	a.mu.Unlock()
	a.appendLog("github sync published atomically: %s", shortSHA(commit))
	return nil
}

func (a *App) buildSyncPayload(cfg Config, now time.Time) (syncPayload, error) {
	a.mu.RLock()
	mappings := copyMappings(a.state.Mappings)
	statuses := make(map[string]string, len(a.state.DomainStatus))
	for k, v := range a.state.DomainStatus {
		statuses[k] = v
	}
	health := copyHealth(a.state.DomainHealth)
	candidates := append([]Candidate(nil), a.state.Candidates...)
	lastRun := a.state.LastRun
	lastRefresh := a.state.LastRefresh
	a.mu.RUnlock()

	domains := make(map[string]Domain, len(cfg.Domains))
	for _, d := range cfg.Domains {
		if d.Enabled {
			domains[d.Host] = d
		}
	}
	candidateByIP := make(map[string]Candidate, len(candidates))
	for _, c := range rankCandidates(candidates) {
		if _, ok := candidateByIP[c.IP]; !ok {
			candidateByIP[c.IP] = c
		}
	}

	hosts := make([]string, 0, len(mappings))
	for host := range mappings {
		if _, ok := domains[host]; ok {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)

	var mapBuilder strings.Builder
	mapBuilder.WriteString("# domain\tip\tgroup\tdelay_ms\tspeed_mb_s\tloss_percent\tcolo\tverified_at\thttp_code\tstatus\n")
	signatureParts := []string{cfg.Sync.Repository, cfg.Sync.Branch}
	latencyCount := 0
	bandwidthCount := 0
	for _, host := range hosts {
		d := domains[host]
		class := d.Class
		if class != "bandwidth" {
			class = "latency"
		}
		if class == "bandwidth" {
			bandwidthCount++
		} else {
			latencyCount++
		}
		ip := mappings[host]
		c, hasCandidate := candidateByIP[ip]
		delay, speed, loss, colo := "-", "-", "-", "-"
		if hasCandidate {
			delay = strconv.FormatFloat(c.DelayMS, 'f', -1, 64)
			speed = strconv.FormatFloat(c.SpeedMB, 'f', -1, 64)
			loss = strconv.FormatFloat(c.LossRate, 'f', -1, 64)
			if c.Colo != "" {
				colo = c.Colo
			}
		}
		verifiedAt := health[host].LastSuccess
		if verifiedAt == "" {
			verifiedAt = lastRun
		}
		if verifiedAt == "" {
			verifiedAt = now.Format(time.RFC3339)
		}
		httpCode := "-"
		if match := httpCodePattern.FindStringSubmatch(statuses[host]); len(match) == 2 {
			httpCode = match[1]
		} else if d.Mode == "tracker" && cfg.Tracker.RealAnnounce {
			httpCode = "200"
		}
		mapBuilder.WriteString(strings.Join([]string{host, ip, class, delay, speed, loss, colo, verifiedAt, httpCode, "VERIFIED"}, "\t"))
		mapBuilder.WriteByte('\n')
		signatureParts = append(signatureParts, strings.Join([]string{host, ip, class, delay, speed, loss, colo}, "\t"))
	}

	sum := sha256.Sum256([]byte(strings.Join(signatureParts, "\n")))
	signature := hex.EncodeToString(sum[:])
	statusDoc := map[string]any{
		"schema":          2,
		"generated_at":    now.Format(time.RFC3339),
		"source":          "CFHost",
		"map_file":        "hosts-map.tsv",
		"domain_count":    len(hosts),
		"latency_count":   latencyCount,
		"bandwidth_count": bandwidthCount,
		"last_run":        lastRun,
		"last_refresh":    lastRefresh,
		"signature":       signature,
	}
	statusBytes, err := json.MarshalIndent(statusDoc, "", "  ")
	if err != nil {
		return syncPayload{}, err
	}
	return syncPayload{
		MapText:    mapBuilder.String(),
		StatusText: string(statusBytes) + "\n",
		Signature:  signature,
	}, nil
}

func (c *githubClient) publishAtomic(ctx context.Context, repository, branch, message, mapText, statusText string) (string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("github repository must be owner/name")
	}
	if strings.TrimSpace(branch) == "" {
		return "", errors.New("github branch is empty")
	}
	if strings.TrimSpace(message) == "" {
		message = "CFHost: update hosts map"
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		commitSHA, treeSHA, err := c.branchHead(ctx, repository, branch)
		if err != nil {
			return "", err
		}
		mapBlob, err := c.createBlob(ctx, repository, mapText)
		if err != nil {
			return "", err
		}
		statusBlob, err := c.createBlob(ctx, repository, statusText)
		if err != nil {
			return "", err
		}
		newTree, err := c.createTree(ctx, repository, treeSHA, mapBlob, statusBlob)
		if err != nil {
			return "", err
		}
		newCommit, err := c.createCommit(ctx, repository, message, newTree, commitSHA)
		if err != nil {
			return "", err
		}
		if err := c.updateRef(ctx, repository, branch, newCommit); err == nil {
			return newCommit, nil
		} else {
			lastErr = err
		}
	}
	return "", fmt.Errorf("github branch changed during publish: %w", lastErr)
}

func (c *githubClient) branchHead(ctx context.Context, repo, branch string) (string, string, error) {
	var refResp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/repos/"+repo+"/git/ref/heads/"+url.PathEscape(branch), nil, &refResp); err != nil {
		return "", "", err
	}
	if refResp.Object.SHA == "" {
		return "", "", errors.New("github branch head has no sha")
	}
	var commitResp struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/repos/"+repo+"/git/commits/"+refResp.Object.SHA, nil, &commitResp); err != nil {
		return "", "", err
	}
	return refResp.Object.SHA, commitResp.Tree.SHA, nil
}

func (c *githubClient) createBlob(ctx context.Context, repo, content string) (string, error) {
	var resp struct {
		SHA string `json:"sha"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/git/blobs", map[string]string{
		"content":  content,
		"encoding": "utf-8",
	}, &resp)
	return resp.SHA, err
}

func (c *githubClient) createTree(ctx context.Context, repo, baseTree, mapBlob, statusBlob string) (string, error) {
	body := map[string]any{
		"base_tree": baseTree,
		"tree": []map[string]string{
			{"path": "hosts-map.tsv", "mode": "100644", "type": "blob", "sha": mapBlob},
			{"path": "status.json", "mode": "100644", "type": "blob", "sha": statusBlob},
		},
	}
	var resp struct {
		SHA string `json:"sha"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/git/trees", body, &resp)
	return resp.SHA, err
}

func (c *githubClient) createCommit(ctx context.Context, repo, message, treeSHA, parentSHA string) (string, error) {
	var resp struct {
		SHA string `json:"sha"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/git/commits", map[string]any{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
	}, &resp)
	return resp.SHA, err
}

func (c *githubClient) updateRef(ctx context.Context, repo, branch, sha string) error {
	return c.doJSON(ctx, http.MethodPatch, "/repos/"+repo+"/git/refs/heads/"+url.PathEscape(branch), map[string]any{
		"sha":   sha,
		"force": false,
	}, nil)
}

func (c *githubClient) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CFHost/0.3")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github API %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
