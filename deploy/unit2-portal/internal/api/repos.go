package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openwiki/portal/internal/auth"
)

type OrchestratorRepo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GitURL    string `json:"git_url"`
	Branch    string `json:"branch"`
	LocalPath string `json:"local_path"`
	Schedule  string `json:"schedule"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	WikiURL   string `json:"wiki_url"` // Route for Nginx static files
}

func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listRepos(w, r)
	case http.MethodPost:
		if session.Role != "admin" {
			http.Error(w, `{"error":"Forbidden: Administrator privileges required"}`, http.StatusForbidden)
			return
		}
		s.proxyToOrchestrator(w, r, "POST", "/api/repos")
	case http.MethodPut:
		if session.Role != "admin" {
			http.Error(w, `{"error":"Forbidden: Administrator privileges required"}`, http.StatusForbidden)
			return
		}
		s.proxyToOrchestrator(w, r, "PUT", "/api/repos")
	case http.MethodDelete:
		if session.Role != "admin" {
			http.Error(w, `{"error":"Forbidden: Administrator privileges required"}`, http.StatusForbidden)
			return
		}
		target := "/api/repos"
		if query := r.URL.RawQuery; query != "" {
			target += "?" + query
		}
		s.proxyToOrchestrator(w, r, "DELETE", target)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBuildTrigger(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if session.Role != "admin" {
		http.Error(w, `{"error":"Forbidden: Administrator privileges required to trigger builds"}`, http.StatusForbidden)
		return
	}
	s.proxyToOrchestrator(w, r, "POST", "/api/build/trigger")
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if session.Role != "admin" {
		http.Error(w, `{"error":"Forbidden: Administrator privileges required for audit logs"}`, http.StatusForbidden)
		return
	}
	s.proxyToOrchestrator(w, r, "GET", "/api/build/status")
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	resp, err := s.httpClient.Get(s.config.OrchestratorURL + "/api/repos")
	if err != nil {
		http.Error(w, "Failed to connect to Orchestrator: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Orchestrator returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	var repos []OrchestratorRepo
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		http.Error(w, "Failed to decode orchestrator repos: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range repos {
		repos[i].WikiURL = fmt.Sprintf("/wiki/%s/", repos[i].ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repos)
}

func (s *Server) proxyToOrchestrator(w http.ResponseWriter, r *http.Request, method, path string) {
	url := s.config.OrchestratorURL + path
	req, err := http.NewRequest(method, url, r.Body)
	if err != nil {
		http.Error(w, "Failed to create proxy request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, "Failed to connect to Orchestrator: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
