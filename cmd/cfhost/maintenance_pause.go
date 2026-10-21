package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const maintenanceFailureLimit = 30

// Derive the pause from persisted health so pre-upgrade failure streaks also
// stop maintenance immediately, without waiting for another failed attempt.
func maintenancePaused(d Domain, health map[string]DomainHealth) bool {
	return !isFollowDomain(d) && health[d.Host].FailureStreak >= maintenanceFailureLimit
}

func maintenancePauseStatus(h DomainHealth) string {
	return fmt.Sprintf("maintenance paused · consecutive failures %d/%d · resume manually", h.FailureStreak, maintenanceFailureLimit)
}

type MaintenancePause struct {
	Host          string `json:"host"`
	FailureStreak int    `json:"failureStreak"`
	LastFailure   string `json:"lastFailure"`
}

func pausedMaintenanceDomains(cfg Config, health map[string]DomainHealth) []MaintenancePause {
	paused := []MaintenancePause{}
	for _, d := range cfg.Domains {
		if d.Enabled && maintenancePaused(d, health) {
			h := health[d.Host]
			paused = append(paused, MaintenancePause{d.Host, h.FailureStreak, h.LastFailure})
		}
	}
	return paused
}

// Only background/probe inputs are filtered. Configuration and existing Hosts
// mappings stay intact; pausing maintenance does not disable a site.
func (a *App) maintenanceConfig(cfg Config) Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	active := cfg
	active.Domains = nil
	for _, d := range cfg.Domains {
		if !maintenancePaused(d, a.state.DomainHealth) {
			active.Domains = append(active.Domains, d)
		}
	}
	return active
}

func (a *App) handleDomainResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	request.Host = strings.ToLower(strings.TrimSpace(request.Host))
	a.mu.Lock()
	d, ok := configuredDomain(a.config, request.Host)
	if !ok || !d.Enabled || isFollowDomain(d) {
		a.mu.Unlock()
		http.Error(w, "enabled independent domain required", http.StatusBadRequest)
		return
	}
	if a.state.Running {
		a.mu.Unlock()
		http.Error(w, "another job is running", http.StatusConflict)
		return
	}
	if !maintenancePaused(d, a.state.DomainHealth) {
		a.mu.Unlock()
		http.Error(w, "domain maintenance is not paused", http.StatusConflict)
		return
	}
	oldHealth := a.state.DomainHealth[d.Host]
	oldStatus, hadStatus := a.state.DomainStatus[d.Host]
	oldKeepalive, hadKeepalive := a.state.TrackerKeepalive[d.Host]
	h := oldHealth
	h.FailureStreak = 0
	a.state.DomainHealth[d.Host] = h
	a.state.DomainStatus[d.Host] = "maintenance resumed · awaiting next repair"
	delete(a.state.TrackerKeepalive, d.Host)
	// Reserve the job slot until persistence completes so a scheduler or another
	// resume cannot race with rollback on a disk-write failure.
	a.state.Running = true
	a.mu.Unlock()
	err := a.persistStateStrict()
	a.mu.Lock()
	if err != nil {
		a.state.DomainHealth[d.Host] = oldHealth
		if hadStatus {
			a.state.DomainStatus[d.Host] = oldStatus
		} else {
			delete(a.state.DomainStatus, d.Host)
		}
		if hadKeepalive {
			a.state.TrackerKeepalive[d.Host] = oldKeepalive
		}
	}
	a.state.Running = false
	a.mu.Unlock()
	if err != nil {
		http.Error(w, "failed to persist maintenance resume", http.StatusInternalServerError)
		return
	}
	a.appendLog("maintenance resumed manually: %s", d.Host)
	writeResponse(w, map[string]string{"status": "resumed", "host": d.Host})
}
