package main

import "fmt"

func isFollowDomain(d Domain) bool {
	return d.Class == "follow"
}

// applyFollowMappings resolves follow domains only after all independent
// strategies have finished selecting/verifying their own mappings.
//
// A follow domain never probes candidates, never consumes a Tracker sample, and
// never triggers CFST by itself. It simply mirrors the current mapping of one
// existing non-follow domain. Follow chains are rejected during config
// normalization, so this pass is deterministic and cycle-free.
func applyFollowMappings(cfg Config, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth) {
	byHost := make(map[string]Domain, len(cfg.Domains))
	for _, d := range cfg.Domains {
		byHost[d.Host] = d
	}

	for _, d := range cfg.Domains {
		if !d.Enabled || !isFollowDomain(d) {
			continue
		}
		target, ok := byHost[d.Follow]
		if !ok {
			delete(mappings, d.Host)
			statuses[d.Host] = "follow unresolved · target missing · " + d.Follow
			continue
		}
		if !target.Enabled {
			delete(mappings, d.Host)
			statuses[d.Host] = "follow unresolved · target disabled · " + target.Host
			continue
		}
		ip := mappings[target.Host]
		if ip == "" {
			delete(mappings, d.Host)
			statuses[d.Host] = "follow unresolved · target has no mapping · " + target.Host
			continue
		}

		mappings[d.Host] = ip
		statuses[d.Host] = fmt.Sprintf("follow · %s → %s", target.Host, ip)
		if targetHealth, ok := health[target.Host]; ok {
			health[d.Host] = targetHealth
		} else {
			delete(health, d.Host)
		}
	}
}
