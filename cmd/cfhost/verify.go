package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

func rankCandidates(in []Candidate) []Candidate {
	return rankCandidatesForClass(in, "latency")
}

func rankCandidatesForClass(in []Candidate, class string) []Candidate {
	out := append([]Candidate(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if class == "normal" {
			if out[i].DelayMS != out[j].DelayMS { return out[i].DelayMS < out[j].DelayMS }
			if out[i].LossRate != out[j].LossRate { return out[i].LossRate < out[j].LossRate }
			if out[i].SpeedMB != out[j].SpeedMB { return out[i].SpeedMB > out[j].SpeedMB }
			return out[i].IP < out[j].IP
		}
		if out[i].LossRate != out[j].LossRate { return out[i].LossRate < out[j].LossRate }
		if class == "bandwidth" {
			if out[i].SpeedMB != out[j].SpeedMB { return out[i].SpeedMB > out[j].SpeedMB }
			if out[i].DelayMS != out[j].DelayMS { return out[i].DelayMS < out[j].DelayMS }
		} else {
			if out[i].DelayMS != out[j].DelayMS { return out[i].DelayMS < out[j].DelayMS }
			if out[i].SpeedMB != out[j].SpeedMB { return out[i].SpeedMB > out[j].SpeedMB }
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func freshCandidates(in []Candidate, ttl time.Duration, now time.Time) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		observed, err := time.Parse(time.RFC3339, c.ObservedAt)
		if err != nil || now.Before(observed) || now.Sub(observed) > ttl { continue }
		out = append(out, c)
	}
	return rankCandidates(out)
}

func candidateEligible(c Candidate, d Domain, cfg Config) bool {
	if d.Class != "bandwidth" { return true }
	return c.LossRate <= cfg.Bandwidth.MaxLossRate &&
		c.DelayMS <= float64(cfg.Bandwidth.MaxDelayMS) &&
		c.SpeedMB >= cfg.Bandwidth.MinSpeedMB
}

func orderedCandidates(all []Candidate, d Domain, preferred, skipIP string, cfg Config) []Candidate {
	if d.Class == "normal" { preferred = "" }
	filtered := make([]Candidate, 0, len(all))
	for _, c := range all {
		if c.IP == skipIP || !candidateEligible(c, d, cfg) { continue }
		filtered = append(filtered, c)
	}
	ranked := rankCandidatesForClass(filtered, d.Class)
	out := make([]Candidate, 0, len(ranked))
	if preferred != "" && preferred != skipIP {
		for _, c := range ranked {
			if c.IP == preferred {
				out = append(out, c)
				break
			}
		}
	}
	for _, c := range ranked {
		if c.IP == preferred { continue }
		out = append(out, c)
	}
	if cfg.Verify.CandidateLimit > 0 && len(out) > cfg.Verify.CandidateLimit {
		out = out[:cfg.Verify.CandidateLimit]
	}
	return out
}

func groupKey(d Domain) string {
	if d.Group == "" { return "" }
	return d.Group + "|" + d.Class
}

func verificationHardFailure(detail string) bool {
	fields := strings.Fields(detail)
	for i := 0; i+1 < len(fields); i++ {
		if strings.EqualFold(strings.Trim(fields[i], "·:()[]"), "HTTP") {
			code := strings.Trim(fields[i+1], "·:()[];,")
			switch code {
			case "403", "421", "451":
				return true
			}
		}
	}
	return strings.Contains(strings.ToLower(detail), "blocked/unusable")
}

func domainVerificationContext(parent context.Context, domainsLeft int) (context.Context, context.CancelFunc, time.Duration, bool) {
	if parent == nil || parent.Err() != nil {
		return nil, func(){}, 0, false
	}
	if domainsLeft < 1 {
		domainsLeft = 1
	}
	deadline, ok := parent.Deadline()
	if !ok {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel, 0, true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, func(){}, 0, false
	}
	reserve := remaining / 10
	if reserve < 5*time.Second {
		reserve = 5 * time.Second
	}
	if reserve > 30*time.Second {
		reserve = 30 * time.Second
	}
	usable := remaining - reserve
	if usable < time.Second {
		return nil, func(){}, 0, false
	}
	budget := usable / time.Duration(domainsLeft)
	if budget < time.Second {
		return nil, func(){}, 0, false
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	return ctx, cancel, budget, true
}

func verifyHTTPConnectivity(parent context.Context, d Domain, ip string, cfg Config) (bool, string) {
	timeout := time.Duration(cfg.VerifyTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	baseDialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err == nil && strings.EqualFold(host, d.Host) && port == "443" {
				return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
			}
			return baseDialer.DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > cfg.Verify.MaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	endpoint := d.Endpoint
	if endpoint == "" { endpoint = "/" }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.Host+endpoint, nil)
	if err != nil { return false, "request error" }
	req.Header.Set("User-Agent", "CFHost/0.4")
	resp, err := client.Do(req)
	if err != nil { return false, err.Error() }
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	ok, detail := evaluateHTTPConnectivityStatus(resp.StatusCode)
	if resp.Request != nil && resp.Request.URL != nil {
		detail = fmt.Sprintf("%s final=%s", detail, resp.Request.URL.String())
	}
	return ok, detail
}

type httpProbeDisposition int

const (
	httpProbeReachable httpProbeDisposition = iota
	httpProbeTemporaryFailure
	httpProbeHardFailure
)

func classifyHTTPConnectivityStatus(status int) httpProbeDisposition {
	if status < 100 || status > 599 {
		return httpProbeHardFailure
	}
	switch status {
	case http.StatusForbidden, http.StatusMisdirectedRequest, http.StatusUnavailableForLegalReasons:
		return httpProbeHardFailure
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return httpProbeTemporaryFailure
	}
	if status >= 500 {
		return httpProbeTemporaryFailure
	}
	return httpProbeReachable
}

var httpStatusCodeDetailPattern = regexp.MustCompile(`(?i)\bHTTP\s+([1-5][0-9]{2})\b`)

func httpStatusCodeFromDetail(detail string) int {
	match := httpStatusCodeDetailPattern.FindStringSubmatch(detail)
	if len(match) != 2 || len(match[1]) != 3 {
		return 0
	}
	return int(match[1][0]-'0')*100 + int(match[1][1]-'0')*10 + int(match[1][2]-'0')
}

func evaluateHTTPConnectivityStatus(status int) (bool, string) {
	switch classifyHTTPConnectivityStatus(status) {
	case httpProbeReachable:
		return true, fmt.Sprintf("HTTP %d", status)
	case httpProbeTemporaryFailure:
		return false, fmt.Sprintf("HTTP %d · temporary failure", status)
	default:
		return false, fmt.Sprintf("HTTP %d · blocked/unusable", status)
	}
}

func verifyHTTPDomain(parent context.Context, d Domain, ip string, cfg Config) (bool, string) {
	if !cfg.Verify.StrictHTTP { return verifyHTTPConnectivity(parent, d, ip, cfg) }
	var last string
	for attempt := 1; attempt <= cfg.Verify.HTTPRetries; attempt++ {
		ok, detail := strictHTTPAttempt(parent, d, ip, cfg)
		last = detail
		if ok { return true, detail + fmt.Sprintf(" · attempt %d", attempt) }
		if verificationHardFailure(detail) {
			return false, detail + fmt.Sprintf(" · hard fail attempt %d", attempt)
		}
		if attempt < cfg.Verify.HTTPRetries {
			select {
			case <-parent.Done():
				return false, parent.Err().Error()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return false, last
}

func strictHTTPAttempt(parent context.Context, d Domain, ip string, cfg Config) (bool, string) {
	timeout := time.Duration(cfg.VerifyTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	baseDialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{ForceAttemptHTTP2: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil && strings.EqualFold(host, d.Host) && port == "443" {
			return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		}
		return baseDialer.DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > cfg.Verify.MaxRedirects { return fmt.Errorf("too many redirects") }
			return nil
		},
	}
	endpoint := d.Endpoint
	if endpoint == "" { endpoint = "/" }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.Host+endpoint, nil)
	if err != nil { return false, "request error" }
	req.Header.Set("User-Agent", "CFHost/0.4")
	resp, err := client.Do(req)
	if err != nil { return false, err.Error() }
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if readErr != nil { return false, "body read failed: " + readErr.Error() }
	return evaluateStrictHTTPResponse(resp.StatusCode, body, resp.Request.URL.String(), cfg)
}

func evaluateStrictHTTPResponse(status int, body []byte, finalURL string, cfg Config) (bool, string) {
	if status < 200 || status >= 300 {
		return false, fmt.Sprintf("HTTP %d final=%s", status, finalURL)
	}
	pattern, _ := regexp.Compile("(?i)" + cfg.Verify.BlockPatterns)
	if pattern != nil && pattern.Find(body) != nil {
		return false, fmt.Sprintf("HTTP %d challenge-or-placeholder final=%s", status, finalURL)
	}
	if status == http.StatusOK && len(body) < cfg.Verify.MinBodyBytes {
		return false, fmt.Sprintf("HTTP 200 body-too-small=%d final=%s", len(body), finalURL)
	}
	return true, fmt.Sprintf("HTTP %d bytes=%d final=%s", status, len(body), finalURL)
}
