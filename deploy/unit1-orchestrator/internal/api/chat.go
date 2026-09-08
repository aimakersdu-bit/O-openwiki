package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openwiki/orchestrator/internal/db"
)

type ChatRequest struct {
	RepoID   string `json:"repo_id"`
	UserID   string `json:"user_id"`
	Question string `json:"question"`
}

// handleChat handles SSE streaming for OpenWiki QA questions.
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

	if strings.TrimSpace(req.RepoID) == "" || strings.TrimSpace(req.Question) == "" {
		http.Error(w, "repo_id and question are required", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		req.UserID = "anonymous"
	}

	repo, err := db.GetRepo(req.RepoID)
	if err != nil {
		http.Error(w, "Repo error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if repo == nil {
		http.Error(w, "Repo not found", http.StatusNotFound)
		return
	}

	// Prepare SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Execute chat via QA process pool
	if err := s.qaPool.StreamChat(r.Context(), repo, req.UserID, req.Question, w); err != nil {
		// If headers were not flushed yet or error occurred
		logPrintf("Chat streaming error for repo %s: %v", req.RepoID, err)
	}
}

// handleQAHistory returns historical Q&A sessions for a given user and repo.
func (s *Server) handleQAHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repoID := r.URL.Query().Get("repo_id")
	userID := r.URL.Query().Get("user_id")

	if repoID == "" || userID == "" {
		http.Error(w, "repo_id and user_id are required", http.StatusBadRequest)
		return
	}

	sessions, err := db.ListQASessions(repoID, userID, 50)
	if err != nil {
		http.Error(w, "Failed to query QA sessions: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}
