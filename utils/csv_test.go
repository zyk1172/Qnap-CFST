package utils

import (
	"net"
	"sort"
	"testing"
	"time"
)

func TestDelayFilterKeepsEligibleIPsAcrossLossGroups(t *testing.T) {
	oldMax, oldMin, oldLoss := InputMaxDelay, InputMinDelay, InputMaxLossRate
	defer func() { InputMaxDelay, InputMinDelay, InputMaxLossRate = oldMax, oldMin, oldLoss }()
	InputMaxDelay = 350 * time.Millisecond
	InputMinDelay = 10 * time.Millisecond
	InputMaxLossRate = 0.2
	data := PingDelaySet{
		{PingData: &PingData{IP: &net.IPAddr{IP: net.ParseIP("104.16.0.1")}, Sended: 10, Received: 10, Delay: 400 * time.Millisecond}},
		{PingData: &PingData{IP: &net.IPAddr{IP: net.ParseIP("104.16.0.2")}, Sended: 10, Received: 9, Delay: 20 * time.Millisecond}},
		{PingData: &PingData{IP: &net.IPAddr{IP: net.ParseIP("104.16.0.3")}, Sended: 10, Received: 8, Delay: 5 * time.Millisecond}},
		{PingData: &PingData{IP: &net.IPAddr{IP: net.ParseIP("104.16.0.4")}, Sended: 10, Received: 7, Delay: 15 * time.Millisecond}},
	}
	sort.Sort(data)
	got := data.FilterDelay().FilterLossRate()
	if len(got) != 1 || got[0].IP.String() != "104.16.0.2" {
		t.Fatalf("unexpected filtered IPs: %#v", got)
	}
}
