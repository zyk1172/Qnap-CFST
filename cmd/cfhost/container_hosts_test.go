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
