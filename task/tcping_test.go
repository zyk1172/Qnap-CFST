package task

import (
	"net"
	"sync"
	"testing"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

func TestConcurrentTCPProbesKeepAllResults(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	defer func() { listener.Close(); <-accepted }()
	oldPort, oldTimes, oldHTTP := TCPPort, PingTimes, Httping
	TCPPort, PingTimes, Httping = listener.Addr().(*net.TCPAddr).Port, 1, false
	t.Cleanup(func() { TCPPort, PingTimes, Httping = oldPort, oldTimes, oldHTTP })
	const probes = 64
	ips := make([]*net.IPAddr, probes)
	for i := range ips {
		ips[i] = &net.IPAddr{IP: net.ParseIP("127.0.0.1")}
	}
	ping := &Ping{
		wg: &sync.WaitGroup{}, m: &sync.Mutex{}, ips: ips,
		control: make(chan bool, probes), bar: utils.NewBar(probes, "可用:", ""),
	}
	results := ping.Run()
	if len(results) != probes {
		t.Fatalf("got %d probe results, want %d", len(results), probes)
	}
	for _, result := range results {
		if result.Received != 1 || result.Sended != 1 {
			t.Fatalf("unexpected probe result: %+v", result)
		}
	}
}
