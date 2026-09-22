package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web
var webAssets embed.FS

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/run", a.handleJob("run"))
	mux.HandleFunc("/api/repair", a.handleJob("repair"))
	mux.HandleFunc("/api/optimize", a.handleJob("optimize"))
	mux.HandleFunc("/api/domain-maintain", a.handleDomainMaintain)
	mux.HandleFunc("/api/apply", a.handleApply)
	mux.HandleFunc("/api/sync", a.handleSync)
	mux.HandleFunc("/api/tracker-samples/discover", a.handleTrackerSampleDiscovery)

	webRoot, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(webRoot))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		files.ServeHTTP(w, r)
	}))
	return mux
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeResponse(w, map[string]string{"status": "ok"})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeResponse(w, a.state)
}

func (a *App) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeResponse(w, a.snapshotConfig())
	case http.MethodPut:
		var c Config
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := normalizeConfig(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := saveConfig(a.dataDir, c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.mu.Lock()
		a.config = c
		a.mu.Unlock()
		a.refreshTrackerSampleInventory(c)
		a.appendLog("configuration saved")
		writeResponse(w, c)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleJob(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !a.startJob(kind) {
			http.Error(w, "another job is running", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeResponse(w, map[string]string{"status": "started", "job": kind})
	}
}

func (a *App) handleDomainMaintain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	request.Host = strings.ToLower(strings.TrimSpace(request.Host))
	if request.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	started, err := a.startDomainMaintenance(request.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !started {
		http.Error(w, "another job is running", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeResponse(w, map[string]string{"status": "started", "job": "maintain", "host": request.Host})
}

func (a *App) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.snapshotConfig()
	a.mu.RLock()
	mappings := copyMappings(a.state.Mappings)
	a.mu.RUnlock()
	if err := a.applyMappings(cfg, mappings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response := map[string]string{"status": "applied"}
	if cfg.Sync.Enabled {
		if err := a.publishSync(r.Context(), cfg, false); err != nil {
			a.appendLog("github sync failed after manual apply: %v", err)
			response["sync"] = "failed: " + err.Error()
		} else {
			response["sync"] = "ok"
		}
	}
	a.persistState()
	writeResponse(w, response)
}

func (a *App) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.snapshotConfig()
	if err := a.publishSync(r.Context(), cfg, true); err != nil {
		a.persistState()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.persistState()
	writeResponse(w, map[string]string{"status": "published"})
}

func (a *App) handleTrackerSampleDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.snapshotConfig()
	if !cfg.Tracker.AutoDiscover {
		http.Error(w, "tracker auto-discovery is disabled", http.StatusBadRequest)
		return
	}
	cache, report := a.refreshAutoTrackerSamples(r.Context(), cfg)
	a.refreshTrackerSampleInventory(cfg)
	a.markTrackerSamplesPending(report.Domains)
	a.persistState()
	a.appendLog("tracker manual discovery: transmission=%d qbittorrent=%d cached=%d", report.Transmission, report.QBittorrent, len(cache))
	writeResponse(w, map[string]any{
		"status":  "ok",
		"cached":  len(cache),
		"report":  report,
	})
}

func writeResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
