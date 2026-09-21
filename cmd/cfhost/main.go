package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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

type RuntimeState struct {
	Running       bool                    `json:"running"`
	CurrentJob    string                  `json:"currentJob"`
	LastRun       string                  `json:"lastRun"`
	LastSuccess   string                  `json:"lastSuccess"`
	LastError     string                  `json:"lastError"`
	LastRefresh   string                  `json:"lastRefresh"`
	NextRefresh   string                  `json:"nextRefresh"`
	Candidates    []Candidate             `json:"candidates"`
	Mappings      map[string]string       `json:"mappings"`
	DomainStatus  map[string]string       `json:"domainStatus"`
	DomainHealth  map[string]DomainHealth `json:"domainHealth"`
	Logs          []string                `json:"logs"`
}

func main() {
	dataDir := getenv("DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	cfg, err := loadConfig(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	app := &App{
		config:  cfg,
		dataDir: dataDir,
		cfstBin: getenv("CFST_BIN", "cfst"),
		state: RuntimeState{
			Mappings:     map[string]string{},
			DomainStatus: map[string]string{},
			DomainHealth: map[string]DomainHealth{},
		},
	}
	app.loadState()
	go app.scheduler()

	log.Printf("CFHost listening on %s", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, app.routes()); err != nil {
		log.Fatal(err)
	}
}

func (a *App) snapshotConfig() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config
}

func (a *App) appendLog(format string, args ...any) {
	line := logLine(format, args...)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Logs = append(a.state.Logs, line)
	if len(a.state.Logs) > 300 {
		a.state.Logs = a.state.Logs[len(a.state.Logs)-300:]
	}
}

func (a *App) loadState() {
	path := filepath.Join(a.dataDir, "state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var s RuntimeState
	if decodeJSON(b, &s) != nil {
		return
	}
	s.Running = false
	s.CurrentJob = ""
	if s.Mappings == nil {
		s.Mappings = map[string]string{}
	}
	if s.DomainStatus == nil {
		s.DomainStatus = map[string]string{}
	}
	if s.DomainHealth == nil {
		s.DomainHealth = map[string]DomainHealth{}
	}
	a.state = s
}

func (a *App) persistState() {
	a.mu.RLock()
	s := a.state
	s.Running = false
	s.CurrentJob = ""
	a.mu.RUnlock()
	_ = writeJSON(filepath.Join(a.dataDir, "state.json"), s)
}

func (a *App) scheduler() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cfg := a.snapshotConfig()
		if !cfg.AutoRepair {
			continue
		}
		a.mu.RLock()
		running := a.state.Running
		last := a.state.LastRun
		a.mu.RUnlock()
		if running {
			continue
		}
		lastTime, _ := time.Parse(time.RFC3339, last)
		if !lastTime.IsZero() && time.Since(lastTime) < time.Duration(cfg.RepairIntervalMinutes)*time.Minute {
			continue
		}
		a.startJob("repair")
	}
}

func (a *App) startJob(kind string) bool {
	a.mu.Lock()
	if a.state.Running {
		a.mu.Unlock()
		return false
	}
	a.state.Running = true
	a.state.CurrentJob = kind
	a.state.LastError = ""
	a.mu.Unlock()

	go func() {
		cfg := a.snapshotConfig()
		timeout := time.Duration(cfg.CFST.RunTimeoutMinutes) * time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		a.appendLog("%s started", kind)
		err := a.runJob(ctx, kind, cfg)
		now := time.Now().Format(time.RFC3339)

		a.mu.Lock()
		a.state.Running = false
		a.state.CurrentJob = ""
		a.state.LastRun = now
		if err != nil {
			a.state.LastError = err.Error()
		} else {
			a.state.LastSuccess = now
			a.state.LastError = ""
		}
		a.mu.Unlock()

		if err != nil {
			a.appendLog("%s failed: %v", kind, err)
		} else {
			a.appendLog("%s finished", kind)
		}
		a.persistState()
	}()
	return true
}
