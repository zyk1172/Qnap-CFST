package main

import (
	"strings"
	"testing"
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
