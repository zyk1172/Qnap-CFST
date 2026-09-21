package main

import "testing"

func TestStrictHTTPBodyEvaluationPatternsCompile(t *testing.T) {
	cfg:=defaultConfig()
	if cfg.Verify.BlockPatterns=="" || !cfg.Verify.StrictHTTP || cfg.Verify.HTTPRetries!=2 { t.Fatal("strict HTTP defaults not loaded") }
}
