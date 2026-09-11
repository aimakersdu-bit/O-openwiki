package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openwiki/portal/internal/auth"
)

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Option 1: Return current user session details
	// Option 2: Query orchestrator QA history if requested
	repoID := r.URL.Query().Get("repo_id")
	if repoID != "" {
		// Forward query to orchestrator QA logs if needed
		url := fmt.Sprintf("%s/api/qa/history?user_id=%s&repo_id=%s", s.config.OrchestratorURL, session.UserID, repoID)
		resp, err := s.httpClient.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			io.Copy(w, resp.Body)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"session": session,
	})
}

// handleQASessions handles GET and DELETE for /portal/qa/sessions
func (s *Server) handleQASessions(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method == http.MethodGet {
		repoID := r.URL.Query().Get("repo_id")
		if repoID == "" {
			http.Error(w, "repo_id required", http.StatusBadRequest)
			return
		}
		url := fmt.Sprintf("%s/api/qa/sessions?user_id=%s&repo_id=%s", s.config.OrchestratorURL, session.UserID, repoID)
		resp, err := s.httpClient.Get(url)
		if err != nil {
			http.Error(w, "Failed to connect to orchestrator: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	if r.Method == http.MethodDelete {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "session_id required", http.StatusBadRequest)
			return
		}
		url := fmt.Sprintf("%s/api/qa/sessions?session_id=%s", s.config.OrchestratorURL, sessionID)
		req, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			http.Error(w, "Failed to create delete request", http.StatusInternalServerError)
			return
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			http.Error(w, "Failed to connect to orchestrator: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleQAMessages handles GET /portal/qa/messages?session_id=...
func (s *Server) handleQAMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	url := fmt.Sprintf("%s/api/qa/messages?session_id=%s&user_id=%s", s.config.OrchestratorURL, sessionID, session.UserID)
	resp, err := s.httpClient.Get(url)
	if err != nil {
		http.Error(w, "Failed to connect to orchestrator: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

