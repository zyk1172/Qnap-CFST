package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCandidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.csv")
	csv := "IP 地址,已发送,已接收,丢包率,平均延迟,下载速度(MB/s),地区码\n104.16.1.1,4,4,0.00,21.50,12.30,HKG\n2606:4700::1,4,4,0.00,33.20,8.50,NRT\n"
	if err := os.WriteFile(path, []byte(csv), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := parseCandidates(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].IP != "104.16.1.1" || got[0].SpeedMB != 12.30 {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestRenderHostsPreservesUnmanagedContent(t *testing.T) {
	in := "127.0.0.1 localhost\n10.0.0.2 custom.local\n# >>> CFHOST MANAGED >>>\n1.1.1.1 old.example\n# <<< CFHOST MANAGED <<<\n"
	out, err := renderHosts(in, map[string]string{"a.example": "104.16.0.1", "b.example": "104.16.0.2"})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(out, "10.0.0.2 custom.local") {
		t.Fatal("unmanaged line was removed")
	}
	if strings.Contains(out, "old.example") {
		t.Fatal("old managed mapping remained")
	}
	if !strings.Contains(out, "104.16.0.1 a.example") || !strings.Contains(out, "104.16.0.2 b.example") {
		t.Fatal("new mappings missing")
	}
}

func TestRenderHostsEmptyClearsManagedBlock(t *testing.T) {
	in := "127.0.0.1 localhost\n# >>> CFHOST MANAGED >>>\n1.1.1.1 old.example\n# <<< CFHOST MANAGED <<<\n"
	out, err := renderHosts(in, map[string]string{})
	if err != nil { t.Fatal(err) }
	if strings.Contains(out, "old.example") || strings.Contains(out, hostsBegin) {
		t.Fatalf("managed block was not cleared: %q", out)
	}
	if !strings.Contains(out, "127.0.0.1 localhost") {
		t.Fatal("unmanaged content lost")
	}
}

func TestDefaultConfigHasManagedDomains(t *testing.T) {
	c := defaultConfig()
	if len(c.Domains) != 16 {
		t.Fatalf("expected 16 default domains, got %d", len(c.Domains))
	}
	if c.Domains[0].Mode != "tracker" || c.Domains[2].Mode != "http" {
		t.Fatal("default verifier modes are wrong")
	}
	if !c.Tracker.RealAnnounce || c.Repair.FailureThreshold != 2 {
		t.Fatal("v0.2 defaults are wrong")
	}
}

func TestFreshCandidatesTTLAndRanking(t *testing.T) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	in := []Candidate{
		{IP: "104.16.0.2", LossRate: 0, DelayMS: 40, SpeedMB: 30, ObservedAt: now.Add(-30 * time.Minute).Format(time.RFC3339)},
		{IP: "104.16.0.1", LossRate: 0, DelayMS: 20, SpeedMB: 10, ObservedAt: now.Add(-30 * time.Minute).Format(time.RFC3339)},
		{IP: "104.16.0.3", LossRate: 0, DelayMS: 10, SpeedMB: 99, ObservedAt: now.Add(-25 * time.Hour).Format(time.RFC3339)},
	}
	got := freshCandidates(in, 24*time.Hour, now)
	if len(got) != 2 || got[0].IP != "104.16.0.1" {
		t.Fatalf("unexpected fresh ranking: %#v", got)
	}
}

func TestOrderedCandidatesPreferredAndSkip(t *testing.T) {
	in := []Candidate{
		{IP: "1.1.1.1", DelayMS: 10},
		{IP: "2.2.2.2", DelayMS: 20},
		{IP: "3.3.3.3", DelayMS: 30},
	}
	cfg := defaultConfig()
	got := orderedCandidates(in, Domain{Class: "latency"}, "3.3.3.3", "1.1.1.1", cfg)
	if len(got) != 2 || got[0].IP != "3.3.3.3" || got[1].IP != "2.2.2.2" {
		t.Fatalf("unexpected candidate order: %#v", got)
	}
}

func TestRefreshDecisionBackoff(t *testing.T) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	last := now.Add(-61 * time.Minute).Format(time.RFC3339)

	ok, _, backoff := refreshDecision(now, last, 2, 2, time.Hour, 12*time.Hour, false)
	if !ok || backoff != time.Hour {
		t.Fatalf("expected refresh at threshold, ok=%v backoff=%s", ok, backoff)
	}

	ok, next, backoff := refreshDecision(now, last, 3, 2, time.Hour, 12*time.Hour, false)
	if ok || backoff != 2*time.Hour || !next.Equal(now.Add(59*time.Minute)) {
		t.Fatalf("unexpected doubled backoff: ok=%v next=%v backoff=%s", ok, next, backoff)
	}

	ok, _, _ = refreshDecision(now, "", 1, 2, time.Hour, 12*time.Hour, false)
	if ok {
		t.Fatal("single failure should not trigger refresh")
	}

	ok, _, _ = refreshDecision(now, "", 1, 2, time.Hour, 12*time.Hour, true)
	if !ok {
		t.Fatal("bootstrap should refresh immediately")
	}
}

func TestEvaluateTrackerResponseTreatsTrackerErrorsAsReachable(t *testing.T) {
	bencodeFailure := func(reason string) string {
		return fmt.Sprintf("d14:failure reason%d:%se", len(reason), reason)
	}
	cases := []struct {
		name      string
		body      string
		reachable bool
		accepted  bool
	}{
		{"accepted announce", "d8:intervali1800e5:peers0:e", true, true},
		{"accepted peers only", "d5:peers6:abcdefe", true, true},
		{"PTT anti-abuse error still proves connectivity", bencodeFailure("PTT:多IP汇报同一资源，等缓存过期或修改qb高级里的网络接口"), true, false},
		{"missing peer id still proves connectivity", bencodeFailure("Missing key peer_id"), true, false},
		{"even IP-ban business error proves tracker connectivity", bencodeFailure("your ip is banned"), true, false},
		{"other valid tracker dictionary proves connectivity", "d5:hello3:youe", true, false},
		{"html challenge is not tracker response", "<html>ok</html>", false, false},
		{"empty response", "", false, false},
		{"truncated dictionary", "d14:failure reason9:abce", false, false},
		{"trailing garbage", "d5:peers0:ejunk", false, false},
	}
	for _, tc := range cases {
		verdict := evaluateTrackerResponse([]byte(tc.body))
		if verdict.Reachable != tc.reachable || verdict.Accepted != tc.accepted {
			t.Fatalf("%s: reachable=%v accepted=%v, want reachable=%v accepted=%v detail=%q",
				tc.name, verdict.Reachable, verdict.Accepted, tc.reachable, tc.accepted, verdict.Detail)
		}
	}
}

func TestTrackerFailureReasonIsSanitized(t *testing.T) {
	reason := "invalid passkey=deadbeef1234 for announce"
	verdict := evaluateTrackerResponse([]byte(fmt.Sprintf("d14:failure reason%d:%se", len(reason), reason)))
	if !verdict.Reachable {
		t.Fatalf("business rejection must still be reachable: %q", verdict.Detail)
	}
	if strings.Contains(verdict.Detail, "deadbeef1234") {
		t.Fatalf("credential leaked into detail: %q", verdict.Detail)
	}
	if verdict.Reason == "" {
		t.Fatal("failure reason should remain visible after sanitization")
	}
}

func TestLoadTrackerSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "samples.tsv")
	line := "tracker.example.com\t/announce\t0123456789abcdef0123456789abcdef01234567\thttps://tracker.example.com/announce?passkey=test\n"
	if err := os.WriteFile(path, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}
	samples, err := loadTrackerSamples(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || len(samples["tracker.example.com"].Hash) != 20 {
		t.Fatalf("unexpected samples: %#v", samples)
	}
}

func TestLoadTrackerSamplesKeepsValidRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "samples-partial.tsv")
	content := "bad-row\n" +
		"tracker.example.com\t/announce\t0123456789abcdef0123456789abcdef01234567\thttps://tracker.example.com/announce?passkey=test\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	samples, err := loadTrackerSamples(path)
	if err == nil {
		t.Fatal("expected warning error for invalid row")
	}
	if len(samples) != 1 {
		t.Fatalf("valid sample should survive invalid sibling row: %#v", samples)
	}
}

func TestNormalizeLegacyDomainClass(t *testing.T) {
	c := defaultConfig()
	c.Domains = []Domain{{Host: "example.com", Group: "site", Mode: "http", Enabled: true}}
	if err := normalizeConfig(&c); err != nil {
		t.Fatal(err)
	}
	if c.Domains[0].Class != "latency" {
		t.Fatalf("legacy domain should migrate to latency class: %#v", c.Domains[0])
	}
}

func TestBandwidthRanking(t *testing.T) {
	in := []Candidate{
		{IP:"1.1.1.1",LossRate:0,DelayMS:20,SpeedMB:10},
		{IP:"2.2.2.2",LossRate:0,DelayMS:60,SpeedMB:30},
		{IP:"3.3.3.3",LossRate:0,DelayMS:10,SpeedMB:20},
	}
	got := rankCandidatesForClass(in,"bandwidth")
	if got[0].IP!="2.2.2.2" || got[1].IP!="3.3.3.3" { t.Fatalf("unexpected bandwidth order: %#v",got) }
}

func TestOrderedCandidatesAppliesBandwidthPolicyAndLimit(t *testing.T) {
	cfg:=defaultConfig(); cfg.Verify.CandidateLimit=2
	cfg.Bandwidth.MaxLossRate=0; cfg.Bandwidth.MaxDelayMS=180; cfg.Bandwidth.MinSpeedMB=0.5
	d:=Domain{Class:"bandwidth"}
	in:=[]Candidate{
		{IP:"1.1.1.1",LossRate:0,DelayMS:40,SpeedMB:5},
		{IP:"2.2.2.2",LossRate:0,DelayMS:30,SpeedMB:9},
		{IP:"3.3.3.3",LossRate:0.1,DelayMS:10,SpeedMB:99},
		{IP:"4.4.4.4",LossRate:0,DelayMS:20,SpeedMB:8},
	}
	got:=orderedCandidates(in,d,"","",cfg)
	if len(got)!=2 || got[0].IP!="2.2.2.2" || got[1].IP!="4.4.4.4" { t.Fatalf("unexpected filtered order: %#v",got) }
}

func TestRenderHostsRejectsBrokenMarkers(t *testing.T) {
	_,err:=renderHosts("127.0.0.1 localhost\n"+hostsBegin+"\n1.1.1.1 a.example\n",map[string]string{"a.example":"1.1.1.1"})
	if err==nil { t.Fatal("expected broken marker rejection") }
}

func TestRenderHostsMigratesLegacyMarkers(t *testing.T) {
	in:="127.0.0.1 localhost\n"+legacyLatencyBegin+"\n1.1.1.1 old.example\n"+legacyLatencyEnd+"\n"
	out,err:=renderHosts(in,map[string]string{"new.example":"2.2.2.2"})
	if err!=nil{t.Fatal(err)}
	if strings.Contains(out,legacyLatencyBegin)||strings.Contains(out,"old.example"){t.Fatalf("legacy marker remained: %s",out)}
	if !strings.Contains(out,"2.2.2.2 new.example"){t.Fatal("new mapping missing")}
}

func TestStageApplyRollback(t *testing.T) {
	dir:=t.TempDir(); hosts:=filepath.Join(dir,"hosts")
	original:="127.0.0.1 localhost\n"
	if err:=os.WriteFile(hosts,[]byte(original),0644);err!=nil{t.Fatal(err)}
	a:=&App{dataDir:dir}
	cfg:=defaultConfig();cfg.HostsPath=hosts
	stage,err:=a.stageApplyMappings(cfg,map[string]string{"a.example":"1.1.1.1"})
	if err!=nil{t.Fatal(err)}
	if !stage.Changed{t.Fatal("expected changed stage")}
	if err:=stage.Rollback();err!=nil{t.Fatal(err)}
	got,_:=os.ReadFile(hosts)
	if string(got)!=original{t.Fatalf("rollback mismatch: %q",string(got))}
}

func TestMappingsFromLegacyBlocks(t *testing.T) {
	content:=legacyLatencyBegin+"\n1.1.1.1 tracker.example.com\n"+legacyLatencyEnd+"\n"
	got,err:=mappingsFromManagedBlocks(content,map[string]bool{"tracker.example.com":true})
	if err!=nil{t.Fatal(err)}
	if got["tracker.example.com"]!="1.1.1.1"{t.Fatalf("migration failed: %#v",got)}
}

func TestOptimizeDueRequiresExplicitScheduledFull(t *testing.T) {
	now:=time.Date(2026,9,22,1,0,0,0,time.UTC)
	cfg:=OptimizeConfig{Enabled:true,ScheduledFull:false,IntervalMinutes:1440,RetryMinutes:60}
	if optimizeDue(now,"","",now.Add(-25*time.Hour).Format(time.RFC3339),cfg){
		t.Fatal("legacy enabled=true must not schedule a global remap")
	}
	cfg.ScheduledFull=true
	if optimizeDue(now,"","",now.Add(-2*time.Hour).Format(time.RFC3339),cfg){t.Fatal("recent refresh should defer first scheduled full optimize")}
	if !optimizeDue(now,"","",now.Add(-25*time.Hour).Format(time.RFC3339),cfg){t.Fatal("stale refresh should trigger explicitly enabled scheduled full optimize")}
	if optimizeDue(now,"",now.Add(-30*time.Minute).Format(time.RFC3339),now.Add(-25*time.Hour).Format(time.RFC3339),cfg){t.Fatal("recent failed optimize attempt should defer retry")}
}

func TestNormalizeConfigRetiresLegacyAutomaticOptimize(t *testing.T) {
	cfg:=defaultConfig()
	cfg.Optimize.Enabled=true
	cfg.Optimize.ScheduledFull=false
	if err:=normalizeConfig(&cfg);err!=nil{t.Fatal(err)}
	if cfg.Optimize.Enabled{t.Fatal("legacy optimize.enabled must be retired")}
	if cfg.Optimize.ScheduledFull{t.Fatal("scheduled global optimize must remain opt-in")}
}


func TestNormalizeNormalDomainClass(t *testing.T) {
	cfg:=defaultConfig()
	cfg.Domains=[]Domain{{Host:"plain.example.com",Class:"normal",Mode:"tracker",Enabled:true}}
	if err:=normalizeConfig(&cfg);err!=nil{t.Fatal(err)}
	if cfg.Domains[0].Class!="normal"{t.Fatalf("normal class was not preserved: %#v",cfg.Domains[0])}
}

func TestNormalRankingUsesLowestDelayFirst(t *testing.T) {
	in:=[]Candidate{
		{IP:"1.1.1.1",LossRate:0,DelayMS:25,SpeedMB:100},
		{IP:"2.2.2.2",LossRate:0.1,DelayMS:10,SpeedMB:5},
		{IP:"3.3.3.3",LossRate:0,DelayMS:15,SpeedMB:20},
	}
	got:=rankCandidatesForClass(in,"normal")
	if len(got)!=3 || got[0].IP!="2.2.2.2" || got[1].IP!="3.3.3.3" {
		t.Fatalf("normal strategy must rank by latency first: %#v",got)
	}
}

func TestOrderedNormalCandidatesIgnoreGroupPreferred(t *testing.T) {
	cfg:=defaultConfig()
	in:=[]Candidate{
		{IP:"1.1.1.1",DelayMS:30},
		{IP:"2.2.2.2",DelayMS:10},
		{IP:"3.3.3.3",DelayMS:20},
	}
	got:=orderedCandidates(in,Domain{Class:"normal"},"1.1.1.1","",cfg)
	if len(got)==0 || got[0].IP!="2.2.2.2" {
		t.Fatalf("normal strategy must ignore group preferred IP and use lowest latency: %#v",got)
	}
}

func TestNormalTrackerDoesNotRequireSample(t *testing.T) {
	cfg:=defaultConfig()
	d:=Domain{Host:"tracker.example.com",Class:"normal",Mode:"tracker",Enabled:true}
	if !domainRefreshable(d,cfg,map[string]TrackerSample{}) {
		t.Fatal("normal tracker must not require a tracker sample")
	}
	ok,detail:=verifyConfiguredDomain(t.Context(),d,"104.16.0.1",cfg,map[string]TrackerSample{})
	if !ok || !strings.Contains(detail,"verification skipped") {
		t.Fatalf("normal strategy should bypass domain verification: ok=%v detail=%q",ok,detail)
	}
}

func TestResolvePendingNormalUsesLowestLatencyWithoutVerification(t *testing.T) {
	cfg:=defaultConfig()
	a:=&App{}
	d:=Domain{Host:"tracker.example.com",Class:"normal",Mode:"tracker",Enabled:true}
	pending:=[]pendingDomain{{Domain:d,Refreshable:true}}
	mappings:=map[string]string{}
	statuses:=map[string]string{}
	remaining:=a.resolvePending(
		t.Context(),
		cfg,
		map[string]TrackerSample{},
		[]Candidate{
			{IP:"104.16.0.1",DelayMS:45},
			{IP:"104.16.0.2",DelayMS:12},
		},
		pending,
		mappings,
		statuses,
		map[string]string{},
	)
	if len(remaining)!=0 {
		t.Fatalf("normal domain should resolve directly: %#v",remaining)
	}
	if mappings[d.Host]!="104.16.0.2" {
		t.Fatalf("normal domain did not receive lowest latency IP: %#v",mappings)
	}
	if !strings.Contains(statuses[d.Host],"verification skipped") {
		t.Fatalf("normal status must state verification was skipped: %q",statuses[d.Host])
	}
}

func TestTrackerDiscoverySkipsNormalDomains(t *testing.T) {
	cfg:=defaultConfig()
	cfg.Domains=[]Domain{
		{Host:"tracker.normal.example",Class:"normal",Mode:"tracker",Enabled:true},
		{Host:"tracker.verified.example",Class:"latency",Mode:"tracker",Enabled:true},
	}
	targets:=trackerTargetDomains(cfg)
	if targets["tracker.normal.example"] {
		t.Fatal("normal tracker must not trigger sample discovery")
	}
	if !targets["tracker.verified.example"] {
		t.Fatal("verified tracker should remain a discovery target")
	}
}

func TestUnresolvedDomainsListsEnabledDomainsWithoutMappings(t *testing.T) {
	cfg := Config{Domains: []Domain{
		{Host: "b.example", Enabled: true},
		{Host: "a.example", Enabled: true},
		{Host: "c.example", Enabled: false},
	}}
	names, total := unresolvedDomains(cfg, map[string]string{"a.example": "1.1.1.1"})
	if total != 1 || len(names) != 1 || names[0] != "b.example" {
		t.Fatalf("got names=%v total=%d", names, total)
	}
	names, total = unresolvedDomains(cfg, nil)
	if total != 2 || len(names) != 2 || names[0] != "a.example" || names[1] != "b.example" {
		t.Fatalf("disabled domains must be excluded and names sorted: %v total=%d", names, total)
	}
}

func TestUnresolvedDomainsCapsReportedNames(t *testing.T) {
	cfg := Config{}
	for i := 0; i < 30; i++ {
		cfg.Domains = append(cfg.Domains, Domain{Host: fmt.Sprintf("d%02d.example", i), Enabled: true})
	}
	names, total := unresolvedDomains(cfg, nil)
	if total != 30 {
		t.Fatalf("total=%d, want 30", total)
	}
	if len(names) != 20 {
		t.Fatalf("reported names=%d, want 20", len(names))
	}
}


func TestResolutionJobClassification(t *testing.T) {
	for _, kind := range []string{"repair", "optimize"} {
		if !resolutionJob(kind) {
			t.Fatalf("%s must surface unresolved domains", kind)
		}
	}
	for _, kind := range []string{"run", "apply", "sync", ""} {
		if resolutionJob(kind) {
			t.Fatalf("%s must not be labelled partial because it does not resolve domains", kind)
		}
	}
}


func TestRunDomainMaintenanceOnlyChangesTarget(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Format(time.RFC3339)
	cfg := defaultConfig()
	cfg.AutoApply = false
	cfg.Sync.Enabled = false
	cfg.Domains = []Domain{
		{Host:"a.example", Class:"normal", Mode:"http", Enabled:true},
		{Host:"b.example", Class:"normal", Mode:"http", Enabled:true},
	}
	a := &App{
		config: cfg,
		dataDir: dir,
		state: RuntimeState{
			Mappings: map[string]string{"a.example":"1.1.1.1", "b.example":"9.9.9.9"},
			DomainStatus: map[string]string{"a.example":"old-a", "b.example":"keep-b"},
			DomainHealth: map[string]DomainHealth{},
			TrackerSamples: map[string]TrackerSampleRuntime{},
			Candidates: []Candidate{
				{IP:"2.2.2.2", DelayMS:12, LossRate:0, SpeedMB:10, ObservedAt:now},
				{IP:"3.3.3.3", DelayMS:25, LossRate:0, SpeedMB:20, ObservedAt:now},
			},
		},
	}
	if err := a.runDomainMaintenance(context.Background(), cfg, "a.example"); err != nil {
		t.Fatal(err)
	}
	if got := a.state.Mappings["a.example"]; got != "2.2.2.2" {
		t.Fatalf("target domain mapping=%q, want 2.2.2.2", got)
	}
	if got := a.state.Mappings["b.example"]; got != "9.9.9.9" {
		t.Fatalf("unrelated domain changed: %q", got)
	}
	if got := a.state.DomainStatus["b.example"]; got != "keep-b" {
		t.Fatalf("unrelated domain status changed: %q", got)
	}
}

func TestTrackerSampleInventoryAndTestState(t *testing.T) {
	dir := t.TempDir()
	manualPath := filepath.Join(dir, "manual.tsv")
	autoPath := filepath.Join(dir, "auto.tsv")
	hash := "0123456789abcdef0123456789abcdef01234567"
	if err := os.WriteFile(autoPath, []byte("tracker.example.com\t/announce\t"+hash+"\thttps://tracker.example.com/announce?passkey=test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Tracker.SamplesPath = manualPath
	cfg.Tracker.AutoSamplesPath = autoPath
	cfg.Domains = []Domain{{Host:"tracker.example.com", Class:"latency", Mode:"tracker", Enabled:true}}
	a := &App{state:RuntimeState{TrackerSamples:map[string]TrackerSampleRuntime{}}}
	a.refreshTrackerSampleInventory(cfg)
	st := a.state.TrackerSamples["tracker.example.com"]
	if !st.Available || st.Source != "auto" || st.Tested {
		t.Fatalf("unexpected initial sample state: %#v", st)
	}
	a.recordTrackerSampleTest(cfg, cfg.Domains[0], true, "announce rejected · candidate reachable")
	st = a.state.TrackerSamples["tracker.example.com"]
	if !st.Available || !st.Tested || !st.Passed || st.LastTest == "" {
		t.Fatalf("sample test state not recorded: %#v", st)
	}
	a.markTrackerSamplesPending([]string{"tracker.example.com"})
	st = a.state.TrackerSamples["tracker.example.com"]
	if st.Tested || st.Passed || st.Detail != "已获取，等待维护验证" {
		t.Fatalf("new downloader sample must return to pending: %#v", st)
	}
}

func TestManualTrackerSampleKeepsPriorityAndTestState(t *testing.T) {
	dir := t.TempDir()
	manualPath := filepath.Join(dir, "manual.tsv")
	autoPath := filepath.Join(dir, "auto.tsv")
	hash := "0123456789abcdef0123456789abcdef01234567"
	line := "tracker.example.com\t/announce\t"+hash+"\thttps://tracker.example.com/announce?passkey=test\n"
	if err := os.WriteFile(manualPath, []byte(line), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(autoPath, []byte(line), 0600); err != nil { t.Fatal(err) }
	cfg := defaultConfig()
	cfg.Tracker.SamplesPath = manualPath
	cfg.Tracker.AutoSamplesPath = autoPath
	cfg.Domains = []Domain{{Host:"tracker.example.com", Class:"latency", Mode:"tracker", Enabled:true}}
	a := &App{state:RuntimeState{TrackerSamples:map[string]TrackerSampleRuntime{}}}
	a.refreshTrackerSampleInventory(cfg)
	a.recordTrackerSampleTest(cfg, cfg.Domains[0], true, "announce accepted")
	a.markTrackerSamplesPending([]string{"tracker.example.com"})
	st := a.state.TrackerSamples["tracker.example.com"]
	if st.Source != "manual" || !st.Tested || !st.Passed {
		t.Fatalf("auto discovery must not invalidate higher-priority manual sample: %#v", st)
	}
}
