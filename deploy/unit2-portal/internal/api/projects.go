package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/openwiki/portal/internal/auth"
)

type PortalProjectRepo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AliasName string `json:"alias_name"`
	Role      string `json:"role"`
}

type PortalProject struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	LocalPath   string              `json:"local_path"`
	Status      string              `json:"status"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
	WikiURL     string              `json:"wiki_url"`
	Repos       []PortalProjectRepo `json:"repos"`
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listProjects(w, r)
	case http.MethodPost:
		if !canManageProjects(session.Role) {
			http.Error(w, `{"error":"Forbidden: Group Leader or Administrator privileges required"}`, http.StatusForbidden)
			return
		}
		s.RecordAudit(r, "project.create", "", nil)
		s.proxyToOrchestrator(w, r, "POST", "/api/projects")
	case http.MethodPut:
		if !canManageProjects(session.Role) {
			http.Error(w, `{"error":"Forbidden: Group Leader or Administrator privileges required"}`, http.StatusForbidden)
			return
		}
		s.RecordAudit(r, "project.update", "", nil)
		s.proxyToOrchestrator(w, r, "PUT", "/api/projects")
	case http.MethodDelete:
		if !canManageProjects(session.Role) {
			http.Error(w, `{"error":"Forbidden: Group Leader or Administrator privileges required"}`, http.StatusForbidden)
			return
		}
		target := "/api/projects"
		if query := r.URL.RawQuery; query != "" {
			target += "?" + query
		}
		s.RecordAudit(r, "project.delete", r.URL.Query().Get("id"), nil)
		s.proxyToOrchestrator(w, r, "DELETE", target)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjectBuildTrigger(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !canManageProjects(session.Role) {
		http.Error(w, `{"error":"Forbidden: Group Leader or Administrator privileges required to trigger builds"}`, http.StatusForbidden)
		return
	}
	s.RecordAudit(r, "project.build_trigger", "", nil)
	s.proxyToOrchestrator(w, r, "POST", "/api/projects/build")
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequest(http.MethodGet, s.config.OrchestratorURL+"/api/projects", nil)
	if err != nil {
		http.Error(w, "Failed to create request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	user := s.getSessionUser(r)
	if user != nil {
		secret := s.config.HMACSecret
		if secret == "" {
			secret = "openwiki-hmac-shared-secret"
		}
		SignRequest(req, secret, user.UserID, user.Role, user.AuthorizedDomains)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, "Failed to connect to Orchestrator: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Orchestrator returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	var projects []PortalProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		http.Error(w, "Failed to decode orchestrator projects: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range projects {
		projects[i].WikiURL = fmt.Sprintf("/wiki/projects/%s/", projects[i].ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(projects)
}
