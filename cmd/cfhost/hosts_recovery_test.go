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

func TestHostsRecoveryAfterSystemReplacementKeepsNewInode(t *testing.T) {
	dir:=t.TempDir()
	path:=filepath.Join(dir,"hosts")
	t.Setenv("HOSTS_VERIFY_PATH",path)
	t.Setenv("CFHOST_DOCKER_TARGETS","0")
	cfg:=defaultConfig(); cfg.HostsPath=path; cfg.AutoApply=true
	cfg.Domains=[]Domain{{Host:"example.test",Enabled:true}}
	if err:=os.WriteFile(path,[]byte("127.0.0.1 old\n"),0600);err!=nil {t.Fatal(err)}
	old,_:=os.Stat(path)
	a:=&App{dataDir:dir,config:cfg,state:RuntimeState{HostsBackup:newHostsBackup(map[string]string{"example.test":"192.0.2.7"})}}
	a.mirrorHostsBackup()
	if err:=os.WriteFile(path+".new",[]byte("127.0.0.1 new-nas\r\n# new system record\n"),0600);err!=nil {t.Fatal(err)}
	if err:=os.Rename(path+".new",path);err!=nil {t.Fatal(err)}
	before,_:=os.Stat(path)
	if os.SameFile(old,before) {t.Fatal("replacement did not change inode")}
	a.recoverHosts()
	after,_:=os.Stat(path)
	got,_:=os.ReadFile(path)
	if !os.SameFile(before,after) || !strings.HasPrefix(string(got),"127.0.0.1 new-nas\r\n# new system record\n") || !strings.Contains(string(got),"192.0.2.7 example.test") {t.Fatalf("recovery changed inode or system bytes: %q",got)}
}

func TestHostsWriteRefusesReplacementAndExternalEdit(t *testing.T) {
	dir:=t.TempDir()
	path:=filepath.Join(dir,"hosts")
	t.Setenv("HOSTS_VERIFY_PATH",path)
	before:=[]byte("127.0.0.1 old\n")
	if err:=os.WriteFile(path,before,0600);err!=nil {t.Fatal(err)}
	info,_:=os.Stat(path)
	external:=[]byte("127.0.0.1 external\n")
	if err:=os.WriteFile(path,external,0600);err!=nil {t.Fatal(err)}
	if err:=writeHostsInPlaceExpected(path,before,[]byte("new"),info);err==nil {t.Fatal("overwrote concurrent content")}
	if got,_:=os.ReadFile(path);string(got)!=string(external) {t.Fatal("external edit lost")}
	if err:=os.WriteFile(path+".new",before,0600);err!=nil {t.Fatal(err)}
	if err:=os.Rename(path+".new",path);err!=nil {t.Fatal(err)}
	if err:=writeHostsInPlaceExpected(path,before,[]byte("new"),info);err==nil {t.Fatal("overwrote replacement inode")}
	if got,_:=os.ReadFile(path);string(got)!=string(before) {t.Fatal("replacement changed")}
}

func TestHostsRollbackPreservesCurrentInode(t *testing.T) {
	dir:=t.TempDir()
	path:=filepath.Join(dir,"hosts")
	t.Setenv("HOSTS_VERIFY_PATH",path)
	cfg:=defaultConfig();cfg.HostsPath=path
	before:=[]byte("127.0.0.1 localhost\n")
	if err:=os.WriteFile(path,before,0600);err!=nil {t.Fatal(err)}
	info,_:=os.Stat(path)
	a:=&App{dataDir:dir}
	stage,err:=a.stageApplyMappings(cfg,map[string]string{"example.test":"192.0.2.8"})
	if err!=nil {t.Fatal(err)}
	if err:=stage.Rollback();err!=nil {t.Fatal(err)}
	after,_:=os.Stat(path)
	got,_:=os.ReadFile(path)
	if !os.SameFile(info,after)||string(got)!=string(before) {t.Fatalf("rollback: %q",got)}
}

func TestHostsWriteFailureRollbackChecksWrittenBytes(t *testing.T) {
	before := []byte("127.0.0.1 localhost\n")
	written := []byte("127.0.0.1 localhost\n# >>> CFHOST")
	external := []byte("127.0.0.1 updated-nas\n")
	for _, tc := range []struct {
		name      string
		current   []byte
		want      []byte
		wantError bool
	}{
		{name: "partial write", current: written, want: before},
		{name: "external edit", current: external, want: external, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hosts")
			t.Setenv("HOSTS_VERIFY_PATH", path)
			if err := os.WriteFile(path, tc.current, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			err = restoreOpenHostsSnapshot(path, f, info, written, before)
			if (err != nil) != tc.wantError {
				t.Fatalf("rollback error = %v, want error: %t", err, tc.wantError)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(tc.want) {
				t.Fatalf("Hosts contents after rollback = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
