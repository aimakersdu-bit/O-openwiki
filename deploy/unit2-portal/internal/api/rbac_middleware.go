package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/openwiki/portal/internal/auth"
	"github.com/openwiki/portal/internal/db"
)

// ComputeHMAC generates a hex-encoded SHA256 HMAC digest for inter-service calls.
func ComputeHMAC(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignRequest injects HMAC authentication headers into outgoing HTTP requests to Orchestrator.
func SignRequest(req *http.Request, secret, user, role string, domains []string) {
	req.Header.Set("X-OpenWiki-User", user)
	req.Header.Set("X-OpenWiki-Role", role)
	domainsStr := strings.Join(domains, ",")
	req.Header.Set("X-OpenWiki-Domains", domainsStr)
	payload := user + ":" + role + ":" + domainsStr
	req.Header.Set("X-OpenWiki-Sig", ComputeHMAC(secret, payload))
}

func isSuperAdmin(role string) bool {
	return role == "super_admin" || role == "admin"
}

func canManageProjects(role string) bool {
	return isSuperAdmin(role) || role == "group_leader"
}

// getSessionUser returns the authenticated UserInfo and authorized domains from the request.
func (s *Server) getSessionUser(r *http.Request) *auth.UserInfo {
	sess, err := auth.GetSessionFromRequest(r)
	if err != nil || sess == nil {
		return nil
	}

	domains, _ := db.GetUserDomains(sess.UserID)
	return &auth.UserInfo{
		UserID:            sess.UserID,
		DisplayName:       sess.DisplayName,
		Role:              sess.Role,
		AuthorizedDomains: domains,
	}
}

// RecordAudit logs administrative actions to the audit_logs SQLite table.
func (s *Server) RecordAudit(r *http.Request, action, targetID string, detail interface{}) {
	user := s.getSessionUser(r)
	operator := "anonymous"
	if user != nil {
		operator = user.UserID
	}
	detailJSON, _ := json.Marshal(detail)
	targetType := "resource"
	if parts := strings.Split(action, "."); len(parts) > 0 {
		targetType = parts[0]
	}
	_ = db.RecordAuditLog(operator, action, targetType, targetID, string(detailJSON))
}

// UserManagementResponse holds user profile and their granted business domains.
type UserManagementResponse struct {
	Username          string   `json:"username"`
	DisplayName       string   `json:"display_name"`
	Role              string   `json:"role"`
	AuthorizedDomains []string `json:"authorized_domains"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

// handleUsers lists registered users with their roles and granted domains (Super Admin only).
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	user := s.getSessionUser(r)
	if user == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !isSuperAdmin(user.Role) {
		http.Error(w, `{"error":"Forbidden: super_admin required"}`, http.StatusForbidden)
		return
	}

	users, err := db.ListUsers()
	if err != nil {
		http.Error(w, "Failed to query users: "+err.Error(), http.StatusInternalServerError)
		return
	}

	res := make([]UserManagementResponse, 0, len(users))
	for _, u := range users {
		domains, _ := db.GetUserDomains(u.Username)
		res = append(res, UserManagementResponse{
			Username:          u.Username,
			DisplayName:       u.DisplayName,
			Role:              u.Role,
			AuthorizedDomains: domains,
			CreatedAt:         u.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt:         u.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

type SetRoleRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// handleSetUserRole modifies user RBAC role (Super Admin only).
func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := s.getSessionUser(r)
	if user == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !isSuperAdmin(user.Role) {
		http.Error(w, `{"error":"Forbidden: super_admin required"}`, http.StatusForbidden)
		return
	}

	var req SetRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.UserID == "" || (req.Role != "super_admin" && req.Role != "group_leader" && req.Role != "user") {
		http.Error(w, "Invalid user_id or role", http.StatusBadRequest)
		return
	}

	if err := db.SetUserRole(req.UserID, req.Role); err != nil {
		http.Error(w, "Failed to update role: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.RecordAudit(r, "user.set_role", req.UserID, req)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

type DomainGrantRequest struct {
	UserID         string `json:"user_id"`
	BusinessDomain string `json:"business_domain"`
}

// handleGrantDomain grants a business domain to a user (Super Admin only).
func (s *Server) handleGrantDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := s.getSessionUser(r)
	if user == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !isSuperAdmin(user.Role) {
		http.Error(w, `{"error":"Forbidden: super_admin required"}`, http.StatusForbidden)
		return
	}

	var req DomainGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	req.BusinessDomain = strings.TrimSpace(req.BusinessDomain)
	if req.UserID == "" || req.BusinessDomain == "" {
		http.Error(w, "user_id and business_domain are required", http.StatusBadRequest)
		return
	}

	if err := db.GrantDomain(req.UserID, req.BusinessDomain, user.UserID); err != nil {
		http.Error(w, "Failed to grant domain: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.RecordAudit(r, "user.grant_domain", req.UserID, req)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// handleRevokeDomain revokes a business domain grant from a user (Super Admin only).
func (s *Server) handleRevokeDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := s.getSessionUser(r)
	if user == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !isSuperAdmin(user.Role) {
		http.Error(w, `{"error":"Forbidden: super_admin required"}`, http.StatusForbidden)
		return
	}

	var req DomainGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.UserID = strings.TrimSpace(req.UserID)
	req.BusinessDomain = strings.TrimSpace(req.BusinessDomain)
	if req.UserID == "" || req.BusinessDomain == "" {
		http.Error(w, "user_id and business_domain are required", http.StatusBadRequest)
		return
	}

	if err := db.RevokeDomain(req.UserID, req.BusinessDomain); err != nil {
		http.Error(w, "Failed to revoke domain: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.RecordAudit(r, "user.revoke_domain", req.UserID, req)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// handleAudit returns audit logs from database.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	user := s.getSessionUser(r)
	if user == nil {
		http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !isSuperAdmin(user.Role) && user.Role != "group_leader" {
		http.Error(w, `{"error":"Forbidden: Administrator privileges required for audit logs"}`, http.StatusForbidden)
		return
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 && val <= 500 {
			limit = val
		}
	}

	logs, err := db.ListAuditLogs(limit)
	if err != nil {
		http.Error(w, "Failed to query audit logs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}
