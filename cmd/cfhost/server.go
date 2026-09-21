package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
)

//go:embed index.html
var indexHTML []byte

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/run", a.handleJob("run"))
	mux.HandleFunc("/api/repair", a.handleJob("repair"))
	mux.HandleFunc("/api/apply", a.handleApply)
	mux.HandleFunc("/api/sync", a.handleSync)
	return mux
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
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

func (a *App) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := a.snapshotConfig()
	a.mu.RLock()
	mappings := copyMappings(a.state.Mappings)
	a.mu.RUnlock()
	if err := a.applyMappings(cfg.HostsPath, mappings); err != nil {
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

func writeResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
