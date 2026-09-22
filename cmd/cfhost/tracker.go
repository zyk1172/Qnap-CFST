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
	"regexp"
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
	if d.Class == "normal" {
		return true, "verification skipped · normal"
	}
	if d.Mode != "tracker" {
		return verifyHTTPDomain(ctx, d, ip, cfg)
	}
	if !cfg.Tracker.RealAnnounce {
		return verifyHTTPConnectivity(ctx, d, ip, cfg)
	}
	sample, ok := samples[d.Host]
	if !ok {
		return false, "tracker sample missing"
	}
	return verifyTrackerAnnounce(ctx, d, ip, sample, cfg)
}

func domainRefreshable(d Domain, cfg Config, samples map[string]TrackerSample) bool {
	if d.Class == "normal" {
		return true
	}
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
	verdict := evaluateTrackerResponse(body)
	return verdict.Reachable, verdict.Detail
}

// trackerProbeVerdict deliberately separates candidate reachability from whether
// the tracker accepted this particular announce. Once the request reached the
// tracker through the candidate IP and the tracker returned a valid bencoded
// dictionary, the Hosts mapping has proven its job even when the tracker sends
// a business-level failure reason.
type trackerProbeVerdict struct {
	Reachable bool
	Accepted  bool
	Reason    string
	Detail    string
}

func evaluateTrackerResponse(body []byte) trackerProbeVerdict {
	trimmed := bytes.TrimSpace(body)
	keys, failureReason, ok := parseTrackerDictionary(trimmed)
	if !ok {
		return trackerProbeVerdict{Detail: "tracker invalid response"}
	}
	if failureReason != nil {
		reason := sanitizeTrackerReason(failureReason)
		if reason == "" {
			reason = "tracker rejected announce"
		}
		if trackerFailureIndicatesUnreachable(reason) {
			return trackerProbeVerdict{
				Reachable: false,
				Reason:    reason,
				Detail:    "tracker unreachable · " + reason,
			}
		}
		return trackerProbeVerdict{
			Reachable: true,
			Reason:    reason,
			Detail:    "announce rejected · candidate reachable · " + reason,
		}
	}
	if keys["interval"] || keys["peers"] || keys["peers6"] {
		return trackerProbeVerdict{Reachable: true, Accepted: true, Detail: "announce accepted"}
	}
	return trackerProbeVerdict{Reachable: true, Detail: "tracker replied · candidate reachable"}
}

// Some front ends return a syntactically valid Tracker failure dictionary even
// though they could not establish the upstream Tracker connection. Those errors
// prove that the HTTP endpoint answered, but not that this candidate can reach
// the Tracker service, so they must remain candidate failures.
func trackerFailureIndicatesUnreachable(reason string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(reason), " "))
	patterns := []string{
		"could not connect to track",
		"couldn't connect to track",
		"cannot connect to track",
		"can't connect to track",
		"failed to connect to track",
		"unable to connect to track",
		"tracker connection failed",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

// parseTrackerDictionary validates the complete top-level bencoded dictionary
// instead of accepting any body that merely starts with 'd' and ends with 'e'.
// It also extracts a string-valued "failure reason" when present.
func parseTrackerDictionary(data []byte) (map[string]bool, []byte, bool) {
	if len(data) < 2 || data[0] != 'd' {
		return nil, nil, false
	}
	pos := 1
	keys := make(map[string]bool)
	var failureReason []byte
	for {
		if pos >= len(data) {
			return nil, nil, false
		}
		if data[pos] == 'e' {
			pos++
			return keys, failureReason, pos == len(data)
		}
		keyBytes, ok := parseBencodeString(data, &pos)
		if !ok {
			return nil, nil, false
		}
		key := string(keyBytes)
		keys[key] = true
		if key == "failure reason" && pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
			value, ok := parseBencodeString(data, &pos)
			if !ok {
				return nil, nil, false
			}
			failureReason = append([]byte(nil), value...)
			continue
		}
		if !skipBencodeValue(data, &pos, 0) {
			return nil, nil, false
		}
	}
}

func parseBencodeString(data []byte, pos *int) ([]byte, bool) {
	if *pos >= len(data) || data[*pos] < '0' || data[*pos] > '9' {
		return nil, false
	}
	start := *pos
	for *pos < len(data) && data[*pos] >= '0' && data[*pos] <= '9' {
		(*pos)++
	}
	if *pos >= len(data) || data[*pos] != ':' {
		return nil, false
	}
	lengthText := string(data[start:*pos])
	if len(lengthText) > 1 && lengthText[0] == '0' {
		return nil, false
	}
	length, err := strconv.Atoi(lengthText)
	if err != nil || length < 0 {
		return nil, false
	}
	(*pos)++
	if length > len(data)-*pos {
		return nil, false
	}
	value := data[*pos : *pos+length]
	*pos += length
	return value, true
}

func skipBencodeValue(data []byte, pos *int, depth int) bool {
	if depth > 64 || *pos >= len(data) {
		return false
	}
	switch data[*pos] {
	case 'i':
		(*pos)++
		start := *pos
		for *pos < len(data) && data[*pos] != 'e' {
			(*pos)++
		}
		if *pos >= len(data) || start == *pos {
			return false
		}
		number := string(data[start:*pos])
		if !validBencodeInteger(number) {
			return false
		}
		(*pos)++
		return true
	case 'l':
		(*pos)++
		for {
			if *pos >= len(data) {
				return false
			}
			if data[*pos] == 'e' {
				(*pos)++
				return true
			}
			if !skipBencodeValue(data, pos, depth+1) {
				return false
			}
		}
	case 'd':
		(*pos)++
		for {
			if *pos >= len(data) {
				return false
			}
			if data[*pos] == 'e' {
				(*pos)++
				return true
			}
			if _, ok := parseBencodeString(data, pos); !ok {
				return false
			}
			if !skipBencodeValue(data, pos, depth+1) {
				return false
			}
		}
	default:
		if data[*pos] >= '0' && data[*pos] <= '9' {
			_, ok := parseBencodeString(data, pos)
			return ok
		}
		return false
	}
}

func validBencodeInteger(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" {
		return false
	}
	start := 0
	if value[0] == '-' {
		if len(value) == 1 || value[1] == '0' {
			return false
		}
		start = 1
	} else if value[0] == '0' {
		return false
	}
	for i := start; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// Trackers sometimes echo request parameters back in the failure reason.
var trackerReasonSecretPattern = regexp.MustCompile(`(?i)(passkey|credential|torrent_pass|authkey)=[^&\\s]*`)

func sanitizeTrackerReason(raw []byte) string {
	reason := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, string(raw))
	reason = trackerReasonSecretPattern.ReplaceAllString(reason, "$1=***")
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > 160 {
		reason = strings.ToValidUTF8(reason[:160], "") + "…"
	}
	return reason
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
