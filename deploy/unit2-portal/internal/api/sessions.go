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
