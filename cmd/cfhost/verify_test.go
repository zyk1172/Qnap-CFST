package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStrictHTTPBodyEvaluationPatternsCompile(t *testing.T) {
	cfg:=defaultConfig()
	if cfg.Verify.BlockPatterns=="" || !cfg.Verify.StrictHTTP || cfg.Verify.HTTPRetries!=2 { t.Fatal("strict HTTP defaults not loaded") }
}

func TestEvaluateStrictHTTPResponse(t *testing.T) {
	cfg:=defaultConfig()
	good:=[]byte(strings.Repeat("ok", 400))
	if ok,_:=evaluateStrictHTTPResponse(200,good,"https://example.com/",cfg);!ok{t.Fatal("valid 200 page rejected")}
	if ok,_:=evaluateStrictHTTPResponse(403,good,"https://example.com/",cfg);ok{t.Fatal("403 accepted")}
	if ok,_:=evaluateStrictHTTPResponse(200,[]byte("Just a moment... checking your browser"),"https://example.com/",cfg);ok{t.Fatal("challenge page accepted")}
	if ok,_:=evaluateStrictHTTPResponse(200,[]byte("short"),"https://example.com/",cfg);ok{t.Fatal("undersized 200 page accepted")}
	if ok,_:=evaluateStrictHTTPResponse(204,nil,"https://example.com/api",cfg);!ok{t.Fatal("valid 204 response rejected")}
}

func TestEvaluateHTTPConnectivityStatusPolicy(t *testing.T) {
	reachable := []int{200, 204, 301, 302, 400, 401, 404, 405, 409, 410, 422}
	for _, status := range reachable {
		if got := classifyHTTPConnectivityStatus(status); got != httpProbeReachable {
			t.Fatalf("HTTP %d must remain reachable evidence, got disposition=%v", status, got)
		}
		if ok, _ := evaluateHTTPConnectivityStatus(status); !ok {
			t.Fatalf("HTTP %d must pass relaxed connectivity verification", status)
		}
	}

	hardFailures := []int{403, 421, 451}
	for _, status := range hardFailures {
		if got := classifyHTTPConnectivityStatus(status); got != httpProbeHardFailure {
			t.Fatalf("HTTP %d must be a hard failure, got disposition=%v", status, got)
		}
		if ok, detail := evaluateHTTPConnectivityStatus(status); ok || !strings.Contains(detail, "blocked/unusable") {
			t.Fatalf("HTTP %d must fail as blocked/unusable: ok=%v detail=%q", status, ok, detail)
		}
	}

	temporaryFailures := []int{408, 425, 429, 500, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 530}
	for _, status := range temporaryFailures {
		if got := classifyHTTPConnectivityStatus(status); got != httpProbeTemporaryFailure {
			t.Fatalf("HTTP %d must be a temporary failure, got disposition=%v", status, got)
		}
		if ok, detail := evaluateHTTPConnectivityStatus(status); ok || !strings.Contains(detail, "temporary failure") {
			t.Fatalf("HTTP %d must fail temporarily: ok=%v detail=%q", status, ok, detail)
		}
	}
}


func TestHTTPStrategiesRejectForbiddenButNormalStillSkipsVerification(t *testing.T) {
	cfg := defaultConfig()

	for _, class := range []string{"latency", "bandwidth"} {
		d := Domain{Host:"example.com", Class:class, Mode:"http", Endpoint:"/", Enabled:true}
		if got := classifyHTTPConnectivityStatus(http.StatusForbidden); got != httpProbeHardFailure {
			t.Fatalf("%s HTTP strategy must classify 403 as hard failure, got %v", class, got)
		}
		if ok, detail := evaluateHTTPConnectivityStatus(http.StatusForbidden); ok || !strings.Contains(detail, "blocked/unusable") {
			t.Fatalf("%s HTTP strategy must reject 403: ok=%v detail=%q", class, ok, detail)
		}
		if ok, _ := evaluateStrictHTTPResponse(http.StatusForbidden, []byte("forbidden"), "https://example.com/login", cfg); ok {
			t.Fatalf("%s strict HTTP strategy must reject final 403", class)
		}
		_ = d
	}

	normal := Domain{Host:"example.com", Class:"normal", Mode:"http", Endpoint:"/", Enabled:true}
	ok, detail := verifyConfiguredDomain(t.Context(), normal, "192.0.2.1", cfg, nil)
	if !ok || !strings.Contains(detail, "verification skipped") {
		t.Fatalf("normal HTTP must remain unprobed: ok=%v detail=%q", ok, detail)
	}
}

func TestVerificationHardFailureClassification(t *testing.T) {
	hard := []string{
		"HTTP 403 final=https://example.com/",
		"HTTP 421 final=https://example.com/",
		"tracker HTTP 451",
		"HTTP 403 · blocked/unusable",
	}
	for _, detail := range hard {
		if !verificationHardFailure(detail) {
			t.Fatalf("expected hard failure for %q", detail)
		}
	}
	soft := []string{
		"HTTP 429 · temporary failure",
		"HTTP 503 final=https://example.com/",
		"context deadline exceeded",
		"tracker invalid response",
	}
	for _, detail := range soft {
		if verificationHardFailure(detail) {
			t.Fatalf("unexpected hard failure for %q", detail)
		}
	}
}

func TestDomainVerificationContextSharesRemainingBudget(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child, childCancel, budget, ok := domainVerificationContext(parent, 4)
	if !ok {
		t.Fatal("expected domain budget")
	}
	defer childCancel()
	if budget < 3*time.Second || budget > 4*time.Second {
		t.Fatalf("unexpected budget: %s", budget)
	}
	if _, ok := child.Deadline(); !ok {
		t.Fatal("domain context must have a deadline")
	}
}
