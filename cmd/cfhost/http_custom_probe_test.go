package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCustomHTTPProbeAuthenticatedPostAndResponse(t *testing.T) {
	const secret = "test-api-key-never-print"
	t.Setenv("CFHOST_PROBE_TEST_KEY", secret)
	t.Setenv("CFHOST_PROBE_TEST_KEY_HOST", "api.example")
	t.Setenv("CFHOST_PROBE_TEST_BODY", `{"limit":1}`)
	t.Setenv("CFHOST_PROBE_TEST_BODY_HOST", "api.example")
	called := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != "POST" || r.Header.Get("X-Api-Key") != secret || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request metadata incorrect")
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"limit":1}` {
			t.Error("body differs")
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	d := Domain{Host: "api.example", Mode: "http", Class: "latency", Endpoint: "/api/search", HTTPProbe: &HTTPProbeConfig{
		Method: "POST", BodyRef: "env:CFHOST_PROBE_TEST_BODY", ContentType: "application/json", BodyRegex: `"code"\s*:\s*0`,
		Headers: []HTTPProbeHeader{{Name: "X-Api-Key", ValueRef: "env:CFHOST_PROBE_TEST_KEY"}},
	}}
	ok, detail := customProbeAttempt(context.Background(), d, server.Client(), server.URL, defaultConfig().Verify.BlockPatterns)
	if !called || !ok {
		t.Fatalf("authorized probe failed: %s", detail)
	}
	if strings.Contains(detail, secret) || strings.Contains(detail, "limit") {
		t.Fatal("diagnostic included sensitive data")
	}
	d.HTTPProbe.BodyRegex = `"code"\s*:\s*1`
	if ok, _ := customProbeAttempt(context.Background(), d, server.Client(), server.URL, defaultConfig().Verify.BlockPatterns); ok {
		t.Fatal("wrong application result accepted")
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("config exposed credential")
	}
}

func TestCustomProbePreventsCredentialForwardingAcrossOrigins(t *testing.T) {
	t.Setenv("CFHOST_PROBE_TEST_KEY", "test-only-value")
	t.Setenv("CFHOST_PROBE_TEST_KEY_HOST", "api.example")
	var reached atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	d := Domain{Host: "api.example", Mode: "http", Class: "latency", Endpoint: "/api", HTTPProbe: &HTTPProbeConfig{Headers: []HTTPProbeHeader{{Name: "X-API-Key", ValueRef: "env:CFHOST_PROBE_TEST_KEY"}}}}
	ok, detail := customProbeAttempt(context.Background(), d, source.Client(), source.URL, defaultConfig().Verify.BlockPatterns)
	if ok || reached.Load() != 0 {
		t.Fatalf("redirect leaked credentials: %v %d", ok, reached.Load())
	}
	if strings.Contains(detail, target.URL) || strings.Contains(detail, "test-only-value") {
		t.Fatal("diagnostic leaked URL or credential")
	}
}

func TestCustomProbeRejectsInvalidConfigurationAndMissingSecrets(t *testing.T) {
	cases := []HTTPProbeConfig{
		{Method: "DELETE"},
		{Body: "secret"},
		{Method: "GET", BodyRef: "env:CFHOST_PROBE_KEY"},
		{Headers: []HTTPProbeHeader{{Name: "X-Key", ValueRef: "env:HOME"}}},
		{Headers: []HTTPProbeHeader{{Name: "Host", ValueRef: "env:CFHOST_PROBE_KEY"}}},
		{Headers: []HTTPProbeHeader{{Name: "X-Key", ValueRef: "file:../token"}}},
		{BodyRegex: "("},
	}
	for _, p := range cases {
		d := Domain{Host: "api.example", Mode: "http", Class: "latency", Endpoint: "/api", HTTPProbe: &p}
		if err := validateCustomHTTPProbe(d); err == nil {
			t.Fatalf("accepted invalid probe: %+v", p)
		}
	}
	d := Domain{Host: "api.example", Mode: "http", Class: "latency", Endpoint: "/api", HTTPProbe: &HTTPProbeConfig{Headers: []HTTPProbeHeader{{Name: "X-Key", ValueRef: "env:CFHOST_PROBE_MISSING_TEST"}}}}
	if ok, detail := customProbeAttempt(context.Background(), d, http.DefaultClient, "https://example.invalid", defaultConfig().Verify.BlockPatterns); ok || !strings.Contains(detail, "unavailable") {
		t.Fatalf("missing credential not reported: %v %q", ok, detail)
	}
	d.Endpoint = "/api?token=hidden"
	if err := validateCustomHTTPProbe(d); err == nil {
		t.Fatal("credential-bearing query path accepted")
	}
}

func TestProbeSecretFilesAreBoundedAndNotSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "api.example"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api.example", "auth"), []byte("secret-value"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readProbeRefInDir("file:auth", "api.example", dir)
	if err != nil || value != "secret-value" {
		t.Fatal("could not read permitted file")
	}
	if err := os.Symlink(filepath.Join(dir, "api.example", "auth"), filepath.Join(dir, "api.example", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readProbeRefInDir("file:link", "api.example", dir); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := readProbeRefInDir("file:../auth", "api.example", dir); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "api.example", "large"), make([]byte, maxProbeBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProbeRefInDir("file:large", "api.example", dir); err == nil {
		t.Fatal("oversize file accepted")
	}
}

func TestProbeReferencesAreBoundToTheirConfiguredHost(t *testing.T) {
	t.Setenv("CFHOST_PROBE_SCOPE_TEST", "opaque")
	t.Setenv("CFHOST_PROBE_SCOPE_TEST_HOST", "api.example")
	if got, err := readProbeRef("env:CFHOST_PROBE_SCOPE_TEST", "api.example"); err != nil || got != "opaque" {
		t.Fatal("bound origin cannot read reference")
	}
	if _, err := readProbeRef("env:CFHOST_PROBE_SCOPE_TEST", "other.example"); err == nil {
		t.Fatal("other origin read environment credential")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "api.example"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api.example", "key"), []byte("opaque"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProbeRefInDir("file:key", "other.example", dir); err == nil {
		t.Fatal("other origin read file credential")
	}
	if _, err := readProbeRefInDir("file:key", "../api.example", dir); err == nil {
		t.Fatal("host path traversal accepted")
	}
}

func TestCustomProbeRejectsChallengeAndUnknownBusinessResult(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Just a moment... checking your browser"))
	}))
	defer server.Close()
	d := Domain{Host: "api.example", Mode: "http", Class: "latency", Endpoint: "/", HTTPProbe: &HTTPProbeConfig{}}
	if ok, _ := customProbeAttempt(context.Background(), d, server.Client(), server.URL, defaultConfig().Verify.BlockPatterns); ok {
		t.Fatal("200 challenge page accepted")
	}
}

func TestCustomProbeCannotBeAttachedToUnverifiedStrategy(t *testing.T) {
	cfg := defaultConfig()
	cfg.Domains = []Domain{{Host: "api.example", Class: "normal", Mode: "http", Endpoint: "/", Enabled: true, HTTPProbe: &HTTPProbeConfig{}}}
	if err := normalizeConfig(&cfg); err == nil {
		t.Fatal("normal strategy accepted a skipped custom probe")
	}
}

func TestCustomProbeUnauthorizedDoesNotPass(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid credential", http.StatusForbidden)
	}))
	defer srv.Close()
	d := Domain{Host: "api.example", Mode: "http", Class: "latency", Endpoint: "/", HTTPProbe: &HTTPProbeConfig{}}
	ok, detail := customProbeAttempt(context.Background(), d, srv.Client(), srv.URL, defaultConfig().Verify.BlockPatterns)
	if ok || !strings.Contains(detail, "403") || strings.Contains(detail, "invalid credential") {
		t.Fatalf("unexpected result %v %q", ok, detail)
	}
}
