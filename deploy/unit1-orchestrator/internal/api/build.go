package api

import (
	"encoding/json"
	"net/http"

	"github.com/openwiki/orchestrator/internal/db"
)

type TriggerBuildRequest struct {
	RepoID string `json:"repo_id"`
}

// handleBuildStatus returns latest build records for a repository.
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

	builds, err := db.GetLatestBuilds(repoID, 10)
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

	go s.scheduler.TriggerBuild(req.RepoID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Build triggered successfully",
		"repo_id": req.RepoID,
	})
}
