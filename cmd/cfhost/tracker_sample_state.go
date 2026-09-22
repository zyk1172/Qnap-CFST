package main

import (
	"context"
	"os"
	"strings"
	"time"
)

type TrackerSampleRuntime struct {
	Available bool   `json:"available"`
	Source    string `json:"source,omitempty"`
	Tested    bool   `json:"tested"`
	Passed    bool   `json:"passed"`
	Detail    string `json:"detail,omitempty"`
	LastTest  string `json:"lastTest,omitempty"`
}

func (a *App) refreshTrackerSampleInventory(cfg Config) {
	manual, _ := loadTrackerSamples(cfg.Tracker.SamplesPath)
	auto, _ := loadTrackerSamples(cfg.Tracker.AutoSamplesPath)
	a.updateTrackerSampleInventory(cfg, manual, auto)

	keep := make(map[string]bool)
	for _, d := range cfg.Domains {
		if d.Mode == "tracker" {
			keep[d.Host] = true
		}
	}
	a.mu.Lock()
	for host := range a.state.TrackerSamples {
		if !keep[host] {
			delete(a.state.TrackerSamples, host)
		}
	}
	a.mu.Unlock()
}

func (a *App) updateTrackerSampleInventory(cfg Config, manual, auto map[string]TrackerSample) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state.TrackerSamples == nil {
		a.state.TrackerSamples = map[string]TrackerSampleRuntime{}
	}
	for _, d := range cfg.Domains {
		if d.Mode != "tracker" {
			continue
		}
		st := a.state.TrackerSamples[d.Host]
		source := ""
		if _, ok := manual[d.Host]; ok {
			source = "manual"
		} else if _, ok := auto[d.Host]; ok {
			source = "auto"
		}
		st.Available = source != ""
		st.Source = source
		if !st.Available {
			st.Tested = false
			st.Passed = false
			st.Detail = ""
			st.LastTest = ""
		}
		a.state.TrackerSamples[d.Host] = st
	}
}

func (a *App) markTrackerSamplesPending(domains []string) {
	if len(domains) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.TrackerSamples == nil {
		a.state.TrackerSamples = map[string]TrackerSampleRuntime{}
	}
	for _, host := range domains {
		st := a.state.TrackerSamples[host]
		if !st.Available || st.Source == "manual" {
			continue
		}
		st.Tested = false
		st.Passed = false
		st.Detail = "已获取，等待维护验证"
		st.LastTest = ""
		a.state.TrackerSamples[host] = st
	}
}

func (a *App) recordTrackerSampleTest(cfg Config, d Domain, ok bool, detail string) {
	if d.Mode != "tracker" || d.Class == "normal" || !cfg.Tracker.RealAnnounce {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.TrackerSamples == nil {
		a.state.TrackerSamples = map[string]TrackerSampleRuntime{}
	}
	st := a.state.TrackerSamples[d.Host]
	st.Available = true
	st.Tested = true
	st.Passed = ok
	st.Detail = strings.TrimSpace(detail)
	st.LastTest = time.Now().Format(time.RFC3339)
	a.state.TrackerSamples[d.Host] = st
}

func (a *App) verifyDomain(ctx context.Context, d Domain, ip string, cfg Config, samples map[string]TrackerSample) (bool, string) {
	ok, detail := verifyConfiguredDomain(ctx, d, ip, cfg, samples)
	if d.Mode == "tracker" && d.Class != "normal" && cfg.Tracker.RealAnnounce {
		if detail == "tracker sample missing" {
			a.mu.Lock()
			st := a.state.TrackerSamples[d.Host]
			st.Available = false
			st.Tested = false
			st.Passed = false
			st.Detail = detail
			st.LastTest = ""
			if a.state.TrackerSamples == nil {
				a.state.TrackerSamples = map[string]TrackerSampleRuntime{}
			}
			a.state.TrackerSamples[d.Host] = st
			a.mu.Unlock()
		} else {
			a.recordTrackerSampleTest(cfg, d, ok, detail)
		}
	}
	return ok, detail
}

// loadTrackerSamplesQuiet keeps startup/status inventory checks from treating a
// missing optional sample file as an error.
func loadTrackerSamplesQuiet(path string) map[string]TrackerSample {
	samples, err := loadTrackerSamples(path)
	if err != nil && !os.IsNotExist(err) {
		return samples
	}
	return samples
}
