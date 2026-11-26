package main

import (
	"strings"
	"testing"
)

func TestContainerHostsRenderPreservesUnmanaged(t *testing.T) {
	original := "127.0.0.1 localhost\r\n172.17.0.2 jellyfin\n# another tool\n"
	next, err := renderContainerHosts(original, map[string]string{"example.test":"192.0.2.1"})
	if err != nil || !strings.HasPrefix(next,original) { t.Fatalf("render: %q %v", next,err) }
	next2, err := renderContainerHosts(next, map[string]string{"example.test":"192.0.2.2"})
	if err != nil || !strings.HasPrefix(next2,original) || strings.Contains(next2,"192.0.2.1") { t.Fatalf("update: %q %v",next2,err) }
	unchanged, err := renderContainerHosts(next2,map[string]string{"example.test":"192.0.2.2"})
	if err!=nil || unchanged!=next2 { t.Fatalf("not idempotent: %q %v",unchanged,err) }
	cleared,err:=renderContainerHosts(next2,nil)
	if err!=nil || cleared!=original { t.Fatalf("clear: %q %v",cleared,err) }
}

func TestExplicitContainerHostsMountRejected(t *testing.T) {
	for _, path:=range []string{"/etc/hosts","/etc","/"} {
		if !hasExplicitHostsMount([]inspectedContainerMount{{Destination:path}}) { t.Fatalf("allowed %s",path) }
	}
	if hasExplicitHostsMount([]inspectedContainerMount{{Destination:"/config"}}) { t.Fatal("rejected unrelated mount") }
}

func TestContainerCleanupQueue(t *testing.T) {
	old := HostsConfig{ContainerTargets:[]string{"Jellyfin","Transmission"}, ContainerCleanup:[]string{"Plex","Immich"}}
	got := reconcileContainerCleanup(old, []string{"Transmission","Plex"})
	if strings.Join(got,",")!="Immich,Jellyfin" { t.Fatalf("queue: %v",got) }
	cfg:=Config{Hosts:old}
	removeContainerTarget(&cfg,"Jellyfin")
	if strings.Join(cfg.Hosts.ContainerTargets,",")!="Transmission" || strings.Join(cfg.Hosts.ContainerCleanup,",")!="Immich,Jellyfin,Plex" { t.Fatalf("single clear: %+v",cfg.Hosts) }
	removeContainerTarget(&cfg,"Jellyfin")
	if strings.Join(cfg.Hosts.ContainerCleanup,",")!="Immich,Jellyfin,Plex" { t.Fatalf("not idempotent: %v",cfg.Hosts.ContainerCleanup) }
}

func TestContainerCleanupOnlyCFHostMarker(t *testing.T) {
	original := "127.0.0.1 localhost\r\n# another tool\n192.0.2.4 other.test\n# CF-YX-LATENCY-BEGIN\n192.0.2.5 legacy.test\n# CF-YX-LATENCY-END\n"
	current:=original+hostsBegin+"\n192.0.2.6 managed.test\n"+hostsEnd+"\n"
	got,err:=renderContainerHosts(current,nil)
	if err!=nil || got!=original { t.Fatalf("cleanup changed unmanaged bytes: %q %v",got,err) }
	if _,err:=renderContainerHosts(original+hostsBegin+"\n",nil); err==nil { t.Fatal("malformed marker accepted") }
}
