package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openwiki/orchestrator/internal/db"
)

type ChatRequest struct {
	ProjectID string `json:"project_id,omitempty"`
	RepoID    string `json:"repo_id,omitempty"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	Question  string `json:"question"`
}

// handleChat handles SSE streaming for OpenWiki QA questions (both repo-level and project-level).
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.Question = strings.TrimSpace(req.Question)
	req.RepoID = strings.TrimSpace(req.RepoID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)

	if req.Question == "" || (req.RepoID == "" && req.ProjectID == "") {
		http.Error(w, "question and either repo_id or project_id are required", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		req.UserID = "anonymous"
	}

	// Prepare SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Branch 1: Project-level QA
	if req.ProjectID != "" {
		proj, err := db.GetProject(req.ProjectID)
		if err != nil {
			http.Error(w, "Project error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if proj == nil {
			http.Error(w, "Project not found", http.StatusNotFound)
			return
		}

		if s.qaManager != nil {
			if err := s.qaManager.StreamProjectChat(r.Context(), proj, req.UserID, req.SessionID, req.Question, w); err != nil {
				logPrintf("Chat streaming error for project %s: %v", req.ProjectID, err)
			}
		}
		return
	}

	// Branch 2: Single repo-level QA
	repo, err := db.GetRepo(req.RepoID)
	if err != nil {
		http.Error(w, "Repo error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if repo == nil {
		http.Error(w, "Repo not found", http.StatusNotFound)
		return
	}

	// Execute chat via QA Manager (or fallback to pool)
	if s.qaManager != nil {
		if err := s.qaManager.StreamChat(r.Context(), repo, req.UserID, req.SessionID, req.Question, w); err != nil {
			logPrintf("Chat streaming error for repo %s: %v", req.RepoID, err)
		}
	} else if s.qaPool != nil {
		if err := s.qaPool.StreamChat(r.Context(), repo, req.UserID, req.Question, w); err != nil {
			logPrintf("Chat streaming error for repo %s: %v", req.RepoID, err)
		}
	}

	// Asynchronously re-export graph and notify visualizer SSE clients if wiki docs were updated
	go func() {
		if s.scheduler != nil && s.scheduler.GetBuilder() != nil {
			wikiDir := repo.WikiDir
			if wikiDir == "" {
				wikiDir = repo.LocalPath + "/openwiki"
			}
			if err := s.scheduler.GetBuilder().ExportActiveGraph(repo.ID, wikiDir); err == nil {
				s.BroadcastReload()
			}
		}
	}()
}

// handleQAHistory returns historical Q&A sessions for a given user and repo or project.
func (s *Server) handleQAHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repoID := r.URL.Query().Get("repo_id")
	projectID := r.URL.Query().Get("project_id")
	userID := r.URL.Query().Get("user_id")

	if repoID == "" && projectID == "" {
		http.Error(w, "repo_id or project_id is required", http.StatusBadRequest)
		return
	}

	var sessions []db.QASession
	var err error
	if projectID != "" {
		sessions, err = db.ListProjectQASessions(projectID, userID, 50)
	} else {
		sessions, err = db.ListQASessions(repoID, userID, 50)
	}
	if err != nil {
		http.Error(w, "Failed to query QA sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []db.QASession{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// handleUserQASessions returns session summaries for a given user and repo or project.
func (s *Server) handleUserQASessions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.handleDeleteQASession(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repoID := r.URL.Query().Get("repo_id")
	projectID := r.URL.Query().Get("project_id")
	userID := r.URL.Query().Get("user_id")

	if repoID == "" && projectID == "" {
		http.Error(w, "repo_id or project_id is required", http.StatusBadRequest)
		return
	}

	var sessions []db.QASessionSummary
	var err error
	if projectID != "" {
		sessions, err = db.ListUserProjectQASessions(projectID, userID, 50)
	} else {
		sessions, err = db.ListUserQASessions(repoID, userID, 50)
	}
	if err != nil {
		http.Error(w, "Failed to query user QA sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []db.QASessionSummary{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// handleQAMessages returns chronological messages for a specific session_id.
func (s *Server) handleQAMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	userID := r.URL.Query().Get("user_id")

	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}

	messages, err := db.GetQASessionMessages(sessionID, userID)
	if err != nil {
		http.Error(w, "Failed to query QA messages: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// handleDeleteQASession deletes a specific session and its messages.
func (s *Server) handleDeleteQASession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	userID := r.URL.Query().Get("user_id")

	if sessionID == "" || userID == "" {
		http.Error(w, "session_id and user_id are required", http.StatusBadRequest)
		return
	}

	if err := db.DeleteQASession(sessionID, userID); err != nil {
		http.Error(w, "Failed to delete QA session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}
