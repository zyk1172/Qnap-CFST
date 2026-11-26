package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostsRecoveryCurrentInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	t.Setenv("HOSTS_VERIFY_PATH", path)
	cfg := defaultConfig()
	cfg.HostsPath = path
	cfg.AutoApply = true
	cfg.Domains = []Domain{{Host:"example.test", Enabled:true}}
	if err := os.WriteFile(path, []byte("127.0.0.1 new-nas\n"), 0600); err != nil { t.Fatal(err) }
	before, _ := os.Stat(path)
	a := &App{config:cfg, dataDir:dir, state:RuntimeState{HostsBackup:newHostsBackup(map[string]string{"example.test":"192.0.2.2"})}}
	a.recoverHosts()
	after, _ := os.Stat(path)
	got, _ := os.ReadFile(path)
	if !os.SameFile(before,after) || !strings.Contains(string(got), "127.0.0.1 new-nas") || !strings.Contains(string(got), "192.0.2.2 example.test") { t.Fatalf("recovery: %s",got) }
	if _, err := os.Stat(filepath.Join(dir,"hosts-last-applied.json")); err != nil { t.Fatal(err) }
}

func TestRecoveryDoesNotRestoreAfterClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir,"hosts")
	cfg := defaultConfig(); cfg.HostsPath = path; cfg.AutoApply = false
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"),0600); err != nil { t.Fatal(err) }
	a := &App{config:cfg,dataDir:dir,state:RuntimeState{HostsClearedAt:"cleared", HostsBackup:newHostsBackup(map[string]string{})}}
	a.recoverHosts()
	got,_ := os.ReadFile(path)
	if string(got)!="127.0.0.1 localhost\n" { t.Fatalf("restored after clear: %s",got) }
}
