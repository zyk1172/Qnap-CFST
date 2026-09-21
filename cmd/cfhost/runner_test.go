package main

import (
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
	out := renderHosts(in, map[string]string{"a.example": "104.16.0.1", "b.example": "104.16.0.2"})
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
	out := renderHosts(in, map[string]string{})
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
	got := orderedCandidates(in, "3.3.3.3", "1.1.1.1")
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

func TestTrackerResponseOK(t *testing.T) {
	if ok, _ := trackerResponseOK([]byte("d8:intervali1800e5:peers0:e")); !ok {
		t.Fatal("valid announce response rejected")
	}
	if ok, _ := trackerResponseOK([]byte("d14:failure reason11:not allowede")); ok {
		t.Fatal("failure reason accepted")
	}
	if ok, _ := trackerResponseOK([]byte("<html>ok</html>")); ok {
		t.Fatal("non-bencode response accepted")
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
