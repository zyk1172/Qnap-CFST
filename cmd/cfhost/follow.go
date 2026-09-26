package main

import "fmt"

func isFollowDomain(d Domain) bool {
	return d.Class == "follow"
}

func configuredFollowTarget(cfg Config, d Domain) (Domain, bool) {
	if !isFollowDomain(d) || d.Follow == "" {
		return Domain{}, false
	}
	for _, target := range cfg.Domains {
		if target.Host == d.Follow {
			return target, true
		}
	}
	return Domain{}, false
}

// applyFollowMapping resolves one follower after its target has finished its own
// strategy. Followers never probe candidates, consume Tracker samples, or
// trigger CFST themselves.
func applyFollowMapping(cfg Config, d Domain, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth) bool {
	target, ok := configuredFollowTarget(cfg, d)
	if !ok {
		delete(mappings, d.Host)
		delete(health, d.Host)
		statuses[d.Host] = "follow unresolved · target missing · " + d.Follow
		return false
	}
	if !target.Enabled {
		delete(mappings, d.Host)
		delete(health, d.Host)
		statuses[d.Host] = "follow unresolved · target disabled · " + target.Host
		return false
	}
	ip := mappings[target.Host]
	if ip == "" {
		delete(mappings, d.Host)
		delete(health, d.Host)
		statuses[d.Host] = "follow unresolved · target has no mapping · " + target.Host
		return false
	}

	mappings[d.Host] = ip
	statuses[d.Host] = fmt.Sprintf("follow · %s → %s", target.Host, ip)
	if targetHealth, ok := health[target.Host]; ok {
		health[d.Host] = targetHealth
	} else {
		delete(health, d.Host)
	}
	return true
}

// applyFollowMappings resolves follow domains only after all independent
// strategies have finished selecting/verifying their own mappings. Follow chains
// are rejected during config normalization, so this pass is deterministic and
// cycle-free.
func applyFollowMappings(cfg Config, mappings map[string]string, statuses map[string]string, health map[string]DomainHealth) int {
	unresolved := 0
	for _, d := range cfg.Domains {
		if !d.Enabled || !isFollowDomain(d) {
			continue
		}
		if !applyFollowMapping(cfg, d, mappings, statuses, health) {
			unresolved++
		}
	}
	return unresolved
}
