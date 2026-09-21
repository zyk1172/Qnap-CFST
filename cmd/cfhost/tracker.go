package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type TrackerSample struct {
	Domain   string
	Path     string
	HashHex  string
	Hash     []byte
	URL      string
}

func loadTrackerSamples(path string) (map[string]TrackerSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make(map[string]TrackerSample)
	problems := make([]string, 0)
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			problems = append(problems, fmt.Sprintf("line %d: expected 4 tab-separated fields", lineNo))
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(parts[0]))
		pathValue := strings.TrimSpace(parts[1])
		hashHex := strings.TrimSpace(parts[2])
		rawURL := strings.TrimSpace(parts[3])
		hashBytes, err := hex.DecodeString(hashHex)
		if err != nil || len(hashBytes) != 20 {
			problems = append(problems, fmt.Sprintf("line %d: invalid 40-char info hash", lineNo))
			continue
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), domain) {
			problems = append(problems, fmt.Sprintf("line %d: announce URL must be https and match domain", lineNo))
			continue
		}
		if pathValue == "" {
			pathValue = u.Path
		}
		if !strings.HasPrefix(pathValue, "/") || u.Path != pathValue {
			problems = append(problems, fmt.Sprintf("line %d: path does not match announce URL", lineNo))
			continue
		}
		out[domain] = TrackerSample{
			Domain:  domain,
			Path:    pathValue,
			HashHex: strings.ToLower(hashHex),
			Hash:    hashBytes,
			URL:     rawURL,
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if len(problems) > 0 {
		return out, fmt.Errorf("ignored invalid tracker samples: %s", strings.Join(problems, "; "))
	}
	return out, nil
}
func verifyConfiguredDomain(ctx context.Context, d Domain, ip string, cfg Config, samples map[string]TrackerSample) (bool, string) {
	if d.Mode != "tracker" || !cfg.Tracker.RealAnnounce {
		return verifyHTTPDomain(ctx, d, ip, cfg.VerifyTimeoutSeconds)
	}
	sample, ok := samples[d.Host]
	if !ok {
		return false, "tracker sample missing"
	}
	return verifyTrackerAnnounce(ctx, d, ip, sample, cfg)
}

func domainRefreshable(d Domain, cfg Config, samples map[string]TrackerSample) bool {
	if d.Mode == "tracker" && cfg.Tracker.RealAnnounce {
		_, ok := samples[d.Host]
		return ok
	}
	return true
}

func verifyTrackerAnnounce(parent context.Context, d Domain, ip string, sample TrackerSample, cfg Config) (bool, string) {
	retries := cfg.Tracker.Retries
	if retries < 1 {
		retries = 1
	}
	var last string
	for attempt := 1; attempt <= retries; attempt++ {
		ok, detail := trackerAnnounceAttempt(parent, d, ip, sample, cfg)
		last = detail
		if ok {
			return true, fmt.Sprintf("%s · attempt %d", detail, attempt)
		}
		if attempt < retries {
			timer := time.NewTimer(time.Second)
			select {
			case <-parent.Done():
				timer.Stop()
				return false, parent.Err().Error()
			case <-timer.C:
			}
		}
	}
	return false, last
}

func trackerAnnounceAttempt(parent context.Context, d Domain, ip string, sample TrackerSample, cfg Config) (bool, string) {
	timeout := time.Duration(cfg.VerifyTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	announceURL := buildAnnounceURL(sample, cfg.Tracker.PeerIDPrefix, cfg.Tracker.AnnouncePort)
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, announceURL, nil)
	if err != nil {
		return false, "tracker request error"
	}
	req.Header.Set("User-Agent", cfg.Tracker.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("tracker HTTP %d", resp.StatusCode)
	}
	if ok, reason := trackerResponseOK(body); !ok {
		return false, reason
	}
	return true, "announce response"
}

func trackerResponseOK(body []byte) (bool, string) {
	trimmed := bytes.TrimSpace(body)
	if bytes.Contains(trimmed, []byte("failure reason")) {
		return false, "tracker failure reason"
	}
	if len(trimmed) == 0 || trimmed[0] != 'd' {
		return false, "tracker invalid response"
	}
	if !bytes.Contains(trimmed, []byte("8:interval")) && !bytes.Contains(trimmed, []byte("5:peers")) {
		return false, "tracker invalid response"
	}
	return true, "announce response"
}

func buildAnnounceURL(sample TrackerSample, peerPrefix string, port int) string {
	separator := "?"
	if strings.Contains(sample.URL, "?") {
		separator = "&"
	}
	if strings.HasSuffix(sample.URL, "?") || strings.HasSuffix(sample.URL, "&") {
		separator = ""
	}
	peerID := makePeerID(peerPrefix)
	query := "info_hash=" + percentEncodeBytes(sample.Hash) +
		"&peer_id=" + url.QueryEscape(peerID) +
		"&port=" + strconv.Itoa(port) +
		"&uploaded=0&downloaded=0&left=0&compact=1&numwant=0"
	return sample.URL + separator + query
}

func percentEncodeBytes(b []byte) string {
	var sb strings.Builder
	for _, v := range b {
		fmt.Fprintf(&sb, "%%%02X", v)
	}
	return sb.String()
}

func makePeerID(prefix string) string {
	need := 20 - len(prefix)
	if need <= 0 {
		return prefix[:20]
	}
	seed := strconv.FormatInt(time.Now().UnixNano(), 10)
	if len(seed) < need {
		seed = strings.Repeat("0", need-len(seed)) + seed
	}
	if len(seed) > need {
		seed = seed[len(seed)-need:]
	}
	return prefix + seed
}
