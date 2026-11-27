package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var errJobBusy = errors.New("another job is running")

// Remove only CFHost's own marker blocks. In particular, do not normalize line
// endings, whitespace, trailing newlines, or legacy/other programs' blocks.
func clearCFHostBlocks(content string) (string, int, error) {
	var out strings.Builder
	inside := false
	removed := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		switch strings.TrimSpace(line) {
		case hostsBegin:
			if inside {
				return "", 0, errors.New("nested CFHost marker; refusing clear")
			}
			inside = true
		case hostsEnd:
			if !inside {
				return "", 0, errors.New("unmatched CFHost end marker; refusing clear")
			}
			inside = false
		default:
			if !inside {
				out.WriteString(line)
			} else if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
				removed++
			}
		}
	}
	if inside {
		return "", 0, errors.New("missing CFHost end marker; refusing clear")
	}
	return out.String(), removed, nil
}

func (a *App) beginHostsOperation(kind string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Running {
		return false
	}
	a.state.Running = true
	a.state.CurrentJob = kind
	a.state.Progress = initialJobProgress(kind)
	return true
}

func (a *App) endHostsOperation() {
	a.mu.Lock()
	a.state.Running = false
	a.state.CurrentJob = ""
	a.state.Progress = JobProgress{}
	a.mu.Unlock()
	a.persistState()
}

func (a *App) clearManagedHosts() (removed int, err error) {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	if !a.beginHostsOperation("clear") {
		return 0, errJobBusy
	}
	defer a.endHostsOperation()
	original := a.snapshotConfig()
	disabled := original
	disabled.AutoApply = false
	// Persist the write-off switch first, so a process crash cannot cause the
	// next startup to immediately repopulate the file that the user cleared.
	if err := saveConfig(a.dataDir, disabled); err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.config = disabled
	oldCleared := a.state.HostsClearedAt
	oldBackup := a.state.HostsBackup
	a.mu.Unlock()
	restoreConfig := func(cause error) error {
		if restoreErr := saveConfig(a.dataDir, original); restoreErr != nil {
			return fmt.Errorf("%v; automatic apply remains off because config rollback failed: %w", cause, restoreErr)
		}
		a.mu.Lock()
		a.config = original
		a.mu.Unlock()
		return cause
	}
	stage, err := a.stageHostsUpdate(original, func(current string) (string, error) {
		out, n, e := clearCFHostBlocks(current)
		removed = n
		return out, e
	})
	if err != nil {
		return 0, restoreConfig(err)
	}
	a.mu.Lock()
	a.state.HostsClearedAt = time.Now().Format(time.RFC3339)
	a.state.HostsBackup = newHostsBackup(map[string]string{})
	a.mu.Unlock()
	if err := a.persistStateStrict(); err != nil {
		rollbackErr := stage.Rollback()
		a.mu.Lock()
		a.state.HostsClearedAt = oldCleared
		a.state.HostsBackup = oldBackup
		a.mu.Unlock()
		if rollbackErr != nil {
			err = fmt.Errorf("%v; Hosts rollback failed: %w", err, rollbackErr)
		}
		return 0, restoreConfig(err)
	}
	a.mirrorHostsBackup()
	a.syncContainerHosts(original, map[string]string{})
	a.appendLog("CFHost Hosts rules cleared: %d; automatic apply disabled; domain configuration and candidates retained", removed)
	return removed, nil
}

func (a *App) handleClearHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	removed, err := a.clearManagedHosts()
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errJobBusy) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeResponse(w, map[string]any{"status": "cleared", "removedRules": removed, "autoApply": false})
}
