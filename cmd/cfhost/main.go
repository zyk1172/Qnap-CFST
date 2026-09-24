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
	mu      sync.RWMutex
	config  Config
	state   RuntimeState
	dataDir string
	cfstBin string
}

type DomainHealth struct {
	FailureStreak int    `json:"failureStreak"`
	LastSuccess   string `json:"lastSuccess"`
	LastFailure   string `json:"lastFailure"`
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
	MigrationAt         string                  `json:"migrationAt"`
	MigrationStatus     string                  `json:"migrationStatus"`
	Candidates          []Candidate             `json:"candidates"`
	Mappings            map[string]string       `json:"mappings"`
	DomainStatus        map[string]string       `json:"domainStatus"`
	DomainHealth        map[string]DomainHealth `json:"domainHealth"`
	HTTPProbe           map[string]HTTPProbeRuntime `json:"httpProbe"`
	TrackerSamples      map[string]TrackerSampleRuntime    `json:"trackerSamples"`
	TrackerKeepalive    map[string]TrackerKeepaliveRuntime `json:"trackerKeepalive"`
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
	ticker:=time.NewTicker(30*time.Second); defer ticker.Stop()
	for now:=range ticker.C {
		cfg:=a.snapshotConfig()
		a.mu.RLock()
		running:=a.state.Running; lastRun:=a.state.LastRun; lastOptimize:=a.state.LastOptimize; lastAttempt:=a.state.LastOptimizeAttempt; lastRefresh:=a.state.LastRefresh
		keepalive:=copyTrackerKeepalive(a.state.TrackerKeepalive)
		a.mu.RUnlock()
		if running { continue }
		if trackerKeepaliveCheckDue(now,keepalive) { a.startJob("repair"); continue }
		if optimizeDue(now,lastOptimize,lastAttempt,lastRefresh,cfg.Optimize) { a.startJob("optimize"); continue }
		if !cfg.AutoRepair { continue }
		lastTime,_:=time.Parse(time.RFC3339,lastRun)
		if !lastTime.IsZero() && time.Since(lastTime)<time.Duration(cfg.RepairIntervalMinutes)*time.Minute { continue }
		a.startJob("repair")
	}
}

func (a *App) startJob(kind string) bool {
	a.mu.Lock()
	if a.state.Running { a.mu.Unlock(); return false }
	a.state.Running=true; a.state.CurrentJob=kind; a.state.CurrentDomain=""; a.state.Progress=initialJobProgress(kind); a.state.LastError=""
	mappingsBefore:=len(a.state.Mappings); refreshBefore:=a.state.LastRefresh
	a.mu.Unlock()

	go func(){
		started:=time.Now(); cfg:=a.snapshotConfig()
		ctx,cancel:=context.WithTimeout(context.Background(),time.Duration(cfg.CFST.RunTimeoutMinutes)*time.Minute); defer cancel()
		a.appendLog("%s started",kind)
		err:=a.runJob(ctx,kind,cfg)
		finished:=time.Now(); now:=finished.Format(time.RFC3339)
		a.mu.Lock()
		a.state.Running=false; a.state.CurrentJob=""; a.state.CurrentDomain=""; a.state.Progress=JobProgress{}; a.state.LastRun=now
		if err!=nil { a.state.LastError=err.Error() } else { a.state.LastSuccess=now; a.state.LastError="" }
		record:=RunRecord{Kind:kind,StartedAt:started.Format(time.RFC3339),FinishedAt:now,DurationMS:finished.Sub(started).Milliseconds(),Success:err==nil,MappingsBefore:mappingsBefore,MappingsAfter:len(a.state.Mappings),CandidateCount:len(a.state.Candidates),FullRefresh:a.state.LastRefresh!=refreshBefore}
		if resolutionJob(kind) {
			record.UnresolvedDomains,record.UnresolvedCount=unresolvedDomains(cfg,a.state.Mappings)
		}
		if err!=nil{record.Error=err.Error()}
		a.state.History=append(a.state.History,record); if len(a.state.History)>200{a.state.History=a.state.History[len(a.state.History)-200:]}
		a.mu.Unlock()
		if err!=nil { a.appendLog("%s failed: %v",kind,err) } else { a.appendLog("%s finished",kind) }
		a.persistState()
	}()
	return true
}
