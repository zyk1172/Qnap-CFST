package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	hostsBegin = "# >>> CFHOST MANAGED >>>"
	hostsEnd   = "# <<< CFHOST MANAGED <<<"
	legacyLatencyBegin = "# CF-YX-LATENCY-BEGIN"
	legacyLatencyEnd   = "# CF-YX-LATENCY-END"
	legacyBandwidthBegin = "# CF-YX-BANDWIDTH-BEGIN"
	legacyBandwidthEnd   = "# CF-YX-BANDWIDTH-END"
)

type hostsStage struct {
	Changed  bool
	Backup   string
	Rollback func() error
}

func managedMarkerPairs() map[string]string {
	return map[string]string{
		hostsBegin: hostsEnd,
		legacyLatencyBegin: legacyLatencyEnd,
		legacyBandwidthBegin: legacyBandwidthEnd,
	}
}

func validateManagedMarkers(content string) error {
	pairs := managedMarkerPairs()
	endToBegin := make(map[string]string, len(pairs))
	for b, e := range pairs { endToBegin[e] = b }
	countBegin := make(map[string]int)
	countEnd := make(map[string]int)
	active := ""
	for lineNo, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		s := strings.TrimSpace(line)
		if end, ok := pairs[s]; ok {
			countBegin[s]++
			if countBegin[s] > 1 { return fmt.Errorf("duplicate Hosts marker %q", s) }
			if active != "" { return fmt.Errorf("nested Hosts marker at line %d", lineNo+1) }
			active = end
			continue
		}
		if begin, ok := endToBegin[s]; ok {
			countEnd[s]++
			if countEnd[s] > 1 { return fmt.Errorf("duplicate Hosts marker %q", s) }
			if active == "" || active != s { return fmt.Errorf("unmatched Hosts marker %q at line %d (begin %q)", s, lineNo+1, begin) }
			active = ""
		}
	}
	if active != "" { return fmt.Errorf("missing Hosts end marker %q", active) }
	for b, e := range pairs {
		if countBegin[b] != countEnd[e] { return fmt.Errorf("incomplete Hosts marker pair %q / %q", b, e) }
	}
	return nil
}

func stripManagedBlocks(content string) (string, error) {
	if err := validateManagedMarkers(content); err != nil { return "", err }
	pairs := managedMarkerPairs()
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	activeEnd := ""
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if activeEnd != "" {
			if s == activeEnd { activeEnd = "" }
			continue
		}
		if end, ok := pairs[s]; ok {
			activeEnd = end
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), nil
}

func canonicalUnmanaged(content string) (string, error) {
	s, err := stripManagedBlocks(content)
	if err != nil { return "", err }
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" { lines = lines[:len(lines)-1] }
	return strings.Join(lines, "\n"), nil
}

func renderHosts(existing string, mappings map[string]string) (string, error) {
	base, err := stripManagedBlocks(existing)
	if err != nil { return "", err }
	out := strings.Split(base, "\n")
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" { out = out[:len(out)-1] }
	if len(mappings) > 0 {
		out = append(out, "", hostsBegin)
		hosts := make([]string, 0, len(mappings))
		for host, ip := range mappings {
			if net.ParseIP(ip) == nil { return "", fmt.Errorf("invalid mapping IP for %s: %s", host, ip) }
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		for _, host := range hosts { out = append(out, mappings[host]+" "+host) }
		out = append(out, hostsEnd)
	}
	out = append(out, "")
	return strings.Join(out, "\n"), nil
}

func (a *App) stageApplyMappings(cfg Config, mappings map[string]string) (hostsStage, error) {
	hostsPath := cfg.HostsPath
	info, err := os.Lstat(hostsPath)
	if err != nil { return hostsStage{}, fmt.Errorf("read hosts metadata: %w", err) }
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() { return hostsStage{}, errors.New("Hosts path must be a regular file") }
	current, err := os.ReadFile(hostsPath)
	if err != nil { return hostsStage{}, fmt.Errorf("read hosts: %w", err) }
	rendered, err := renderHosts(string(current), mappings)
	if err != nil { return hostsStage{}, err }
	beforeUnmanaged, err := canonicalUnmanaged(string(current))
	if err != nil { return hostsStage{}, err }
	afterUnmanaged, err := canonicalUnmanaged(rendered)
	if err != nil { return hostsStage{}, err }
	if beforeUnmanaged != afterUnmanaged { return hostsStage{}, errors.New("unmanaged Hosts content changed") }
	if rendered == string(current) {
		a.appendLog("hosts unchanged")
		return hostsStage{Changed:false, Rollback:func() error{return nil}}, nil
	}

	backup := filepath.Join(a.dataDir, "hosts-backup-"+time.Now().Format("20060102-150405.000000000"))
	if err := os.WriteFile(backup, current, info.Mode().Perm()); err != nil { return hostsStage{}, fmt.Errorf("backup hosts: %w", err) }
	restore := func() error {
		if err := writeHostsInPlace(hostsPath, current); err != nil { return err }
		got, err := os.ReadFile(hostsPath)
		if err != nil { return err }
		if !bytes.Equal(got, current) { return errors.New("Hosts rollback verification failed") }
		return nil
	}
	if err := writeHostsInPlace(hostsPath, []byte(rendered)); err != nil {
		_ = restore()
		return hostsStage{}, fmt.Errorf("write hosts: %w", err)
	}
	got, err := os.ReadFile(hostsPath)
	if err != nil || !bytes.Equal(got, []byte(rendered)) {
		_ = restore()
		if err != nil { return hostsStage{}, fmt.Errorf("verify hosts write: %w", err) }
		return hostsStage{}, errors.New("Hosts write verification failed")
	}
	a.pruneHostsBackups(cfg.Hosts.BackupRetention)
	a.appendLog("hosts applied: %d mappings", len(mappings))
	return hostsStage{Changed:true, Backup:backup, Rollback:restore}, nil
}

func writeHostsInPlace(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil { return err }
	if _, err := f.Write(content); err != nil { _ = f.Close(); return err }
	if err := f.Sync(); err != nil { _ = f.Close(); return err }
	return f.Close()
}

func (a *App) pruneHostsBackups(keep int) {
	if keep < 1 { return }
	matches, _ := filepath.Glob(filepath.Join(a.dataDir, "hosts-backup-*"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for i := keep; i < len(matches); i++ { _ = os.Remove(matches[i]) }
}

func (a *App) applyMappings(cfg Config, mappings map[string]string) error {
	_, err := a.stageApplyMappings(cfg, mappings)
	return err
}
