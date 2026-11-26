package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type App struct {
	mu        sync.RWMutex
	persistMu sync.Mutex
	configMu  sync.Mutex
	cfstMu    sync.Mutex
	config    Config
	state     RuntimeState
	dataDir   string
	cfstBin   string
}

type DomainHealth struct {
	FailureStreak int    `json:"failureStreak"`
	LastSuccess   string `json:"lastSuccess"`
	LastFailure   string `json:"lastFailure"`
	NextCheck string `json:"nextCheck,omitempty"`
	FailureClass string `json:"failureClass,omitempty"`
}

type HTTPProbeRuntime struct {
	Available       bool   `json:"available"`
	Reachable       bool   `json:"reachable"`
	StatusCode      int    `json:"statusCode,omitempty"`
	Detail          string `json:"detail,omitempty"`
	CheckedAt       string `json:"checkedAt,omitempty"`
	LastSuccessCode int    `json:"lastSuccessCode,omitempty"`
	LastSuccessAt   string `json:"lastSuccessAt,omitempty"`
	LastFailureCode int    `json:"lastFailureCode,omitempty"`
	LastFailureAt   string `json:"lastFailureAt,omitempty"`
	LastFailureDetail string `json:"lastFailureDetail,omitempty"`
}

type CFSTRateLimitRuntime struct {
	Origin string `json:"origin,omitempty"`
	Consecutive int `json:"consecutive,omitempty"`
	Active            bool   `json:"active"`
	Mode              string `json:"mode"`
	StatusCode        int    `json:"statusCode,omitempty"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
	Until             string `json:"until,omitempty"`
	DetectedAt        string `json:"detectedAt,omitempty"`
	LastEvent         string `json:"lastEvent,omitempty"`
}

type TrackerKeepaliveRuntime struct {
	Attempts           int     `json:"attempts"`
	StartedAt          string  `json:"startedAt,omitempty"`
	LastReannounce     string  `json:"lastReannounce,omitempty"`
	NextCheck          string  `json:"nextCheck,omitempty"`
	NextReannounce     string  `json:"nextReannounce,omitempty"`
	SampleTestAt       string  `json:"sampleTestAt,omitempty"`
	MappingIP          string  `json:"mappingIP,omitempty"`
	MappingChangedAt   string  `json:"mappingChangedAt,omitempty"`
	RejectedIP         string  `json:"rejectedIP,omitempty"`
	RejectedAt         string  `json:"rejectedAt,omitempty"`
	FastRetry          bool    `json:"fastRetry,omitempty"`
	Status             string  `json:"status,omitempty"`
	MatchedTorrents    int     `json:"matchedTorrents,omitempty"`
	EvaluatedTorrents  int     `json:"evaluatedTorrents,omitempty"`
	ConnectedTorrents  int     `json:"connectedTorrents,omitempty"`
	ConnectionFailures int     `json:"connectionFailures,omitempty"`
	ConnectedPercent   float64 `json:"connectedPercent,omitempty"`
	LastEvent          string  `json:"lastEvent,omitempty"`
}

type RuntimeState struct {
	AutomaticRepair AutomaticRepairRuntime `json:"automaticRepair"`
	CandidateRefresh CandidateRefreshRuntime `json:"candidateRefresh"`
	Running             bool                    `json:"running"`
	CurrentJob          string                  `json:"currentJob"`
	CurrentDomain       string                  `json:"currentDomain,omitempty"`
	Progress            JobProgress             `json:"progress"`
	LastRun             string                  `json:"lastRun"`
	LastSuccess         string                  `json:"lastSuccess"`
	LastError           string                  `json:"lastError"`
	LastRefresh         string                  `json:"lastRefresh"`
	NextRefresh         string                  `json:"nextRefresh"`
	LastOptimize        string                  `json:"lastOptimize"`
	LastOptimizeAttempt string                  `json:"lastOptimizeAttempt"`
	HostsBackup *HostsRecoveryBackup `json:"hostsBackup,omitempty"`
	HostsMaintenance    HostsMaintenanceRuntime `json:"hostsMaintenance"`
	ResolutionAttempt   ResolutionAttempt       `json:"resolutionAttempt"`
	HostsClearedAt      string                  `json:"hostsClearedAt,omitempty"`
	MigrationAt         string                  `json:"migrationAt"`
	MigrationStatus     string                  `json:"migrationStatus"`
	Candidates          []Candidate             `json:"candidates"`
	Mappings            map[string]string       `json:"mappings"`
	DomainStatus        map[string]string       `json:"domainStatus"`
	DomainHealth        map[string]DomainHealth `json:"domainHealth"`
	HTTPProbe           map[string]HTTPProbeRuntime `json:"httpProbe"`
	TrackerSamples      map[string]TrackerSampleRuntime    `json:"trackerSamples"`
	TrackerKeepalive    map[string]TrackerKeepaliveRuntime `json:"trackerKeepalive"`
	CandidateSweeps map[string]CandidateSweep `json:"candidateSweeps,omitempty"`
	CFSTSources map[string]CFSTSourceRuntime `json:"cfstSources,omitempty"`
	CFSTSchedule CFSTScheduleRuntime `json:"cfstSchedule"`
	CFSTRateLimit       CFSTRateLimitRuntime               `json:"cfstRateLimit"`
	Sync                SyncRuntimeState        `json:"sync"`
	History             []RunRecord             `json:"history"`
	Logs                []string                `json:"logs"`
}

func main() {
	dataDir := getenv("DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0755); err != nil { log.Fatal(err) }
	cfg, err := loadConfig(dataDir)
	if err != nil { log.Fatal(err) }
	app := &App{
		config: cfg, dataDir:dataDir, cfstBin:getenv("CFST_BIN","cfst"),
		state: RuntimeState{Mappings:map[string]string{}, DomainStatus:map[string]string{}, DomainHealth:map[string]DomainHealth{}, HTTPProbe:map[string]HTTPProbeRuntime{}, TrackerSamples:map[string]TrackerSampleRuntime{}, TrackerKeepalive:map[string]TrackerKeepaliveRuntime{}},
	}
	app.loadState()
	app.refreshTrackerSampleInventory(cfg)
	if err := app.migrateLegacy(cfg); err != nil { app.appendLog("legacy migration blocked: %v", err) }
	go app.scheduler()

	server := &http.Server{Addr:cfg.Listen, Handler:app.routes()}
	errCh := make(chan error,1)
	go func(){ log.Printf("CFHost listening on %s",cfg.Listen); errCh <- server.ListenAndServe() }()
	sigCh := make(chan os.Signal,1)
	signal.Notify(sigCh,syscall.SIGINT,syscall.SIGTERM)
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err,http.ErrServerClosed) { log.Fatal(err) }
	case sig := <-sigCh:
		log.Printf("CFHost stopping on %s",sig)
		ctx,cancel := context.WithTimeout(context.Background(),10*time.Second); defer cancel()
		if err := server.Shutdown(ctx); err != nil { log.Printf("shutdown: %v",err) }
		app.persistState()
	}
}

func (a *App) snapshotConfig() Config { a.mu.RLock(); defer a.mu.RUnlock(); return a.config }

func (a *App) appendLog(format string,args ...any) {
	line:=logLine(format,args...)
	a.mu.Lock(); defer a.mu.Unlock()
	a.state.Logs=append(a.state.Logs,line)
	if len(a.state.Logs)>300 { a.state.Logs=a.state.Logs[len(a.state.Logs)-300:] }
}

func (a *App) loadState() {
	b,err:=os.ReadFile(filepath.Join(a.dataDir,"state.json")); if err!=nil{return}
	var s RuntimeState; if decodeJSON(b,&s)!=nil{return}
	s.Running=false; s.CurrentJob=""; s.CurrentDomain=""; s.Progress=JobProgress{}
	s.CandidateRefresh.Running = false
	if s.Mappings==nil{s.Mappings=map[string]string{}}
	if s.DomainStatus==nil{s.DomainStatus=map[string]string{}}
	if s.DomainHealth==nil{s.DomainHealth=map[string]DomainHealth{}}
	if s.HTTPProbe==nil{s.HTTPProbe=map[string]HTTPProbeRuntime{}}
	if s.TrackerSamples==nil{s.TrackerSamples=map[string]TrackerSampleRuntime{}}
	if s.TrackerKeepalive==nil{s.TrackerKeepalive=map[string]TrackerKeepaliveRuntime{}}
	if s.CFSTRateLimit.Mode=="" { s.CFSTRateLimit.Mode="normal" }
	if len(s.History)>200{s.History=s.History[len(s.History)-200:]}
	a.state=s
}

func (a *App) persistState() {
	if err:=a.persistStateStrict(); err!=nil { a.appendLog("state persist failed: %v",err) }
}

func optimizeDue(now time.Time,lastOptimize,lastAttempt,lastRefresh string,cfg OptimizeConfig) bool {
	if !cfg.ScheduledFull { return false }
	if lastAttemptTime,err:=time.Parse(time.RFC3339,lastAttempt); err==nil && !lastAttemptTime.IsZero() && now.Sub(lastAttemptTime)<time.Duration(cfg.RetryMinutes)*time.Minute {
		return false
	}
	base,err:=time.Parse(time.RFC3339,lastOptimize)
	if err!=nil || base.IsZero() { base,_=time.Parse(time.RFC3339,lastRefresh) }
	if base.IsZero() { return true }
	return now.Sub(base)>=time.Duration(cfg.IntervalMinutes)*time.Minute
}

func trackerKeepaliveCheckDue(now time.Time, states map[string]TrackerKeepaliveRuntime) bool {
	for _, state := range states {
		if strings.TrimSpace(state.NextCheck) == "" {
			continue
		}
		next, err := time.Parse(time.RFC3339, state.NextCheck)
		if err == nil && !now.Before(next) {
			return true
		}
	}
	return false
}

func (a *App) scheduler() {
	a.recoverHosts()
	ticker:=time.NewTicker(30*time.Second); defer ticker.Stop()
	for now:=range ticker.C {
		a.recoverHosts()
		a.startCandidateRefresh(now)
		kind := a.scheduledJob(now)
		if kind == "reserve" {
			if err:=a.requestCandidateRefresh("candidate reserve low", nil, false);err!=nil {a.appendLog("candidate refresh queue failed: %v",err)} else {a.startCandidateRefresh(now)}
		} else if kind != "" { a.startJob(kind) }
	}
}

func (a *App) scheduledJob(now time.Time) string {
	a.mu.RLock()
	running := a.state.Running
	a.mu.RUnlock()
	if running { return "" }
	cfg := a.snapshotConfig()
	// A broken Hosts target cannot be repaired by probing more sites. Check
	// locally with backoff, without consuming a job or network budget.
	if err := a.preflightMaintenanceHosts(cfg, now, false); err != nil { return "" }
	a.mu.RLock()
	defer a.mu.RUnlock()
	return nextScheduledJob(now, cfg, a.state)
}

func (a *App) startJob(kind string) bool {
	return a.startJobWithOrigin(kind, false)
}

func (a *App) startManualJob(kind string) bool {
	return a.startJobWithOrigin(kind, true)
}

func (a *App) startJobWithOrigin(kind string, manual bool) bool {
	a.mu.Lock()
	if a.state.Running { a.mu.Unlock(); return false }
	a.state.Running=true; a.state.CurrentJob=kind; a.state.CurrentDomain=""; a.state.Progress=initialJobProgress(kind); a.state.LastError=""
	if !manual&&kind=="repair" {a.state.AutomaticRepair.Stages=nil}
	mappingsBefore:=len(a.state.Mappings); refreshBefore:=a.state.LastRefresh
	a.mu.Unlock()

	go func(){
		started:=time.Now(); cfg:=a.snapshotConfig()
		// Manual work retains its full configured verification/CFST limits.
		// Automatic resolution checkpoints bounded slices independently of CFST.
		ctx:=context.Background()
		if manual { ctx = withManualOperation(ctx) }
		a.appendLog("%s started",kind)
		var err error
		if !manual && kind == "repair" {
			err = a.runAutomaticRepair(ctx, cfg)
		} else if !manual && kind == "optimize" {
			err = a.requestAutomaticOptimize(cfg)
		} else {
			err = a.runJob(ctx,kind,cfg)
		}
		var deferred *cfstDeferredError
		errors.As(err, &deferred)
		finished:=time.Now(); now:=finished.Format(time.RFC3339)
		a.mu.Lock()
		queuedOptimize:=!manual&&kind=="optimize"&&err==nil
		if manual&&err==nil&&(kind=="repair"||kind=="optimize"){a.state.AutomaticRepair.LastSweep=finished.Format(time.RFC3339Nano)}
		if (deferred==nil || kind=="repair")&&!queuedOptimize { a.state.LastRun=now }
		if deferred!=nil||queuedOptimize { a.state.LastError="" } else if err!=nil { a.state.LastError=err.Error() } else { a.state.LastSuccess=now; a.state.LastError="" }
		record:=RunRecord{Kind:kind,StartedAt:started.Format(time.RFC3339),FinishedAt:now,DurationMS:finished.Sub(started).Milliseconds(),Success:err==nil,MappingsBefore:mappingsBefore,MappingsAfter:len(a.state.Mappings),CandidateCount:len(a.state.Candidates),FullRefresh:a.state.LastRefresh!=refreshBefore}
		if !manual && kind == "repair" { record.Stages = a.state.AutomaticRepair.Stages; record.DeferredCount = len(a.state.AutomaticRepair.Pending) }
		if !manual && (kind=="repair"||kind=="optimize") {record.FullRefresh=false}
		if !manual && kind=="optimize" && err==nil {record.Queued=a.state.CandidateRefresh.Requested}
		if resolutionJob(kind) {
			record.UnresolvedDomains,record.UnresolvedCount=unresolvedDomainsWithStatus(cfg,a.state.Mappings,a.state.DomainStatus)
		}
		if err!=nil{record.Error=err.Error()}
		a.state.History=append(a.state.History,record); if len(a.state.History)>200{a.state.History=a.state.History[len(a.state.History)-200:]}
		a.mu.Unlock()
		if deferred!=nil { a.appendLog("%s deferred: %v",kind,err) } else if err!=nil { a.appendLog("%s failed: %v",kind,err) } else { a.appendLog("%s finished",kind) }
		a.persistState()
		// Release the job slot only after logs/state are finished. Otherwise a
		// new job (or shutdown/test cleanup) races the previous writer.
		a.mu.Lock();a.state.Running=false;a.state.CurrentJob="";a.state.CurrentDomain="";a.state.Progress=JobProgress{};a.mu.Unlock()
	}()
	return true
}
