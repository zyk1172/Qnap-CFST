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
