package main

type RunRecord struct {
	Kind           string `json:"kind"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt"`
	DurationMS     int64  `json:"durationMs"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
	FullRefresh    bool   `json:"fullRefresh"`
	MappingsBefore int    `json:"mappingsBefore"`
	MappingsAfter  int    `json:"mappingsAfter"`
	CandidateCount int    `json:"candidateCount"`
}
