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
	return out
}

func candidateEligible(c Candidate, d Domain, cfg Config) bool {
	if d.Class != "bandwidth" { return true }
	return c.LossRate <= cfg.Bandwidth.MaxLossRate &&
		c.DelayMS <= float64(cfg.Bandwidth.MaxDelayMS) &&
		c.SpeedMB >= cfg.Bandwidth.MinSpeedMB
}

func orderedCandidates(all []Candidate, d Domain, preferred, skipIP string, cfg Config) []Candidate {
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

func verifyHTTPConnectivity(parent context.Context, d Domain, ip string, cfg Config) (bool, string) {
	timeout := time.Duration(cfg.VerifyTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip, "443"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.Host+d.Endpoint, nil)
	if err != nil { return false, "request error" }
	req.Header.Set("User-Agent", "CFHost/0.4")
	resp, err := client.Do(req)
	if err != nil { return false, err.Error() }
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode < 100 || resp.StatusCode > 599 { return false, fmt.Sprintf("HTTP %d", resp.StatusCode) }
	return true, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

func verifyHTTPDomain(parent context.Context, d Domain, ip string, cfg Config) (bool, string) {
	if !cfg.Verify.StrictHTTP { return verifyHTTPConnectivity(parent, d, ip, cfg) }
	var last string
	for attempt := 1; attempt <= cfg.Verify.HTTPRetries; attempt++ {
		ok, detail := strictHTTPAttempt(parent, d, ip, cfg)
		last = detail
		if ok { return true, detail + fmt.Sprintf(" · attempt %d", attempt) }
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
	finalURL := resp.Request.URL.String()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("HTTP %d final=%s", resp.StatusCode, finalURL)
	}
	pattern, _ := regexp.Compile("(?i)" + cfg.Verify.BlockPatterns)
	if pattern != nil && pattern.Find(body) != nil {
		return false, fmt.Sprintf("HTTP %d challenge-or-placeholder final=%s", resp.StatusCode, finalURL)
	}
	if resp.StatusCode == http.StatusOK && len(body) < cfg.Verify.MinBodyBytes {
		return false, fmt.Sprintf("HTTP 200 body-too-small=%d final=%s", len(body), finalURL)
	}
	return true, fmt.Sprintf("HTTP %d bytes=%d final=%s", resp.StatusCode, len(body), finalURL)
}
