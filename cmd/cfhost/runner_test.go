package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDefaultConfigHasManagedDomains(t *testing.T) {
	c := defaultConfig()
	if len(c.Domains) != 16 {
		t.Fatalf("expected 16 default domains, got %d", len(c.Domains))
	}
	if c.Domains[0].Mode != "tracker" || c.Domains[2].Mode != "http" {
		t.Fatal("default verifier modes are wrong")
	}
}
