package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/openwiki/orchestrator/internal/db"
)

// handleRepos routes GET, POST, PUT, DELETE for /api/repos
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listRepos(w, r)
	case http.MethodPost:
		s.createRepo(w, r)
	case http.MethodPut:
		s.updateRepo(w, r)
	case http.MethodDelete:
		s.deleteRepo(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// listRepos returns all registered repositories.
func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := db.ListRepos()
	if err != nil {
		http.Error(w, "Failed to query repos: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repos)
}

type CreateRepoRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GitURL    string `json:"git_url"`
	Branch    string `json:"branch"`
	LocalPath string `json:"local_path"`
	Schedule  string `json:"schedule"`
}

// createRepo registers a new repository in SQLite and schedules cron job.
func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) {
	var req CreateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.GitURL = strings.TrimSpace(req.GitURL)
	req.LocalPath = strings.TrimSpace(req.LocalPath)

	if req.ID == "" || req.GitURL == "" {
		http.Error(w, "id and git_url are required", http.StatusBadRequest)
		return
	}

	// Auto-default local_path if not provided
	if req.LocalPath == "" {
		reposBase := s.config.ReposBaseDir
		if reposBase == "" {
			reposBase = "/var/openwiki/repos"
		}
		req.LocalPath = filepath.Join(reposBase, req.ID)
	}

	if req.Branch == "" {
		req.Branch = "master"
	}
	if req.Schedule == "" {
		req.Schedule = "0 2 * * *"
	}
	if req.Name == "" {
		req.Name = req.ID
	}

	repo := &db.Repo{
		ID:        req.ID,
		Name:      req.Name,
		GitURL:    req.GitURL,
		Branch:    req.Branch,
		LocalPath: req.LocalPath,
		Schedule:  req.Schedule,
		Status:    "active",
	}

	if err := db.SaveRepo(repo); err != nil {
		http.Error(w, "Failed to save repo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Register job in cron scheduler
	if s.scheduler != nil {
		if err := s.scheduler.AddRepoJob(repo); err != nil {
			logPrintf("Warning: failed to add repo to scheduler: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(repo)
}

type UpdateRepoRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GitURL    string `json:"git_url"`
	Branch    string `json:"branch"`
	LocalPath string `json:"local_path"`
	WikiDir   string `json:"wiki_dir"`
	StaticDir string `json:"static_dir"`
	Schedule  string `json:"schedule"`
	Status    string `json:"status"`
}

func (s *Server) updateRepo(w http.ResponseWriter, r *http.Request) {
	var req UpdateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	existing, err := db.GetRepo(req.ID)
	if err != nil || existing == nil {
		http.Error(w, "Repo not found", http.StatusNotFound)
		return
	}

	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.GitURL != "" {
		existing.GitURL = req.GitURL
	}
	if req.Branch != "" {
		existing.Branch = req.Branch
	}
	if req.LocalPath != "" {
		existing.LocalPath = req.LocalPath
	}
	if req.WikiDir != "" {
		existing.WikiDir = req.WikiDir
	}
	if req.StaticDir != "" {
		existing.StaticDir = req.StaticDir
	}
	if req.Schedule != "" {
		existing.Schedule = req.Schedule
	}
	if req.Status != "" {
		existing.Status = req.Status
	}

	if err := db.UpdateRepo(existing); err != nil {
		http.Error(w, "Failed to update repo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if s.scheduler != nil {
		_ = s.scheduler.UpdateRepoJob(existing)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(existing)
}

func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id query parameter is required", http.StatusBadRequest)
		return
	}

	if err := db.DeleteRepo(id); err != nil {
		http.Error(w, "Failed to delete repo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if s.scheduler != nil {
		s.scheduler.RemoveRepoJob(id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Repo deleted successfully",
		"id":      id,
	})
}
