package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/openwiki/portal/internal/db"
)

// handleDomains routes GET/POST/PUT/DELETE for /portal/domains (business domain dictionary).
func (s *Server) handleDomains(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listDomains(w, r)
	case http.MethodPost:
		s.createDomain(w, r)
	case http.MethodPut:
		s.updateDomain(w, r)
	case http.MethodDelete:
		s.deleteDomain(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	user := s.getSessionUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Non-admins only see active domains (for populating selects).
	// super_admin/admin see all (for management UI).
	var list []db.BusinessDomain
	var err error
	if user.Role == "super_admin" || user.Role == "admin" {
		list, err = db.ListAllBusinessDomains()
	} else {
		list, err = db.ListBusinessDomains()
	}
	if err != nil {
		http.Error(w, "Failed to list domains: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []db.BusinessDomain{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

type domainRequest struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
	Status      string `json:"status"`
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	user := s.getSessionUser(r)
	if user == nil || (user.Role != "super_admin" && user.Role != "admin") {
		http.Error(w, "Forbidden: admin only", http.StatusForbidden)
		return
	}
	var req domainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	d := &db.BusinessDomain{
		Name:        req.Name,
		Description: req.Description,
		SortOrder:   req.SortOrder,
		Status:      req.Status,
	}
	if err := db.CreateBusinessDomain(d); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "Domain name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "Failed to create domain: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.RecordAudit(r, "domain.create", req.Name, req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(d)
}

func (s *Server) updateDomain(w http.ResponseWriter, r *http.Request) {
	user := s.getSessionUser(r)
	if user == nil || (user.Role != "super_admin" && user.Role != "admin") {
		http.Error(w, "Forbidden: admin only", http.StatusForbidden)
		return
	}
	var req domainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == 0 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	d := &db.BusinessDomain{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		SortOrder:   req.SortOrder,
		Status:      req.Status,
	}
	if err := db.UpdateBusinessDomain(d); err != nil {
		http.Error(w, "Failed to update domain: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.RecordAudit(r, "domain.update", req.Name, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d)
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	user := s.getSessionUser(r)
	if user == nil || user.Role != "super_admin" {
		http.Error(w, "Forbidden: super_admin only", http.StatusForbidden)
		return
	}
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "valid id query parameter is required", http.StatusBadRequest)
		return
	}
	if err := db.DeleteBusinessDomain(id); err != nil {
		http.Error(w, "Failed to delete domain: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.RecordAudit(r, "domain.delete", idStr, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Domain deleted", "id": idStr})
}
