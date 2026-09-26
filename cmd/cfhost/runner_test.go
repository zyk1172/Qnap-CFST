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
		{"upstream tracker connect failure is unreachable", bencodeFailure("Could not connect to tracker"), false, false},
		{"truncated connect wording is unreachable", bencodeFailure("Could not connect to track"), false, false},
		{"failed-to-connect wording is unreachable", bencodeFailure("Failed to connect to tracker"), false, false},
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

func TestNormalTrackerRequiresSampleAndDoesNotSkipVerification(t *testing.T) {
	cfg:=defaultConfig()
	d:=Domain{Host:"tracker.example.com",Class:"normal",Mode:"tracker",Enabled:true}
	if domainRefreshable(d,cfg,map[string]TrackerSample{}) {
		t.Fatal("normal Tracker must require a real announce sample")
	}
	ok,detail:=verifyConfiguredDomain(t.Context(),d,"104.16.0.1",cfg,map[string]TrackerSample{})
	if ok || detail!="tracker sample missing" {
		t.Fatalf("normal Tracker must not bypass verification: ok=%v detail=%q",ok,detail)
	}
	a:=&App{state:RuntimeState{TrackerSamples:map[string]TrackerSampleRuntime{}}}
	a.recordTrackerSampleTest(cfg,d,true,"announce accepted · attempt 1")
	state:=a.state.TrackerSamples[d.Host]
	if !state.Tested || !state.Passed || !strings.Contains(state.Detail,"announce accepted") {
		t.Fatalf("normal Tracker sample result was not persisted: %#v",state)
	}
}

func TestResolvePendingNormalHTTPUsesLowestLatencyWithoutVerification(t *testing.T) {
	cfg:=defaultConfig()
	a:=&App{}
	d:=Domain{Host:"plain.example.com",Class:"normal",Mode:"http",Enabled:true}
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
		t.Fatalf("normal HTTP domain should resolve directly: %#v",remaining)
	}
	if mappings[d.Host]!="104.16.0.2" {
		t.Fatalf("normal HTTP domain did not receive lowest latency IP: %#v",mappings)
	}
	if !strings.Contains(statuses[d.Host],"verification skipped") {
		t.Fatalf("normal HTTP status must state verification was skipped: %q",statuses[d.Host])
	}
}

func TestResolvePendingNormalTrackerDoesNotBypassMissingSample(t *testing.T) {
	cfg:=defaultConfig()
	a:=&App{state:RuntimeState{TrackerSamples:map[string]TrackerSampleRuntime{}}}
	d:=Domain{Host:"tracker.example.com",Class:"normal",Mode:"tracker",Enabled:true}
	pending:=[]pendingDomain{{Domain:d,Refreshable:true}}
	mappings:=map[string]string{}
	statuses:=map[string]string{}
	remaining:=a.resolvePending(
		t.Context(),
		cfg,
		map[string]TrackerSample{},
		[]Candidate{{IP:"104.16.0.2",DelayMS:12}},
		pending,
		mappings,
		statuses,
		map[string]string{},
	)
	if len(remaining)!=1 || mappings[d.Host]!="" {
		t.Fatalf("normal Tracker must remain unresolved without a sample: remaining=%#v mappings=%#v",remaining,mappings)
	}
	if !strings.Contains(statuses[d.Host],"tracker sample missing") {
		t.Fatalf("normal Tracker unresolved reason must expose missing sample: %q",statuses[d.Host])
	}
}

func TestTrackerDiscoveryIncludesNormalTrackerDomains(t *testing.T) {
	cfg:=defaultConfig()
	cfg.Domains=[]Domain{
		{Host:"tracker.normal.example",Class:"normal",Mode:"tracker",Enabled:true},
		{Host:"tracker.verified.example",Class:"latency",Mode:"tracker",Enabled:true},
		{Host:"normal-http.example",Class:"normal",Mode:"http",Enabled:true},
	}
	targets:=trackerTargetDomains(cfg)
	if !targets["tracker.normal.example"] {
		t.Fatal("normal Tracker must participate in sample discovery")
	}
	if !targets["tracker.verified.example"] {
		t.Fatal("latency Tracker should remain a discovery target")
	}
	if targets["normal-http.example"] {
		t.Fatal("normal HTTP domain must not enter Tracker sample discovery")
	}
}

func TestCFSTRunTimeoutIsIndependent(t *testing.T) {
	cfg:=defaultConfig()
	cfg.CFST.RunTimeoutMinutes=10
	if got:=cfstRunTimeout(cfg);got!=10*time.Minute {
		t.Fatalf("CFST timeout=%s want 10m",got)
	}
	cfg.CFST.RunTimeoutMinutes=0
	if got:=cfstRunTimeout(cfg);got!=30*time.Minute {
		t.Fatalf("invalid CFST timeout should fall back to 30m, got %s",got)
	}
	parent:=context.Background()
	if _,ok:=parent.Deadline();ok {
		t.Fatal("Repair/job parent context must not require a deadline")
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


func TestTrackerFailureConnectivityClassification(t *testing.T) {
	unreachable := []string{
		"Could not connect to tracker",
		"Could not connect to track",
		"Couldn't connect to tracker",
		"Cannot connect to tracker",
		"Can't connect to tracker",
		"Failed to connect to tracker",
		"Unable to connect to tracker",
		"Tracker connection failed",
	}
	for _, reason := range unreachable {
		if !trackerFailureIndicatesUnreachable(reason) {
			t.Fatalf("expected connectivity failure classification for %q", reason)
		}
	}
	business := []string{
		"Missing key peer_id",
		"your ip is banned",
		"PTT:多IP汇报同一资源",
		"torrent not registered",
	}
	for _, reason := range business {
		if trackerFailureIndicatesUnreachable(reason) {
			t.Fatalf("business rejection must not be classified as unreachable: %q", reason)
		}
	}
}

func TestParseCFSTRateLimitSignal(t *testing.T) {
	got, ok := parseCFSTRateLimitSignal("noise\nCFST_RATE_LIMIT status=429 retry_after=\"551\"\n")
	if !ok || got.StatusCode != 429 || got.RetryAfter != "551" {
		t.Fatalf("unexpected rate-limit signal: ok=%v got=%#v", ok, got)
	}
}

func TestRetryAfterDeadline(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	until, seconds := retryAfterDeadline(now, "551", 15*time.Minute)
	if seconds != 551 || !until.Equal(now.Add(551*time.Second)) {
		t.Fatalf("seconds Retry-After parsed incorrectly: until=%v seconds=%d", until, seconds)
	}
	until, seconds = retryAfterDeadline(now, "", 15*time.Minute)
	if seconds != 900 || !until.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("fallback cooldown parsed incorrectly: until=%v seconds=%d", until, seconds)
	}
}

func TestBuildCFSTArgsDegraded(t *testing.T) {
	cfg := defaultConfig()
	cfg.CFST.DownloadCount = 20
	cfg.CFST.DownloadSeconds = 5
	cfg.CFST.MinSpeedMB = 2
	cfg.CFST.DownloadURL = "https://speed.cloudflare.com/__down?bytes=90000000"
	cfg.CFST.DegradedDownloadCount = 5
	cfg.CFST.DegradedDownloadSeconds = 2
	cfg.CFST.DegradedDownloadMB = 5

	args := buildCFSTArgs(cfg, "/app/ip.txt", "/data/result.csv", true)
	value := func(flag string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag {
				return args[i+1]
			}
		}
		return ""
	}
	if value("-dn") != "5" || value("-dt") != "2" || value("-sl") != "0" {
		t.Fatalf("unexpected degraded args: %v", args)
	}
	if value("-url") != "https://speed.cloudflare.com/__down?bytes=5000000" {
		t.Fatalf("degraded URL did not shrink bytes parameter: %v", args)
	}
	if value("-dr") != "" {
		t.Fatalf("degraded mode must not use artificial rate throttling: %v", args)
	}
}

func TestDegradedDownloadURLPreservesOtherQueryParameters(t *testing.T) {
	got, ok := degradedDownloadURL("https://speed.cloudflare.com/__down?foo=bar&bytes=90000000", 5)
	if !ok {
		t.Fatal("Cloudflare bytes URL should be rewritable")
	}
	if !strings.Contains(got, "bytes=5000000") || !strings.Contains(got, "foo=bar") {
		t.Fatalf("unexpected degraded URL: %s", got)
	}
	if got, ok := degradedDownloadURL("https://example.com/file.bin", 5); ok || got != "https://example.com/file.bin" {
		t.Fatalf("generic URL without bytes must remain untouched: %s ok=%v", got, ok)
	}
}

func TestLegacyCFSTConfigEnablesAdaptiveRateLimit(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "listen": ":8080",
  "cfst": {
    "threads": 200,
    "pingTimes": 4,
    "downloadCount": 20,
    "downloadSeconds": 5,
    "maxDelayMs": 350,
    "maxLossRate": 0.2,
    "minSpeedMB": 0.1,
    "downloadUrl": "https://cf.xiu2.xyz/url",
    "ipv6": false,
    "runTimeoutMinutes": 30
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CFST.AdaptiveRateLimit || cfg.CFST.DegradedDownloadMB != 5 || cfg.CFST.DegradedDownloadCount != 5 {
		t.Fatalf("legacy config did not receive adaptive rate-limit defaults: %#v", cfg.CFST)
	}
}

func TestLegacyDegradedRateMigratesToDownloadMB(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "cfst": {
    "threads": 200,
    "pingTimes": 4,
    "downloadCount": 20,
    "downloadSeconds": 5,
    "maxDelayMs": 350,
    "maxLossRate": 0.2,
    "minSpeedMB": 0.1,
    "downloadUrl": "https://speed.cloudflare.com/__down?bytes=90000000",
    "runTimeoutMinutes": 30,
    "adaptiveRateLimit": true,
    "degradedRateMbps": 3.5,
    "degradedDownloadCount": 5,
    "degradedDownloadSeconds": 2,
    "rateLimitFallbackMinutes": 15
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CFST.DegradedDownloadMB != 3.5 {
		t.Fatalf("legacy degradedRateMbps was not migrated: %#v", cfg.CFST)
	}
}

func TestRetainEnabledMappings(t *testing.T) {
	cfg := Config{Domains: []Domain{
		{Host:"a.example",Enabled:true},
		{Host:"b.example",Enabled:false},
	}}
	got := retainEnabledMappings(map[string]string{
		"a.example":"1.1.1.1",
		"b.example":"2.2.2.2",
		"removed.example":"3.3.3.3",
	}, cfg)
	if len(got)!=1 || got["a.example"]!="1.1.1.1" {
		t.Fatalf("unexpected retained mappings: %#v", got)
	}
}

func TestCommitResolutionRejectsExpiredContextWithoutMutatingMappings(t *testing.T) {
	dir:=t.TempDir()
	a:=&App{
		dataDir:dir,
		state:RuntimeState{
			Mappings:map[string]string{"a.example":"1.1.1.1"},
			DomainStatus:map[string]string{"a.example":"old"},
			DomainHealth:map[string]DomainHealth{},
			TrackerSamples:map[string]TrackerSampleRuntime{},
		},
	}
	cfg:=defaultConfig()
	cfg.AutoApply=false
	ctx,cancel:=context.WithCancel(context.Background())
	cancel()
	err:=a.commitResolution(ctx,cfg,map[string]string{"a.example":"2.2.2.2"},map[string]string{"a.example":"new"},map[string]DomainHealth{},"",false)
	if err==nil {
		t.Fatal("expired context must block commit")
	}
	if got:=a.state.Mappings["a.example"];got!="1.1.1.1" {
		t.Fatalf("mapping mutated despite blocked commit: %q",got)
	}
}

func TestCommitResolutionClearsRejectedIPOnlyAfterReplacement(t *testing.T) {
	dir:=t.TempDir()
	cfg:=defaultConfig()
	cfg.AutoApply=false
	cfg.Sync.Enabled=false

	a:=&App{
		dataDir:dir,
		state:RuntimeState{
			Mappings:map[string]string{"tracker.example.com":"104.16.0.10"},
			DomainStatus:map[string]string{},
			DomainHealth:map[string]DomainHealth{},
			TrackerSamples:map[string]TrackerSampleRuntime{},
			TrackerKeepalive:map[string]TrackerKeepaliveRuntime{
				"tracker.example.com":{RejectedIP:"104.16.0.10",RejectedAt:"2026-09-24T00:00:00Z",Attempts:3,Status:"repair"},
			},
		},
	}

	if err:=a.commitResolution(context.Background(),cfg,
		map[string]string{"tracker.example.com":"104.16.0.10"},
		map[string]string{},map[string]DomainHealth{},"",false);err!=nil{t.Fatal(err)}
	if got:=a.state.TrackerKeepalive["tracker.example.com"].RejectedIP;got!="104.16.0.10"{
		t.Fatalf("same mapping must remain rejected, got %q",got)
	}

	if err:=a.commitResolution(context.Background(),cfg,
		map[string]string{"tracker.example.com":"104.16.0.11"},
		map[string]string{},map[string]DomainHealth{},"",false);err!=nil{t.Fatal(err)}
	state:=a.state.TrackerKeepalive["tracker.example.com"]
	if state.RejectedIP!=""{
		t.Fatalf("rejected IP must clear only after replacement commits: %#v",state)
	}
	if state.Status!="healthy" || !strings.Contains(state.LastEvent,"104.16.0.11"){
		t.Fatalf("replacement clear state missing: %#v",state)
	}
}

func TestFinalizeUnresolvedMappingDropsConfirmedHTTPHardFailure(t *testing.T) {
	d:=Domain{Host:"moviepilot.example",Class:"latency",Mode:"http",Enabled:true}
	p:=pendingDomain{Domain:d,FailedCurrent:"104.16.0.10",Refreshable:true}
	current:=map[string]string{d.Host:"104.16.0.10"}

	mappings:=copyMappings(current)
	statuses:=map[string]string{d.Host:"current failed · HTTP 403 · blocked/unusable"}
	finalizeUnresolvedMapping(mappings,current,statuses,p,map[string]bool{d.Host:true})
	if _,exists:=mappings[d.Host];exists{
		t.Fatalf("confirmed HTTP 403 mapping must not be stale-retained: %#v",mappings)
	}
	if !strings.Contains(statuses[d.Host],"old mapping removed"){
		t.Fatalf("hard failure status must expose removal: %q",statuses[d.Host])
	}

	mappings=copyMappings(current)
	statuses=map[string]string{d.Host:"current failed · context deadline exceeded"}
	finalizeUnresolvedMapping(mappings,current,statuses,p,map[string]bool{})
	if mappings[d.Host]!="104.16.0.10"{
		t.Fatalf("uncertain timeout must still retain last-known-good mapping: %#v",mappings)
	}
}

func TestTrackerKeepaliveSchedulerDue(t *testing.T) {
	now:=time.Now()
	if trackerKeepaliveCheckDue(now,map[string]TrackerKeepaliveRuntime{
		"tracker.example":{NextCheck:now.Add(time.Minute).Format(time.RFC3339)},
	}){
		t.Fatal("future observation must not be scheduled yet")
	}
	if !trackerKeepaliveCheckDue(now,map[string]TrackerKeepaliveRuntime{
		"tracker.example":{NextCheck:now.Add(-time.Second).Format(time.RFC3339)},
	}){
		t.Fatal("due keepalive observation must wake the scheduler")
	}
}

func TestSmartRepairRetainsLastKnownGoodWhenTrackerSampleMissing(t *testing.T) {
	dir:=t.TempDir()
	cfg:=defaultConfig()
	cfg.AutoApply=false
	cfg.Sync.Enabled=false
	cfg.Tracker.AutoDiscover=false
	cfg.Tracker.SamplesPath=filepath.Join(dir,"missing-manual.tsv")
	cfg.Tracker.AutoSamplesPath=filepath.Join(dir,"missing-auto.tsv")
	cfg.Domains=[]Domain{{Host:"tracker.example.com",Group:"example",Class:"latency",Mode:"tracker",Endpoint:"/announce",Enabled:true}}
	a:=&App{
		config:cfg,
		dataDir:dir,
		state:RuntimeState{
			Mappings:map[string]string{"tracker.example.com":"1.1.1.1"},
			DomainStatus:map[string]string{},
			DomainHealth:map[string]DomainHealth{},
			TrackerSamples:map[string]TrackerSampleRuntime{},
		},
	}
	if err:=a.runSmartRepair(context.Background(),cfg);err!=nil {
		t.Fatal(err)
	}
	if got:=a.state.Mappings["tracker.example.com"];got!="1.1.1.1" {
		t.Fatalf("last-known-good mapping was removed: %q",got)
	}
	if !strings.Contains(a.state.DomainStatus["tracker.example.com"],"stale retained") {
		t.Fatalf("status must expose stale retention: %q",a.state.DomainStatus["tracker.example.com"])
	}
	if a.state.DomainHealth["tracker.example.com"].FailureStreak!=1 {
		t.Fatalf("retained stale mapping must still count as failed health: %#v",a.state.DomainHealth["tracker.example.com"])
	}
}

func TestGroupHardFailureCache(t *testing.T) {
	cache:=map[string]map[string]bool{}
	markGroupHardFailure(cache,"mteam|latency","104.16.0.1")
	if !groupHardFailed(cache,"mteam|latency","104.16.0.1") {
		t.Fatal("hard failure was not cached")
	}
	if groupHardFailed(cache,"ptcafe|latency","104.16.0.1") {
		t.Fatal("hard failure leaked across groups")
	}
}
