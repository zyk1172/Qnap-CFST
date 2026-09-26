package main

import (
	"net/http"
	"os"
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
		"web/assets/cfhost-logo.svg",
		"web/assets/cfhost-logo.png",
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
		{"/assets/cfhost-logo.svg", "image/svg+xml", "<svg"},
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


func TestAppearancePopoverSurvivesSidebarToggle(t *testing.T) {
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil {
		t.Fatal(err)
	}
	index, err := webAssets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	eventsJS := string(events)
	indexHTML := string(index)

	const toggle = `data-action="open-appearance"`
	toggles := strings.Count(indexHTML, toggle)
	if toggles < 2 {
		t.Fatalf("expected at least 2 appearance toggles in index.html, found %d", toggles)
	}
	anchor := strings.Index(indexHTML, `class="popover-anchor"`)
	firstToggle := strings.Index(indexHTML, toggle)
	if anchor < 0 || firstToggle < 0 || firstToggle > anchor {
		t.Fatal("expected an appearance toggle outside .popover-anchor (sidebar footer)")
	}
	if !strings.Contains(eventsJS, "action !== 'open-appearance'") {
		t.Fatal("outside-click must not close the popover opened by an appearance toggle")
	}
}


func TestDesktopSidebarCannotCollapse(t *testing.T) {
	index, err := webAssets.ReadFile("web/index.html")
	if err != nil { t.Fatal(err) }
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil { t.Fatal(err) }
	interactions, err := webAssets.ReadFile("web/js/interactions.js")
	if err != nil { t.Fatal(err) }
	css, err := webAssets.ReadFile("web/css/app.css")
	if err != nil { t.Fatal(err) }

	indexHTML := string(index)
	eventsJS := string(events)
	interactionsJS := string(interactions)
	cssText := string(css)

	for _, forbidden := range []string{
		`data-action="toggle-sidebar"`,
		`sidebar-collapse`,
		`切换侧栏宽度`,
		`function toggleSidebar()`,
		`sidebar-collapsed .sidebar`,
	} {
		if strings.Contains(indexHTML, forbidden) || strings.Contains(eventsJS, forbidden) || strings.Contains(interactionsJS, forbidden) || strings.Contains(cssText, forbidden) {
			t.Fatalf("desktop sidebar collapse interaction must be removed: %q", forbidden)
		}
	}
	if !strings.Contains(eventsJS, "localStorage.removeItem('cfhost-sidebar-collapsed')") {
		t.Fatal("boot must clear obsolete persisted collapsed-sidebar preference")
	}
	for _, required := range []string{`data-action="open-sidebar"`, `data-action="close-sidebar"`} {
		if !strings.Contains(indexHTML, required) {
			t.Fatalf("mobile sidebar interaction must remain: %q", required)
		}
	}
}

func TestSingleColumnGridTracksClampMinimum(t *testing.T) {
	css, err := webAssets.ReadFile("web/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	cssText := string(css)
	for _, selector := range []string{
		".split-main",
		".grid.cols-4, .grid.cols-3, .grid.cols-2",
		".settings-layout",
		".form-grid",
	} {
		want := selector + " { grid-template-columns: minmax(0, 1fr); }"
		if !strings.Contains(cssText, want) {
			t.Fatalf("single-column override must clamp the track minimum: %q", want)
		}
	}
	if strings.Contains(cssText, "grid-template-columns: 1fr;") {
		t.Fatal("bare 1fr single-column tracks can overflow narrow viewports")
	}
}


func TestSearchInputsPatchResultsInsteadOfRerendering(t *testing.T) {
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil {
		t.Fatal(err)
	}
	eventsJS := string(events)
	for id, updater := range map[string]string{
		"domain-search": "updateDomainResults()",
		"log-search":    "updateLogResults()",
	} {
		if !strings.Contains(eventsJS, updater) {
			t.Fatalf("%s must patch results instead of re-rendering", id)
		}
	}
	start := strings.Index(eventsJS, "document.addEventListener('input'")
	end := strings.Index(eventsJS, "event.target.id === 'command-input'")
	if start < 0 || end < 0 || end < start {
		t.Fatal("search input handler not found in events.js")
	}
	handler := eventsJS[start:end]
	if strings.Contains(handler, "renderPage(") {
		t.Fatal("search input must not re-render the page and destroy its own focus")
	}
	if strings.Contains(handler, "requestAnimationFrame") {
		t.Fatal("deferred search focus restore drops fast keystrokes")
	}

	// The partial updaters and the full renderers must share their markup
	// builders, otherwise the two paths silently drift apart.
	for file, builders := range map[string][]string{
		"web/js/dashboard-domains.js":   {"function domainRowsHTML(", "${domainRowsHTML(domains, filtered)}", "rows.innerHTML = domainRowsHTML(domains, filtered)"},
		"web/js/candidates-ops-logs.js": {"function logLinesHTML(", "${logLinesHTML(filtered)}", "content.innerHTML = logLinesHTML(filtered)"},
	} {
		content, err := webAssets.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range builders {
			if !strings.Contains(string(content), needle) {
				t.Fatalf("%s missing %q", file, needle)
			}
		}
	}
}

// A run can report success while individual domains stay unresolved, so both
// the dashboard and the history table must surface that instead of showing a
// clean success the operator would never look into.
func TestUnresolvedDomainsAreSurfacedInUI(t *testing.T) {
	for file, needles := range map[string][]string{
		"web/js/dashboard-domains.js": {
			"function unresolvedDomainsNow(",
			"function unresolvedNotice(",
			"item.unresolvedCount",
			"item.unresolvedDomains",
		},
		"web/js/candidates-ops-logs.js": {
			"function historyRow(",
			"item.unresolvedCount",
			"部分完成",
		},
	} {
		content, err := webAssets.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range needles {
			if !strings.Contains(string(content), needle) {
				t.Fatalf("%s missing %q", file, needle)
			}
		}
	}
}


func TestShellStatusSurfacesUnresolvedDomains(t *testing.T) {
	core, err := webAssets.ReadFile("web/js/core.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := webAssets.ReadFile("web/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	coreJS := string(core)
	if !strings.Contains(coreJS, "const unresolved = counts.failed > 0") ||
		!strings.Contains(coreJS, "'有域名待处理'") ||
		!strings.Contains(coreJS, "'is-warning'") {
		t.Fatal("shell status must surface unresolved enabled domains")
	}
	if !strings.Contains(string(css), ".status-dot.is-warning") {
		t.Fatal("warning shell status must have a dedicated status-dot style")
	}
}


func TestDomainTableShowsTrackerSampleStateAndMaintenance(t *testing.T) {
	domains, err := webAssets.ReadFile("web/js/dashboard-domains.js")
	if err != nil { t.Fatal(err) }
	core, err := webAssets.ReadFile("web/js/core.js")
	if err != nil { t.Fatal(err) }
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil { t.Fatal(err) }
	interactions, err := webAssets.ReadFile("web/js/interactions.js")
	if err != nil { t.Fatal(err) }

	domainsJS := string(domains)
	coreJS := string(core)
	eventsJS := string(events)
	interactionsJS := string(interactions)

	for _, want := range []string{"data-maintain-domain", "trackerSampleMeta(domain)", "data-domain-expand", "domain-detail-panel"} {
		if !strings.Contains(domainsJS, want) {
			t.Fatalf("domain table missing %q", want)
		}
	}
	for _, want := range []string{"CFHost 待探测", "CFHost 探测通过", "CFHost 探测失败", "探测样本未获取"} {
		if !strings.Contains(coreJS, want) {
			t.Fatalf("sample status helper missing %q", want)
		}
	}
	if !strings.Contains(eventsJS, "data-maintain-domain") {
		t.Fatal("maintenance button is not wired")
	}
	if !strings.Contains(interactionsJS, "/api/domain-maintain") {
		t.Fatal("single-domain maintenance API is not invoked")
	}
}

func TestDomainPagePollingIncludesTrackerSampleState(t *testing.T) {
	core, err := webAssets.ReadFile("web/js/core.js")
	if err != nil { t.Fatal(err) }
	coreJS := string(core)
	for _, want := range []string{"trackerSamples: state.trackerSamples", "currentDomain: state.currentDomain"} {
		if !strings.Contains(coreJS, want) {
			t.Fatalf("domain refresh signature missing %q", want)
		}
	}
}


func TestDomainTableIsCompactExpandableAndNonScrolling(t *testing.T) {
	domains, err := webAssets.ReadFile("web/js/dashboard-domains.js")
	if err != nil { t.Fatal(err) }
	css, err := webAssets.ReadFile("web/css/app.css")
	if err != nil { t.Fatal(err) }
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil { t.Fatal(err) }

	domainsJS := string(domains)
	cssText := string(css)
	eventsJS := string(events)

	if !strings.Contains(domainsJS, "<th>域名</th><th>类型 / 策略</th><th>当前 IP</th><th>状态</th><th></th>") {
		t.Fatal("domain main table must keep only five compact columns")
	}
	for _, removed := range []string{"<th>站点组</th>", "<th>样本</th>", "<th>连续失败</th>"} {
		if strings.Contains(domainsJS, removed) {
			t.Fatalf("detail-only column leaked back into compact table: %q", removed)
		}
	}
	for _, want := range []string{
		"['配置',",
		"['当前 IP', ip || '—']",
		"['CFHost 探测', domain.mode === 'tracker' ? sample.label : httpProbe.label]",
		"['健康记录',",
	} {
		if !strings.Contains(domainsJS, want) {
			t.Fatalf("expanded detail is missing %q", want)
		}
	}
	for _, removed := range []string{"['站点组',", "['Endpoint',", "['类型 / 策略',", "['最近失败',"} {
		if strings.Contains(domainsJS, removed) {
			t.Fatalf("expanded detail must stay compact; obsolete card remains: %q", removed)
		}
	}
	for _, want := range []string{"function httpRuntimePanel(domain)", "最近成功", "最近失败", "无 HTTP 响应"} {
		if !strings.Contains(domainsJS, want) {
			t.Fatalf("HTTP runtime status UI is missing %q", want)
		}
	}
	if !strings.Contains(eventsJS, "const opening = store.expandedDomainHost !== domain.host") ||
		!strings.Contains(eventsJS, "store.expandedDomainHost = opening ? domain.host : ''") {
		t.Fatal("clicking a domain must toggle its detail row")
	}
	if !strings.Contains(cssText, ".domain-table-wrap { overflow: hidden; }") ||
		!strings.Contains(cssText, ".domain-table { table-layout: fixed; min-width: 0; }") {
		t.Fatal("domain table must not depend on horizontal scrolling")
	}
	if !strings.Contains(cssText, `grid-template-areas:
      "host actions"
      "meta actions"
      "ip status";`) {
		t.Fatal("mobile domain rows must switch to a compact grid")
	}
}

func TestDomainTrackerFilterSortAndRuntimeDetails(t *testing.T) {
	domains, err := webAssets.ReadFile("web/js/dashboard-domains.js")
	if err != nil { t.Fatal(err) }
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil { t.Fatal(err) }
	interactions, err := webAssets.ReadFile("web/js/interactions.js")
	if err != nil { t.Fatal(err) }

	domainsJS := string(domains)
	eventsJS := string(events)
	interactionsJS := string(interactions)

	for _, want := range []string{
		`data-domain-filter="tracker"`,
		`store.domainFilter === 'tracker' && (domain.mode !== 'tracker' || domain.class === 'follow')`,
		`Number(b.domain.mode === 'tracker' && b.domain.class !== 'follow') - Number(a.domain.mode === 'tracker' && a.domain.class !== 'follow')`,
		`Transmission 状态`,
		`trackerStatus === 'Working'`,
		`部分失败`,
		`const latestIssue = issues[0]`,
		`Tracker 连接`,
		`Repair / 保活`,
		`最近 Tracker 返回`,
	} {
		if !strings.Contains(domainsJS, want) {
			t.Fatalf("Tracker domain UI is missing %q", want)
		}
	}
	for _, removed := range []string{
		`扫描范围`,
		`来源</span><strong>Transmission 活跃种子聚合`,
		`下次状态检查`,
		`最早下次重新汇报`,
		`状态说明`,
	} {
		if strings.Contains(domainsJS, removed) {
			t.Fatalf("Tracker expanded detail must stay concise; obsolete field remains: %q", removed)
		}
	}
	if !strings.Contains(eventsJS, "if (opening && domain.mode === 'tracker' && domain.class !== 'follow') await loadTrackerRuntime(domain)") {
		t.Fatal("Tracker runtime must load lazily when a Tracker row is expanded")
	}
	if !strings.Contains(interactionsJS, "/api/tracker-runtime?host=") {
		t.Fatal("Tracker expansion must query the runtime endpoint")
	}
}

func TestDomainFollowStrategyUI(t *testing.T) {
	interactions, err := webAssets.ReadFile("web/js/interactions.js")
	if err != nil { t.Fatal(err) }
	domains, err := webAssets.ReadFile("web/js/dashboard-domains.js")
	if err != nil { t.Fatal(err) }
	settings, err := webAssets.ReadFile("web/js/settings.js")
	if err != nil { t.Fatal(err) }
	events, err := webAssets.ReadFile("web/js/events.js")
	if err != nil { t.Fatal(err) }

	interactionsJS := string(interactions)
	domainsJS := string(domains)
	settingsJS := string(settings)
	eventsJS := string(events)

	for _, want := range []string{
		`<label>跟随域名</label><select name="follow"`,
		`value="follow"`,
		`follow.disabled = !active`,
		`follow.required = active`,
		`follow: strategy === 'follow'`,
		`.filter(domain => domain.host !== current.host && domain.class !== 'follow')`,
		`item.follow === previous.host`,
		`正被 ${followers.map(item => item.host).join('、')} 跟随`,
	} {
		if !strings.Contains(interactionsJS, want) {
			t.Fatalf("follow domain editor is missing %q", want)
		}
	}
	if strings.Contains(interactionsJS, `name="group"`) || strings.Contains(interactionsJS, "站点组") {
		t.Fatal("legacy site-group field must be removed from domain editor")
	}
	for _, want := range []string{
		`domain.class === 'follow' ? '跟随' : 'LAT'`,
		`['跟随域名', domain.follow || '—']`,
		`follow · 直接复用目标域名当前 IP`,
		`同步跟随域名当前 IP`,
	} {
		if !strings.Contains(domainsJS, want) {
			t.Fatalf("follow domain list is missing %q", want)
		}
	}
	if !strings.Contains(settingsJS, "follow：不独立测速或验证") {
		t.Fatal("settings must explain follow strategy")
	}
	if !strings.Contains(eventsJS, "domain.class !== 'follow'") {
		t.Fatal("follow Tracker domains must not load Transmission runtime")
	}
}

func TestDomainActionsAreIconOnlyAndColorCoded(t *testing.T) {
	domains, err := webAssets.ReadFile("web/js/dashboard-domains.js")
	if err != nil { t.Fatal(err) }
	css, err := webAssets.ReadFile("web/css/app.css")
	if err != nil { t.Fatal(err) }

	domainsJS := string(domains)
	cssText := string(css)
	for _, cls := range []string{
		"domain-action maintain",
		"domain-action edit",
		"domain-action toggle",
		"domain-action delete",
	} {
		if !strings.Contains(domainsJS, cls) {
			t.Fatalf("missing color-coded action class %q", cls)
		}
	}
	if strings.Contains(domainsJS, `>${maintaining ? icon('activity') : icon('repair')}${maintaining ? '维护中' : '维护'}</button>`) {
		t.Fatal("maintenance action must be icon-only")
	}
	for _, selector := range []string{
		".domain-action.maintain",
		".domain-action.edit",
		".domain-action.toggle",
		".domain-action.delete",
	} {
		if !strings.Contains(cssText, selector) {
			t.Fatalf("missing action color style %q", selector)
		}
	}
}


func TestSidebarShowsLiveJobProgress(t *testing.T) {
	index, err := webAssets.ReadFile("web/index.html")
	if err != nil { t.Fatal(err) }
	core, err := webAssets.ReadFile("web/js/core.js")
	if err != nil { t.Fatal(err) }
	css, err := webAssets.ReadFile("web/css/app.css")
	if err != nil { t.Fatal(err) }

	indexHTML := string(index)
	coreJS := string(core)
	cssText := string(css)

	for _, want := range []string{
		"id=\"sidebar-subtitle-text\"",
		"id=\"sidebar-subtitle-copy\"",
		"id=\"sidebar-progress-track\"",
		"id=\"sidebar-progress-fill\"",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("sidebar progress markup missing %q", want)
		}
	}
	for _, want := range []string{
		"function liveProgressText()",
		"function updateSidebarProgress(",
		"progress.step",
		"progress.steps",
		"progress.percent",
		"progress.detail",
	} {
		if !strings.Contains(coreJS, want) {
			t.Fatalf("sidebar progress logic missing %q", want)
		}
	}
	if strings.Contains(coreJS, "$('#sidebar-subtitle').textContent") {
		t.Fatal("sidebar subtitle must preserve its marquee child structure")
	}
	for _, want := range []string{
		".sidebar-subtitle.is-progress .sidebar-subtitle-track",
		"@keyframes sidebar-runtime-scroll",
		".sidebar-progress-track.is-active",
	} {
		if !strings.Contains(cssText, want) {
			t.Fatalf("sidebar progress CSS missing %q", want)
		}
	}
}

func TestRepairAndOptimizeExposeRealProgressStages(t *testing.T) {
	for file, stages := range map[string][]string{
		"repair.go": {
			"读取样本",
			"检查当前映射",
			"验证缓存候选",
			"评估刷新",
			"CFST 测速",
			"验证新候选",
			"应用结果",
		},
		"optimize.go": {
			"准备完整优化",
			"CFST 测速",
			"读取样本",
			"验证并选择域名",
			"应用结果",
		},
	} {
		content, err := os.ReadFile(file)
		if err != nil { t.Fatal(err) }
		for _, stage := range stages {
			if !strings.Contains(string(content), stage) {
				t.Fatalf("%s missing progress stage %q", file, stage)
			}
		}
	}
}
