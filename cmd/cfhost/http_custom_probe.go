package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const probeSecretDir = "/run/secrets/cfhost"
const maxProbeBytes = 128 * 1024

var probeEnvName = regexp.MustCompile(`^CFHOST_PROBE_[A-Z0-9_]+$`)
var probeFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var probeHeaderName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

func validateProbeRef(ref string) error {
	switch {
	case strings.HasPrefix(ref, "env:"):
		if !probeEnvName.MatchString(strings.TrimPrefix(ref, "env:")) {
			return errors.New("environment reference must use CFHOST_PROBE_ followed by uppercase letters, digits or underscore")
		}
	case strings.HasPrefix(ref, "file:"):
		name := strings.TrimPrefix(ref, "file:")
		if !probeFileName.MatchString(name) || name == "." || name == ".." {
			return errors.New("file reference must be a simple name in /run/secrets/cfhost")
		}
	default:
		return errors.New("use an env: or file: reference")
	}
	return nil
}

func validateCustomHTTPProbe(d Domain) error {
	p := d.HTTPProbe
	if p == nil {
		return nil
	}
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "POST" {
		return errors.New("method must be GET or POST")
	}
	if strings.ContainsAny(d.Endpoint, "?#") || strings.ContainsAny(d.Endpoint, "\r\n") {
		return errors.New("custom probe path cannot contain query, fragment or control characters")
	}
	if len(p.Headers) > 12 {
		return errors.New("at most 12 probe headers are allowed")
	}
	seen := map[string]bool{}
	for _, h := range p.Headers {
		key := strings.ToLower(strings.TrimSpace(h.Name))
		if !probeHeaderName.MatchString(h.Name) || seen[key] {
			return errors.New("invalid or duplicate header name")
		}
		switch key {
		case "host", "connection", "content-length", "transfer-encoding", "proxy-authorization":
			return errors.New("restricted header name")
		}
		if err := validateProbeRef(h.ValueRef); err != nil {
			return err
		}
		seen[key] = true
	}
	if p.BodyRef != "" {
		if method != "POST" {
			return errors.New("body reference requires POST")
		}
		if err := validateProbeRef(p.BodyRef); err != nil {
			return err
		}
	}
	if p.Body != "" {
		return errors.New("literal request bodies are not supported; use bodyRef")
	}
	if p.ContentType != "" && p.ContentType != "application/json" && p.ContentType != "text/plain" && p.ContentType != "application/x-www-form-urlencoded" {
		return errors.New("unsupported content type")
	}
	if len(p.BodyRegex) > 1024 {
		return errors.New("response regex is too long")
	}
	if p.BodyRegex != "" {
		if _, err := regexp.Compile(p.BodyRegex); err != nil {
			return errors.New("invalid response regex")
		}
	}
	return nil
}

func readProbeRef(ref, host string) (string, error) {
	return readProbeRefInDir(ref, host, probeSecretDir)
}

func readProbeRefInDir(ref, host, secretDir string) (string, error) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if validateDomainHost(host) != nil {
		return "", errors.New("invalid probe origin")
	}
	if err := validateProbeRef(ref); err != nil {
		return "", err
	}
	if strings.HasPrefix(ref, "env:") {
		name := strings.TrimPrefix(ref, "env:")
		allowedHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(os.Getenv(name+"_HOST")), "."))
		if allowedHost == "" || allowedHost != host {
			return "", errors.New("probe reference unavailable for this host")
		}
		value, ok := os.LookupEnv(name)
		if !ok || len(value) == 0 || len(value) > maxProbeBytes {
			return "", errors.New("probe reference unavailable or too large")
		}
		return value, nil
	}
	hostDir := filepath.Join(secretDir, host)
	dirInfo, err := os.Lstat(hostDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("probe reference unavailable for this host")
	}
	path := filepath.Join(hostDir, strings.TrimPrefix(ref, "file:"))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("probe reference unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("probe reference unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("probe reference changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxProbeBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxProbeBytes {
		return "", errors.New("probe reference unavailable or too large")
	}
	return string(b), nil
}

func customProbeAttempt(ctx context.Context, d Domain, client *http.Client, baseURL, blockPatterns string) (bool, string) {
	if err := validateCustomHTTPProbe(d); err != nil {
		return false, "invalid custom HTTP probe"
	}
	p := d.HTTPProbe
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = "GET"
	}
	var body io.Reader
	if p.BodyRef != "" {
		value, err := readProbeRef(p.BodyRef, d.Host)
		if err != nil {
			return false, "request body reference unavailable"
		}
		body = strings.NewReader(value)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+d.Endpoint, body)
	if err != nil {
		return false, "custom HTTP request invalid"
	}
	req.Header.Set("User-Agent", "CFHost/"+cfhostVersion)
	if p.ContentType != "" {
		req.Header.Set("Content-Type", p.ContentType)
	}
	for _, h := range p.Headers {
		value, err := readProbeRef(h.ValueRef, d.Host)
		if err != nil {
			return false, "request header reference unavailable"
		}
		value = strings.TrimRight(value, "\r\n")
		if strings.ContainsAny(value, "\r\n\x00") {
			return false, "request header reference invalid"
		}
		req.Header.Set(h.Name, value)
	}
	origin := req.URL
	// Even custom headers such as X-API-Key must never cross scheme, host or port.
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 10 {
			return errors.New("too many redirects")
		}
		if next.URL.Scheme != "https" || !strings.EqualFold(next.URL.Host, origin.Host) || next.URL.Scheme != origin.Scheme {
			return errors.New("redirect outside probe origin")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, "custom HTTP request timed out or cancelled"
		}
		return false, "custom HTTP request failed (connection, TLS or redirect)"
	}
	defer resp.Body.Close()
	result := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, result + " · custom probe rejected status"
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBytes+1))
	if err != nil || len(data) > maxProbeBytes {
		return false, result + " · response unavailable or exceeds limit"
	}
	if blockPatterns != "" {
		pattern, err := regexp.Compile("(?i)" + blockPatterns)
		if err == nil && pattern.Match(data) {
			return false, result + " · challenge or placeholder"
		}
	}
	if p.BodyRegex != "" {
		pattern, _ := regexp.Compile(p.BodyRegex)
		if !pattern.Match(data) {
			return false, result + " · response did not match configured pattern"
		}
	}
	return true, result + " · custom probe passed"
}

func verifyCustomHTTPProbe(parent context.Context, d Domain, ip string, cfg Config) (bool, string) {
	timeout := time.Duration(cfg.VerifyTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	tr := &http.Transport{ForceAttemptHTTP2: true}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil && strings.EqualFold(host, d.Host) && port == "443" {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		}
		return dialer.DialContext(ctx, network, address)
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: timeout}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return customProbeAttempt(ctx, d, client, "https://"+d.Host, cfg.Verify.BlockPatterns)
}
