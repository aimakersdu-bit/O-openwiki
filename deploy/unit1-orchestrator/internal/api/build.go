package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

type TriggerBuildRequest struct {
	RepoID string `json:"repo_id"`
	Mode   string `json:"mode,omitempty"` // "full", "knowledge" / "knowledge-only"
}

const maxAPIBuildLogBytes = 256 * 1024 // 256KB tail cap when include_log=1

// handleBuildStatus returns latest build records for a repository.
// By default it omits the heavy `log` column for fast dashboard/list loading;
// pass ?include_log=1 (e.g. from the log viewer modal) to include build logs.
func (s *Server) handleBuildStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repoID := r.URL.Query().Get("repo_id")
	if repoID == "" {
		http.Error(w, "repo_id query parameter is required", http.StatusBadRequest)
		return
	}

	limit := 10
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 {
			if n > 50 {
				n = 50
			}
			limit = n
		}
	}

	includeLog := r.URL.Query().Get("include_log") == "1" || r.URL.Query().Get("include_log") == "true"

	var (
		builds []db.Build
		err    error
	)
	if includeLog {
		builds, err = db.GetLatestBuilds(repoID, limit)
		for i := range builds {
			if len(builds[i].Log) > maxAPIBuildLogBytes {
				builds[i].Log = "... [earlier log truncated for display; full log saved on disk] ...\n" +
					builds[i].Log[len(builds[i].Log)-maxAPIBuildLogBytes:]
			}
		}
	} else {
		builds, err = db.GetLatestBuildsSummary(repoID, limit)
	}
	if err != nil {
		http.Error(w, "Failed to query build status: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(builds)
}

// handleBuildTrigger manually triggers a build pipeline for a repository.
func (s *Server) handleBuildTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TriggerBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.RepoID == "" {
		http.Error(w, "repo_id is required", http.StatusBadRequest)
		return
	}

	if s.scheduler == nil {
		http.Error(w, "Scheduler not initialized", http.StatusInternalServerError)
		return
	}

	buildMode := scheduler.BuildModeFull
	if req.Mode == string(scheduler.BuildModeKnowledgeOnly) || req.Mode == "knowledge" {
		buildMode = scheduler.BuildModeKnowledgeOnly
	}

	go s.scheduler.TriggerBuildWithMode(req.RepoID, buildMode)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Build triggered successfully",
		"repo_id": req.RepoID,
		"mode":    string(buildMode),
	})
}

