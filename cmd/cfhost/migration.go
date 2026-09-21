package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func (a *App) migrateLegacy(cfg Config) error {
	a.mu.RLock()
	hasState := len(a.state.Mappings) > 0
	a.mu.RUnlock()
	if hasState { return nil }

	configured := make(map[string]bool)
	for _, d := range cfg.Domains { if d.Enabled { configured[d.Host] = true } }
	imported := make(map[string]string)

	if path := strings.TrimSpace(cfg.Hosts.LegacyStateMapPath); path != "" {
		if m, err := readLegacyStateMap(path, configured); err == nil {
			for k,v := range m { imported[k]=v }
		} else if !os.IsNotExist(err) {
			a.appendLog("legacy state map warning: %v", err)
		}
	}
	if b, err := os.ReadFile(cfg.HostsPath); err == nil {
		m, parseErr := mappingsFromManagedBlocks(string(b), configured)
		if parseErr != nil { return parseErr }
		for k,v := range m { imported[k]=v }
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read Hosts for migration: %w", err)
	}
	if len(imported) == 0 { return nil }

	now := time.Now().Format(time.RFC3339)
	a.mu.Lock()
	for host, ip := range imported {
		a.state.Mappings[host] = ip
		a.state.DomainStatus[host] = "migrated · pending verification"
	}
	a.state.MigrationAt = now
	a.state.MigrationStatus = fmt.Sprintf("imported %d mappings; pending Repair verification", len(imported))
	a.mu.Unlock()
	if err := a.persistStateStrict(); err != nil { return err }
	a.appendLog("legacy migration imported %d mappings; old markers will be removed on first successful Hosts apply", len(imported))
	return nil
}

func readLegacyStateMap(path string, configured map[string]bool) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil { return nil, err }
	defer f.Close()
	out := make(map[string]string)
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") { continue }
		p := strings.Split(line, "\t")
		if len(p) < 2 { continue }
		host := strings.ToLower(strings.TrimSpace(p[0]))
		ip := strings.TrimSpace(p[1])
		if configured[host] && net.ParseIP(ip) != nil { out[host]=ip }
	}
	return out, s.Err()
}

func mappingsFromManagedBlocks(content string, configured map[string]bool) (map[string]string, error) {
	if err := validateManagedMarkers(content); err != nil { return nil, err }
	pairs := managedMarkerPairs()
	out := make(map[string]string)
	activeEnd := ""
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		s := strings.TrimSpace(line)
		if activeEnd != "" {
			if s == activeEnd { activeEnd="" ; continue }
			fields := strings.Fields(s)
			if len(fields) >= 2 {
				ip, host := fields[0], strings.ToLower(fields[1])
				if configured[host] && net.ParseIP(ip) != nil { out[host]=ip }
			}
			continue
		}
		if end, ok := pairs[s]; ok { activeEnd=end }
	}
	return out, nil
}
