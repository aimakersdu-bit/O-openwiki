package api

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/openwiki/portal/internal/auth"
)

// handlePortalBatchImport proxies batch repo importing to Orchestrator (Admin only).
func (s *Server) handlePortalBatchImport(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if session.Role != "admin" {
		http.Error(w, `{"error":"Forbidden: Administrator privileges required"}`, http.StatusForbidden)
		return
	}

	s.proxyToOrchestrator(w, r, http.MethodPost, "/api/repos/batch-import")
}

// handlePortalBatchConcurrency dynamically updates concurrency limits on Orchestrator (Admin only).
func (s *Server) handlePortalBatchConcurrency(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if session.Role != "admin" {
		http.Error(w, `{"error":"Forbidden: Administrator privileges required"}`, http.StatusForbidden)
		return
	}

	s.proxyToOrchestrator(w, r, http.MethodPut, "/api/batch/concurrency")
}

// handlePortalBatchStatus returns active slots, progress, and dynamic ETA estimation.
func (s *Server) handlePortalBatchStatus(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	target := "/api/batch/status"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	s.proxyToOrchestrator(w, r, http.MethodGet, target)
}

// handlePortalBatchControl proxies pause, resume, and cancel commands (Admin only).
func (s *Server) handlePortalBatchControl(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if session.Role != "admin" {
		http.Error(w, `{"error":"Forbidden: Administrator privileges required"}`, http.StatusForbidden)
		return
	}

	action := filepath.Base(r.URL.Path)
	target := fmt.Sprintf("/api/batch/%s", action)
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	s.proxyToOrchestrator(w, r, http.MethodPost, target)
}

// handlePortalDailyTokens returns daily token telemetry.
func (s *Server) handlePortalDailyTokens(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	target := "/api/telemetry/tokens/daily"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	s.proxyToOrchestrator(w, r, http.MethodGet, target)
}

// handlePortalTokensExport proxies TSV/CSV token export with full content-type preservation.
func (s *Server) handlePortalTokensExport(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	target := "/api/telemetry/tokens/export"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}

	url := s.config.OrchestratorURL + target
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "Failed to create proxy request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, "Failed to connect to Orchestrator: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
