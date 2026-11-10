package task

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XIU2/CloudflareSpeedTest/utils"
)

func TestDownloadRedirectCannotContactCoolingBackend(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Redirect(w, r, "http://cooling.example/file", http.StatusFound)
	}))
	defer server.Close()
	oldURL, oldPort, oldTimeout := URL, TCPPort, Timeout
	t.Cleanup(func() { URL = oldURL; TCPPort = oldPort; Timeout = oldTimeout })
	URL = server.URL
	parsed, _ := url.Parse(URL)
	TCPPort, _ = strconv.Atoi(parsed.Port())
	Timeout = time.Second
	t.Setenv("CFST_BLOCKED_ORIGINS", "http://cooling.example")
	_, _, blocked := downloadHandler(&net.IPAddr{IP: net.ParseIP("127.0.0.1")})
	if !blocked || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("cooling redirect was contacted: blocked=%v calls=%d", blocked, calls)
	}
}

func TestDownloadAttemptBudgetStopsUnproductiveSourceSweep(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	oldURL, oldPort, oldTimeout, oldCount, oldMin, oldDisable := URL, TCPPort, Timeout, TestCount, MinSpeed, Disable
	t.Cleanup(func() {
		URL = oldURL
		TCPPort = oldPort
		Timeout = oldTimeout
		TestCount = oldCount
		MinSpeed = oldMin
		Disable = oldDisable
	})
	URL = server.URL
	parsed, _ := url.Parse(URL)
	TCPPort, _ = strconv.Atoi(parsed.Port())
	Timeout = time.Second
	TestCount = 2
	MinSpeed = 100
	Disable = false
	t.Setenv("CFST_MAX_DOWNLOAD_ATTEMPTS", "3")
	t.Setenv("CFST_BLOCKED_ORIGINS", "")
	pool := utils.PingDelaySet{}
	for i := 0; i < 20; i++ {
		pool = append(pool, utils.CloudflareIPData{PingData: &utils.PingData{IP: &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, Sended: 4, Received: 4}})
	}
	got := TestDownloadSpeed(pool)
	if len(got) != 0 || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("unbounded source requests: candidates=%d calls=%d", len(got), calls)
	}
}
