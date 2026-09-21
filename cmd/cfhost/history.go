package main

import "sort"

// resolutionJob reports whether a job is responsible for producing domain
// mappings. A plain CFST "run" only refreshes the candidate pool and must not be
// labelled partial merely because some domains were already unresolved.
func resolutionJob(kind string) bool {
	return kind == "repair" || kind == "optimize"
}

// unresolvedDomains names enabled domains that ended a resolution job without
// a mapping.
func unresolvedDomains(cfg Config, mappings map[string]string) ([]string, int) {
	unresolved := make([]string, 0, len(cfg.Domains))
	for _, d := range cfg.Domains {
		if !d.Enabled {
			continue
		}
		if _, ok := mappings[d.Host]; !ok {
			unresolved = append(unresolved, d.Host)
		}
	}
	sort.Strings(unresolved)
	total := len(unresolved)
	const maxNames = 20
	if len(unresolved) > maxNames {
		unresolved = unresolved[:maxNames]
	}
	return unresolved, total
}

type RunRecord struct {
	Kind              string   `json:"kind"`
	StartedAt         string   `json:"startedAt"`
	FinishedAt        string   `json:"finishedAt"`
	DurationMS        int64    `json:"durationMs"`
	Success           bool     `json:"success"`
	Error             string   `json:"error,omitempty"`
	FullRefresh       bool     `json:"fullRefresh"`
	MappingsBefore    int      `json:"mappingsBefore"`
	MappingsAfter     int      `json:"mappingsAfter"`
	CandidateCount    int      `json:"candidateCount"`
	UnresolvedCount   int      `json:"unresolvedCount"`
	UnresolvedDomains []string `json:"unresolvedDomains,omitempty"`
}
