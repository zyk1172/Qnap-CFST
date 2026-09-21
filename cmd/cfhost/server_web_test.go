package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebAssetsEmbedded(t *testing.T) {
	for _, name := range []string{
		"web/index.html",
		"web/css/theme.css",
		"web/css/app.css",
		"web/js/core.js",
		"web/js/dashboard-domains.js",
		"web/js/candidates-ops-logs.js",
		"web/js/settings.js",
		"web/js/interactions.js",
		"web/js/events.js",
	} {
		content, err := webAssets.ReadFile(name)
		if err != nil {
			t.Fatalf("embedded asset %s missing: %v", name, err)
		}
		if len(content) < 100 {
			t.Fatalf("embedded asset %s is unexpectedly small", name)
		}
	}
}

func TestRoutesServeWebUI(t *testing.T) {
	app := &App{
		config: defaultConfig(),
		state: RuntimeState{
			Mappings:     map[string]string{},
			DomainStatus: map[string]string{},
			DomainHealth: map[string]DomainHealth{},
		},
	}
	cases := []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/", "text/html", "CFHOST"},
		{"/css/theme.css", "text/css", "--primary: #8d51f9"},
		{"/js/core.js", "javascript", "PAGE_META"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		app.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d", tc.path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), tc.contentType) {
			t.Fatalf("%s content type %q does not contain %q", tc.path, rec.Header().Get("Content-Type"), tc.contentType)
		}
		if !strings.Contains(rec.Body.String(), tc.contains) {
			t.Fatalf("%s missing expected marker %q", tc.path, tc.contains)
		}
	}
}

func TestAPIRoutesStillTakePrecedenceOverStaticFiles(t *testing.T) {
	app := &App{
		config: defaultConfig(),
		state: RuntimeState{
			Mappings:     map[string]string{},
			DomainStatus: map[string]string{},
			DomainHealth: map[string]DomainHealth{},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status returned %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status content type is %q", rec.Header().Get("Content-Type"))
	}
}

func TestStaticUIRejectsNonGetMethods(t *testing.T) {
	app := &App{
		config: defaultConfig(),
		state: RuntimeState{
			Mappings:     map[string]string{},
			DomainStatus: map[string]string{},
			DomainHealth: map[string]DomainHealth{},
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}


func TestDashboardPollingDoesNotRestartCounterAnimation(t *testing.T) {
	core, err := webAssets.ReadFile("web/js/core.js")
	if err != nil {
		t.Fatal(err)
	}
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil {
		t.Fatal(err)
	}

	coreJS := string(core)
	eventsJS := string(events)

	if !strings.Contains(coreJS, "renderPage(false, false)") {
		t.Fatal("status polling must render without dashboard counter animation")
	}
	if !strings.Contains(coreJS, "nextSignature !== store.renderSignature") {
		t.Fatal("status polling must skip unchanged page rerenders")
	}
	if !strings.Contains(coreJS, "store.renderSignature = pageRefreshSignature()") {
		t.Fatal("rendered page signature must be recorded")
	}
	if !strings.Contains(eventsJS, "renderPage(false, true)") {
		t.Fatal("initial page load should retain dashboard counter animation")
	}
}
