package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// This is an infrastructure pause, independent of any site's failure count.
// It survives restarts; no alternate Hosts file or container control is needed.
type HostsMaintenanceRuntime struct {
	Blocked   bool   `json:"blocked"`
	Error     string `json:"error,omitempty"`
	Since     string `json:"since,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
	NextCheck string `json:"nextCheck,omitempty"`
	Failures  int    `json:"failures,omitempty"`
}

func maintenanceHostsError(cfg Config) error {
	if !cfg.AutoApply && !cfg.Sync.Enabled {
		return nil
	}
	if _, err := hostsBindingStatus(cfg.HostsPath); err != nil {
		return err
	}
	if !cfg.AutoApply {
		return nil
	}
	info, err := os.Lstat(cfg.HostsPath)
	if err != nil {
		return fmt.Errorf("read Hosts metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("Hosts path must be a regular file")
	}
	content, err := os.ReadFile(cfg.HostsPath)
	if err != nil {
		return fmt.Errorf("read Hosts: %w", err)
	}
	if err := validateManagedMarkers(string(content)); err != nil {
		return err
	}
	// Check write access without truncating or changing the file.
	f, err := os.OpenFile(cfg.HostsPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open Hosts for writing: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = hostsBindingStatus(cfg.HostsPath)
	return err
}

func (a *App) blockHostsMaintenance(err error, now time.Time) {
	a.mu.Lock()
	old := a.state.HostsMaintenance
	s := old
	s.Blocked, s.Error, s.CheckedAt = true, err.Error(), now.Format(time.RFC3339)
	if !old.Blocked {
		s.Since = s.CheckedAt
		s.Failures = 0
	}
	if s.Failures < 4 {
		s.Failures++
	}
	delay := 5 * time.Minute * time.Duration(1<<(s.Failures-1))
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	s.NextCheck = now.Add(delay).Format(time.RFC3339)
	a.state.HostsMaintenance = s
	a.mu.Unlock()
	if !old.Blocked || old.Error != s.Error {
		a.appendLog("maintenance paused: %v; only local Hosts checks will run until recovery", err)
	}
}

func (a *App) preflightMaintenanceHosts(cfg Config, now time.Time, force bool) error {
	a.mu.RLock()
	s := a.state.HostsMaintenance
	a.mu.RUnlock()
	if s.Blocked && !force && (cfg.AutoApply || cfg.Sync.Enabled) {
		if next, err := time.Parse(time.RFC3339, s.NextCheck); err == nil && now.Before(next) {
			return fmt.Errorf("maintenance paused: %s", s.Error)
		}
	}
	if err := maintenanceHostsError(cfg); err != nil {
		a.blockHostsMaintenance(err, now)
		a.persistState()
		return fmt.Errorf("maintenance paused: %w", err)
	}
	if s.Blocked {
		a.mu.Lock()
		a.state.HostsMaintenance = HostsMaintenanceRuntime{CheckedAt: now.Format(time.RFC3339)}
		a.mu.Unlock()
		a.appendLog("maintenance resumed: Hosts preflight passed; existing schedules and speed-test cooldowns retained")
		a.persistState()
	}
	return nil
}
