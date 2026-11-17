package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptrace"
	"sort"
	"sync"
	"time"
)

// Only unfinished work is checkpointed. Completed domains are not re-probed
// on every continuation, and candidate failures remain in CandidateSweeps.
type AutomaticDomainWork struct {
	Key         string `json:"key"`
	CurrentIP   string `json:"currentIP,omitempty"`
	SkipIP      string `json:"skipIP,omitempty"`
	Candidates  bool   `json:"candidates"`
	LastDetail  string `json:"lastDetail,omitempty"`
	LastAttempt string `json:"lastAttempt,omitempty"`
}

type AutomaticRepairRuntime struct {
	LastSweep string                         `json:"lastSweep,omitempty"`
	Recheck   bool                           `json:"recheck,omitempty"`
	Pending   map[string]AutomaticDomainWork `json:"pending,omitempty"`
	NextCheck string                         `json:"nextCheck,omitempty"`
	Optimize  bool                           `json:"optimize,omitempty"`
	Stages    map[string]int64               `json:"stages,omitempty"`
	Passed    int                            `json:"passed"`
	Failed    int                            `json:"failed"`
	Deferred  int                            `json:"deferred"`
}

type automaticRepairPolicy struct {
	Slice, Domain, ContinueAfter, StartGap time.Duration
	Workers                                int
}

func automaticPolicy(cfg Config) automaticRepairPolicy {
	p := automaticRepairPolicy{120 * time.Second, 30 * time.Second, 30 * time.Second, 250 * time.Millisecond, 3}
	if cfg.Repair.TimeSliceSeconds > 0 {
		p.Slice = time.Duration(cfg.Repair.TimeSliceSeconds) * time.Second
	}
	if cfg.Repair.DomainSliceSeconds > 0 {
		p.Domain = time.Duration(cfg.Repair.DomainSliceSeconds) * time.Second
	}
	if cfg.Repair.ContinueAfterSeconds > 0 {
		p.ContinueAfter = time.Duration(cfg.Repair.ContinueAfterSeconds) * time.Second
	}
	if cfg.Repair.Concurrency > 0 {
		p.Workers = cfg.Repair.Concurrency
	}
	return p
}

func automaticWorkKey(cfg Config, d Domain, ip string) string {
	// Include probe credentials/configuration identity, but persist only a hash.
	b, _ := json.Marshal(struct {
		Domain    Domain
		Verify    VerifyConfig
		Tracker   TrackerConfig
		Bandwidth BandwidthConfig
		Timeout   int
		IP        string
	}{d, cfg.Verify, cfg.Tracker, cfg.Bandwidth, cfg.VerifyTimeoutSeconds, ip})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func copyAutomaticWork(in map[string]AutomaticDomainWork) map[string]AutomaticDomainWork {
	out := make(map[string]AutomaticDomainWork, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type automaticProbeFunc func(context.Context, Domain, string, Config, map[string]TrackerSample) (bool, string)

// Throttle starts across workers; serialize real announce probes separately.
// A shared-IP failure is reusable only when the actual TCP connect failed.
// HTTP status, TLS and application failures never poison other domains.
type automaticProbeGate struct {
	mu              sync.Mutex
	next            time.Time
	gap             time.Duration
	tracker         chan struct{}
	connectFailures map[string]time.Time
}

func (g *automaticProbeGate) verify(ctx context.Context, d Domain, ip string, cfg Config, samples map[string]TrackerSample, probe automaticProbeFunc) (bool, string) {
	if d.Mode == "tracker" && cfg.Tracker.RealAnnounce {
		select {
		case g.tracker <- struct{}{}:
			defer func() { <-g.tracker }()
		case <-ctx.Done():
			return false, ctx.Err().Error()
		}
	}
	g.mu.Lock()
	at := g.next
	if now := time.Now(); now.After(at) {
		at = now
	}
	g.next = at.Add(g.gap)
	g.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false, ctx.Err().Error()
	}
	g.mu.Lock()
	failedUntil := g.connectFailures[ip]
	g.mu.Unlock()
	if time.Now().Before(failedUntil) {
		return false, "connection failed · shared IP TCP failure observed this round"
	}
	trace := &httptrace.ClientTrace{ConnectDone: func(network, address string, err error) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil || host != ip || ctx.Err() != nil {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if err != nil {
			g.connectFailures[ip] = time.Now().Add(5 * time.Second)
		} else {
			delete(g.connectFailures, ip)
		}
	}}
	return probe(httptrace.WithClientTrace(ctx, trace), d, ip, cfg, samples)
}

type automaticDomainResult struct {
	Domain                     Domain
	Work                       AutomaticDomainWork
	IP, Detail                 string
	Done, Passed, NeedsRefresh bool
}

func (a *App) verifyAutomaticDomain(ctx context.Context, cfg Config, samples map[string]TrackerSample, candidates []Candidate, d Domain, work AutomaticDomainWork, downloader map[string]downloaderTrackerFailure, health DomainHealth, gate *automaticProbeGate, probe automaticProbeFunc) automaticDomainResult {
	r := automaticDomainResult{Domain: d, Work: work, IP: work.CurrentIP}
	if normalModeSkipsVerification(d) {
		order := orderedCandidates(candidates, d, "", "", cfg)
		r.Done = true
		if len(order) > 0 {
			r.IP = order[0].IP
			r.Passed = true
			r.Detail = "normal · lowest latency · verification skipped"
		} else if r.IP != "" {
			r.Passed = true
			r.Detail = "normal retained · no fresh candidate · verification skipped"
		} else {
			r.NeedsRefresh = true
			r.Detail = "no CFST candidate"
		}
		return r
	}
	if !domainRefreshable(d, cfg, samples) {
		r.Done = true
		r.Detail = "tracker sample missing"
		return r
	}
	if !r.Work.Candidates {
		if r.IP != "" {
			failure, reported := downloader[d.Host]
			if reported && (failure.Hard || downloaderFailureIsNewer(failure, health.LastSuccess)) {
				r.Work.LastDetail = "downloader reported tracker connection failure · " + failure.Detail
				if failure.RejectedIP != "" {
					r.Work.SkipIP = failure.RejectedIP
				}
			} else {
				ok, detail := gate.verify(ctx, d, r.IP, cfg, samples, probe)
				if ctx.Err() != nil {
					return r
				}
				if ok {
					r.Done = true
					r.Passed = true
					r.Detail = "retained · " + r.IP + " · " + detail
					return r
				}
				r.Work.LastDetail = detail
			}
			if !failureNeedsCandidates(r.Work.LastDetail) {
				r.Done = true
				r.Detail = r.Work.LastDetail
				return r
			}
		}
		r.Work.Candidates = true
	}
	order := a.candidateBatch(candidates, d, r.Work.SkipIP, cfg)
	for _, c := range order {
		if ctx.Err() != nil {
			return r
		}
		ok, detail := gate.verify(ctx, d, c.IP, cfg, samples, probe)
		// Cancellation is unfinished work, never failed candidate evidence.
		if ctx.Err() != nil {
			return r
		}
		a.recordCandidateAttempt(d.Host, c, ok)
		r.Work.LastDetail = detail
		if ok {
			r.IP = c.IP
			r.Done = true
			r.Passed = true
			r.Detail = "verified · " + c.IP + " · " + detail
			return r
		}
		if !failureNeedsCandidates(detail) {
			r.Done = true
			r.Detail = detail
			return r
		}
	}
	if a.cachedCandidatesRemaining(d.Host) > 0 {
		return r
	}
	r.Done = true
	r.NeedsRefresh = true
	r.Detail = r.Work.LastDetail
	if r.Detail == "" {
		r.Detail = "no verified candidate"
	}
	return r
}

func (a *App) runAutomaticRepair(ctx context.Context, cfg Config) error {
	return a.runAutomaticRepairWithPolicy(ctx, cfg, automaticPolicy(cfg), a.verifyDomain)
}

func (a *App) runAutomaticRepairWithPolicy(ctx context.Context, cfg Config, policy automaticRepairPolicy, probe automaticProbeFunc) error {
	if err := a.preflightMaintenanceHosts(cfg, time.Now(), true); err != nil {
		return err
	}
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, policy.Slice)
	defer cancel()
	stages := map[string]int64{}
	a.mu.Lock()
	current := copyMappings(a.state.Mappings)
	health := copyHealth(a.state.DomainHealth)
	statuses := copyStatuses(a.state.DomainStatus)
	work := copyAutomaticWork(a.state.AutomaticRepair.Pending)
	lastSweep := a.state.AutomaticRepair.LastSweep
	clock := lastSweep
	if clock == "" {
		clock = a.state.LastRun
	}
	optimizing := a.state.AutomaticRepair.Optimize && cfg.Optimize.ScheduledFull
	if a.state.AutomaticRepair.Recheck {
		work = map[string]AutomaticDomainWork{}
	}
	if a.state.CandidateRefresh.ReadyOptimize && cfg.Optimize.ScheduledFull {
		optimizing = true
	}
	hadReady := len(a.state.CandidateRefresh.ReadyTargets) > 0
	readyAt, _ := time.Parse(time.RFC3339Nano, a.state.CandidateRefresh.FinishedAt)
	for _, host := range a.state.CandidateRefresh.ReadyTargets {
		lastSuccess, _ := time.Parse(time.RFC3339Nano, health[host].LastSuccess)
		if !optimizing && !readyAt.IsZero() && lastSuccess.After(readyAt) {
			continue
		}
		if d, ok := configuredDomain(cfg, host); ok && d.Enabled && !isFollowDomain(d) && !maintenancePaused(d, health) {
			work[host] = AutomaticDomainWork{Key: automaticWorkKey(cfg, d, current[host]), CurrentIP: current[host], SkipIP: current[host], Candidates: true}
			if optimizing {
				w := work[host]
				w.SkipIP = ""
				work[host] = w
			}
		}
	}
	a.state.CandidateRefresh.ReadyTargets = nil
	a.state.CandidateRefresh.ReadyOptimize = false
	a.state.AutomaticRepair.Pending = copyAutomaticWork(work)
	a.state.AutomaticRepair.Optimize = optimizing
	cached := repairCandidates(append([]Candidate(nil), a.state.Candidates...), current, cfg, started)
	a.mu.Unlock()
	mappings := retainEnabledMappings(current, cfg)
	continuing := len(work) > 0
	lastSweepAt, _ := time.Parse(time.RFC3339Nano, clock)
	regularDue := (!continuing && !hadReady) || (cfg.AutoRepair && (lastSweepAt.IsZero() || started.Sub(lastSweepAt) >= time.Duration(cfg.RepairIntervalMinutes)*time.Minute))
	if regularDue {
		lastSweep = started.Format(time.RFC3339Nano)
	}
	active := map[string]bool{}
	for _, d := range cfg.Domains {
		if !d.Enabled {
			delete(mappings, d.Host)
			statuses[d.Host] = "disabled"
			delete(work, d.Host)
			continue
		}
		if isFollowDomain(d) {
			continue
		}
		if maintenancePaused(d, health) {
			statuses[d.Host] = maintenancePauseStatus(health[d.Host])
			delete(work, d.Host)
			continue
		}
		active[d.Host] = true
		w, exists := work[d.Host]
		key := automaticWorkKey(cfg, d, current[d.Host])
		invalidated := exists && w.Key != key
		if invalidated {
			delete(work, d.Host)
			exists = false
		}
		if !exists && !invalidated && (!regularDue || !domainCheckDue(health[d.Host], started)) {
			continue
		}
		if !exists {
			work[d.Host] = AutomaticDomainWork{Key: key, CurrentIP: current[d.Host], SkipIP: current[d.Host], Candidates: optimizing}
			if optimizing {
				w := work[d.Host]
				w.SkipIP = ""
				work[d.Host] = w
			}
		}
	}
	for host := range work {
		if !active[host] {
			delete(work, host)
		}
	}
	a.mu.Lock()
	a.state.AutomaticRepair.Pending = copyAutomaticWork(work)
	a.state.AutomaticRepair.Recheck = false
	a.state.AutomaticRepair.LastSweep = lastSweep
	a.mu.Unlock()
	if err := a.persistStateStrict(); err != nil {
		return fmt.Errorf("persist automatic work before verification: %w", err)
	}
	scope := cfg
	scope.Domains = nil
	for _, d := range cfg.Domains {
		if _, ok := work[d.Host]; ok {
			scope.Domains = append(scope.Domains, d)
		}
	}
	// A failed downloader must not spend the entire verification slice before
	// the first domain is checked. Existing cached/manual samples remain usable.
	a.setJobProgress("读取样本", "自动维护时间片", 1, 4, 0, 0)
	sampleStarted := time.Now()
	sampleBudget := policy.Slice / 4
	if sampleBudget > 15*time.Second {
		sampleBudget = 15 * time.Second
	}
	sampleCtx, sampleCancel := context.WithTimeout(probeCtx, sampleBudget)
	samples := a.loadSamples(sampleCtx, scope)
	downloader := a.loadDownloaderTrackerFailures(sampleCtx, scope, samples)
	sampleCancel()
	stages["samples"] = time.Since(sampleStarted).Milliseconds()
	if err := ctx.Err(); err != nil {
		return err
	}
	// One attempt per (domain, IP) in automatic work. Further candidates/rounds
	// provide retries without multiplying every timeout by inline retry counts.
	probeCfg := cfg
	probeCfg.Verify.HTTPRetries = 1
	probeCfg.Tracker.Retries = 1
	// Preserve legacy/manual timeout settings, while allowing an automatic
	// request to finish before its enclosing slice expires.
	requestSeconds := int(policy.Domain / time.Second / 2)
	if sliceSeconds := int(policy.Slice / time.Second / 2); sliceSeconds < requestSeconds {
		requestSeconds = sliceSeconds
	}
	if requestSeconds < 1 {
		requestSeconds = 1
	}
	if probeCfg.VerifyTimeoutSeconds > requestSeconds {
		probeCfg.VerifyTimeoutSeconds = requestSeconds
	}
	gate := &automaticProbeGate{gap: policy.StartGap, tracker: make(chan struct{}, 1), connectFailures: map[string]time.Time{}}
	results := make(chan automaticDomainResult, len(work))
	tasks := append([]Domain(nil), scope.Domains...)
	sort.SliceStable(tasks, func(i, j int) bool { return work[tasks[i].Host].LastAttempt < work[tasks[j].Host].LastAttempt })
	jobs := make(chan Domain, len(tasks))
	for _, d := range tasks {
		jobs <- d
	}
	close(jobs)
	var wg sync.WaitGroup
	verifyStarted := time.Now()
	for i := 0; i < policy.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				w := work[d.Host]
				if probeCtx.Err() != nil {
					results <- automaticDomainResult{Domain: d, Work: w, IP: w.CurrentIP}
					continue
				}
				domainCtx, domainCancel := context.WithTimeout(probeCtx, policy.Domain)
				r := a.verifyAutomaticDomain(domainCtx, probeCfg, samples, cached, d, w, downloader, health[d.Host], gate, probe)
				domainCancel()
				r.Work.LastAttempt = time.Now().Format(time.RFC3339Nano)
				results <- r
			}
		}()
	}
	a.setJobProgress("验证域名", fmt.Sprintf("%d 个域名 · 并发 %d", len(tasks), policy.Workers), 2, 4, 0, len(tasks))
	wg.Wait()
	close(results)
	stages["verification"] = time.Since(verifyStarted).Milliseconds()
	if err := ctx.Err(); err != nil {
		return err
	}
	passed, failed := 0, 0
	refreshTargets := []string{}
	for r := range results {
		host := r.Domain.Host
		if !r.Done {
			work[host] = r.Work
			statuses[host] = "unresolved · verification deferred · continuation pending"
			if r.IP != "" {
				statuses[host] += " · stale retained · " + r.IP
			}
			continue
		}
		delete(work, host)
		if r.Passed {
			mappings[host] = r.IP
			statuses[host] = r.Detail
			health[host] = a.candidateOutcome(health[host], true, "", time.Now(), cfg, host)
			h := health[host]
			h.LastSuccess = time.Now().Format(time.RFC3339Nano)
			health[host] = h
			passed++
		} else {
			failed++
			statuses[host] = "unresolved · " + r.Detail
			if r.IP != "" {
				statuses[host] += " · stale retained · " + r.IP
			}
			health[host] = domainOutcome(health[host], false, r.Detail, time.Now(), cfg, host)
			if r.NeedsRefresh && !maintenancePaused(r.Domain, health) {
				refreshTargets = append(refreshTargets, host)
			}
		}
	}
	for _, d := range cfg.Domains {
		if d.Enabled && !isFollowDomain(d) && !maintenancePaused(d, health) && !domainCheckDue(health[d.Host], started) {
			if _, wasWork := work[d.Host]; !wasWork && statuses[d.Host] == "" {
				statuses[d.Host] = "unresolved · retry deferred until " + health[d.Host].NextCheck
			}
		}
	}
	applyFollowMappings(cfg, mappings, statuses, health)
	configured := map[string]bool{}
	for _, d := range cfg.Domains {
		if d.Enabled {
			configured[d.Host] = true
		}
	}
	for host := range health {
		if !configured[host] {
			delete(health, host)
		}
	}
	optimized := optimizing && len(work) == 0
	a.setJobProgress("应用结果", fmt.Sprintf("通过 %d · 失败 %d · 未完成 %d", passed, failed, len(work)), 3, 4, 0, 0)
	commitStarted := time.Now()
	if automaticConfigKey(cfg) != automaticConfigKey(a.snapshotConfig()) {
		a.mu.Lock()
		a.state.AutomaticRepair.Recheck = true
		a.state.AutomaticRepair.NextCheck = time.Now().Add(policy.ContinueAfter).Format(time.RFC3339Nano)
		a.state.AutomaticRepair.Stages = stages
		a.state.AutomaticRepair.Passed = 0
		a.state.AutomaticRepair.Failed = 0
		a.mu.Unlock()
		a.appendLog("repair slice: configuration changed; unfinished work will be rebuilt without applying obsolete mappings")
		return a.persistStateStrict()
	}
	// Commit under the live job context, not the expired probing time slice.
	// Keepalive uses the bounded context and arms its existing fallback first.
	if err := a.commitResolution(ctx, cfg, mappings, statuses, health, "", optimized); err != nil {
		return err
	}
	stages["commit"] = time.Since(commitStarted).Milliseconds()
	keepaliveStarted := time.Now()
	a.reconcileTransmissionTrackerKeepaliveAfterRepair(probeCtx, cfg, samples, current, mappings)
	stages["keepalive"] = time.Since(keepaliveStarted).Milliseconds()
	next := ""
	if len(work) > 0 {
		next = time.Now().Add(policy.ContinueAfter).Format(time.RFC3339Nano)
	}
	a.mu.Lock()
	a.state.AutomaticRepair = AutomaticRepairRuntime{LastSweep: lastSweep, Pending: work, NextCheck: next, Optimize: optimizing && !optimized, Stages: stages, Passed: passed, Failed: failed, Deferred: len(work)}
	a.mu.Unlock()
	if err := a.persistStateStrict(); err != nil {
		return fmt.Errorf("persist repair checkpoint: %w", err)
	}
	if len(refreshTargets) > 0 {
		maxStreak := 0
		for _, host := range refreshTargets {
			if health[host].FailureStreak > maxStreak {
				maxStreak = health[host].FailureStreak
			}
		}
		a.mu.RLock()
		lastRefresh := a.state.LastRefresh
		a.mu.RUnlock()
		bootstrap := len(freshCandidates(cached, time.Duration(cfg.Repair.CandidateTTLMinutes)*time.Minute, started)) == 0 && len(current) == 0
		refresh, _, _ := refreshDecision(time.Now(), lastRefresh, maxStreak, cfg.Repair.FailureThreshold, time.Duration(cfg.Repair.RefreshCooldownMinutes)*time.Minute, time.Duration(cfg.Repair.RefreshMaxBackoffMinutes)*time.Minute, bootstrap)
		if refresh {
			if err := a.requestCandidateRefresh("unresolved domains", refreshTargets, false); err != nil {
				return err
			}
		}
	}
	a.appendLog("repair slice: passed=%d failed=%d unfinished=%d skipped=%d; samples=%dms verification=%dms commit=%dms keepalive=%dms total=%dms", passed, failed, len(work), len(cfg.Domains)-len(tasks), stages["samples"], stages["verification"], stages["commit"], stages["keepalive"], time.Since(started).Milliseconds())
	a.completeJobProgress("完成", fmt.Sprintf("通过 %d · 失败 %d · 待续 %d", passed, failed, len(work)))
	return nil
}

func automaticConfigKey(cfg Config) string {
	b, _ := json.Marshal(cfg)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
