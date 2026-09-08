package api

import (
	"encoding/json"
	"net/http"

	"github.com/openwiki/portal/internal/auth"
	"github.com/openwiki/portal/internal/db"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	User  *auth.UserInfo `json:"user"`
	Token string         `json:"token"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	userInfo, err := auth.Authenticate(s.config, req.Username, req.Password)
	if err != nil {
		http.Error(w, "Authentication failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	session, err := auth.CreateSession(userInfo, s.config.SessionTTLHours)
	if err != nil {
		http.Error(w, "Failed to create session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	auth.SetSessionCookie(w, session)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(LoginResponse{
		User:  userInfo,
		Token: session.Token,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	session, _ := auth.GetSessionFromRequest(r)
	if session != nil {
		_ = db.DeleteSession(session.Token)
	}
	auth.ClearSessionCookie(w)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"message":"Logged out"}`))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	session, err := auth.GetSessionFromRequest(r)
	if err != nil || session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"user_id":      session.UserID,
		"display_name": session.DisplayName,
		"expires_at":   session.ExpiresAt,
	})
}
