package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

const (
	settingGitConfigured     = "git_auth_configured"
	settingGitEnabled        = "git_auth_enabled"
	settingGitAuthType       = "git_auth_type"
	settingGitHost           = "git_http_host"
	settingGitUsername       = "git_http_username"
	settingGitPassword       = "git_http_password"
	settingGitToken          = "git_http_token"
	settingGitOverrideInline = "git_override_inline"
	settingGitSSLNoVerify    = "git_ssl_no_verify"
	settingGitUpdatedAt      = "git_auth_updated_at"
)

func isSuperAdminRole(role string) bool {
	return role == "super_admin" || role == "admin"
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func maskGitSecret(secret string) string {
	s := strings.TrimSpace(secret)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= 4 {
		return "••••"
	}
	return string(runes[:2]) + "••••" + string(runes[len(runes)-2:])
}

// resolveGitCredentialsWithMeta resolves unified Git credentials from SQLite system_settings
// first, falling back to startup config/env vars until explicitly configured in DB.
func (s *Server) resolveGitCredentialsWithMeta() (scheduler.GitCredentials, string) {
	if database := s.getDB(); database != nil {
		if creds, updatedAt, ok := loadGitCredentialsFromDB(database); ok {
			return creds, updatedAt
		}
	}

	// Fallback to config / env before first DB save
	creds := scheduler.GitCredentialsFromEnv()
	if s.config != nil {
		if strings.TrimSpace(creds.Username) == "" && strings.TrimSpace(s.config.GitHTTPUsername) != "" {
			creds.Username = s.config.GitHTTPUsername
			if creds.Source == "none" {
				creds.Source = "config"
			}
		}
		if creds.Password == "" && s.config.GitHTTPPassword != "" {
			creds.Password = s.config.GitHTTPPassword
			creds.Enabled = true
			if creds.Token == "" {
				creds.AuthType = "password"
			}
			creds.Source = "config"
		}
		if creds.Token == "" && s.config.GitHTTPToken != "" {
			creds.Token = s.config.GitHTTPToken
			creds.Enabled = true
			creds.AuthType = "token"
			creds.Source = "config"
		}
	}
	if creds.AuthType == "" {
		creds.AuthType = "token"
	}
	return creds, ""
}

func loadGitCredentialsFromDB(database *db.DB) (scheduler.GitCredentials, string, bool) {
	if database == nil {
		return scheduler.GitCredentials{}, "", false
	}
	configured, err := database.GetSetting(settingGitConfigured, "")
	if err != nil || configured != "true" {
		return scheduler.GitCredentials{}, "", false
	}

	enabledStr, _ := database.GetSetting(settingGitEnabled, "true")
	authType, _ := database.GetSetting(settingGitAuthType, "token")
	host, _ := database.GetSetting(settingGitHost, "")
	username, _ := database.GetSetting(settingGitUsername, "")
	password, _ := database.GetSetting(settingGitPassword, "")
	token, _ := database.GetSetting(settingGitToken, "")
	overrideStr, _ := database.GetSetting(settingGitOverrideInline, "true")
	sslNoVerifyStr, _ := database.GetSetting(settingGitSSLNoVerify, "false")
	updatedAt, _ := database.GetSetting(settingGitUpdatedAt, "")

	if authType != "password" && authType != "token" {
		if token != "" || password == "" {
			authType = "token"
		} else {
			authType = "password"
		}
	}

	return scheduler.GitCredentials{
		Enabled:        enabledStr != "false",
		AuthType:       authType,
		Host:           scheduler.NormalizeGitHost(host),
		Username:       strings.TrimSpace(username),
		Password:       password,
		Token:          token,
		OverrideInline: overrideStr != "false",
		SSLNoVerify:    sslNoVerifyStr == "true",
		Source:         "database",
	}, updatedAt, true
}

func (s *Server) resolveGitCredentials() scheduler.GitCredentials {
	creds, _ := s.resolveGitCredentialsWithMeta()
	return creds
}

type gitCredentialsResponse struct {
	Status         string `json:"status,omitempty"`
	Message        string `json:"message,omitempty"`
	Enabled        bool   `json:"enabled"`
	AuthType       string `json:"auth_type"`
	Host           string `json:"host"`
	Username       string `json:"username"`
	HasPassword    bool   `json:"has_password"`
	HasToken       bool   `json:"has_token"`
	MaskedSecret   string `json:"masked_secret"`
	OverrideInline bool   `json:"override_inline"`
	SSLNoVerify    bool   `json:"ssl_no_verify"`
	Source         string `json:"source"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

func buildGitCredentialsResponse(creds scheduler.GitCredentials, updatedAt, status, message string) gitCredentialsResponse {
	activeSecret := creds.Token
	if creds.AuthType == "password" || (activeSecret == "" && creds.Password != "") {
		activeSecret = creds.Password
	}
	return gitCredentialsResponse{
		Status:         status,
		Message:        message,
		Enabled:        creds.Enabled,
		AuthType:       creds.AuthType,
		Host:           creds.Host,
		Username:       creds.Username,
		HasPassword:    creds.Password != "",
		HasToken:       creds.Token != "",
		MaskedSecret:   maskGitSecret(activeSecret),
		OverrideInline: creds.OverrideInline,
		SSLNoVerify:    creds.SSLNoVerify,
		Source:         creds.Source,
		UpdatedAt:      updatedAt,
	}
}

func (s *Server) handleGitCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	authCtx := GetAuthContext(r)
	if authCtx == nil || !isSuperAdminRole(authCtx.Role) {
		writeJSONError(w, http.StatusForbidden, "仅超级管理员可查看或配置全局 Git 认证凭据")
		return
	}

	switch r.Method {
	case http.MethodGet:
		creds, updatedAt := s.resolveGitCredentialsWithMeta()
		_ = json.NewEncoder(w).Encode(buildGitCredentialsResponse(creds, updatedAt, "", ""))

	case http.MethodPut, http.MethodPost:
		database := s.getDB()
		if database == nil {
			writeJSONError(w, http.StatusInternalServerError, "database unavailable")
			return
		}

		var req struct {
			Enabled        *bool   `json:"enabled"`
			AuthType       *string `json:"auth_type"`
			Host           *string `json:"host"`
			Username       *string `json:"username"`
			Password       *string `json:"password"`
			Token          *string `json:"token"`
			ClearPassword  bool    `json:"clear_password"`
			ClearToken     bool    `json:"clear_token"`
			OverrideInline *bool   `json:"override_inline"`
			SSLNoVerify    *bool   `json:"ssl_no_verify"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
			return
		}

		next, _ := s.resolveGitCredentialsWithMeta()
		if req.Enabled != nil {
			next.Enabled = *req.Enabled
		}
		if req.AuthType != nil {
			at := strings.ToLower(strings.TrimSpace(*req.AuthType))
			if at != "token" && at != "password" {
				writeJSONError(w, http.StatusBadRequest, "auth_type 仅支持 token 或 password")
				return
			}
			next.AuthType = at
		}
		if next.AuthType == "" {
			next.AuthType = "token"
		}
		if req.Host != nil {
			next.Host = scheduler.NormalizeGitHost(*req.Host)
		}
		if req.Username != nil {
			next.Username = strings.TrimSpace(*req.Username)
		}
		if req.ClearPassword {
			next.Password = ""
		} else if req.Password != nil && *req.Password != "" {
			next.Password = *req.Password
		}
		if req.ClearToken {
			next.Token = ""
		} else if req.Token != nil && *req.Token != "" {
			next.Token = *req.Token
		}
		if req.OverrideInline != nil {
			next.OverrideInline = *req.OverrideInline
		}
		if req.SSLNoVerify != nil {
			next.SSLNoVerify = *req.SSLNoVerify
		}

		if next.Enabled {
			if next.AuthType == "password" {
				if next.Username == "" || next.Password == "" {
					writeJSONError(w, http.StatusBadRequest, "启用账号密码认证时，用户名和密码不能为空")
					return
				}
			} else {
				if next.Token == "" {
					writeJSONError(w, http.StatusBadRequest, "启用 Token 认证时，Access Token 不能为空")
					return
				}
			}
		}

		nowStr := time.Now().Format(time.RFC3339)
		boolStr := func(v bool) string {
			if v {
				return "true"
			}
			return "false"
		}

		pairs := map[string]string{
			settingGitConfigured:     "true",
			settingGitEnabled:        boolStr(next.Enabled),
			settingGitAuthType:       next.AuthType,
			settingGitHost:           next.Host,
			settingGitUsername:       next.Username,
			settingGitPassword:       next.Password,
			settingGitToken:          next.Token,
			settingGitOverrideInline: boolStr(next.OverrideInline),
			settingGitSSLNoVerify:    boolStr(next.SSLNoVerify),
			settingGitUpdatedAt:      nowStr,
		}
		for k, v := range pairs {
			if err := database.SetSetting(k, v); err != nil {
				writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("保存配置失败: %v", err))
				return
			}
		}

		next.Source = "database"
		_ = scheduler.SyncGitCredentialsFile(next)

		_ = json.NewEncoder(w).Encode(buildGitCredentialsResponse(next, nowStr, "ok", "全局 Git 认证配置已保存并立即生效"))

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTestGitCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	authCtx := GetAuthContext(r)
	if authCtx == nil || !isSuperAdminRole(authCtx.Role) {
		writeJSONError(w, http.StatusForbidden, "仅超级管理员可测试全局 Git 认证凭据")
		return
	}

	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		GitURL         string  `json:"git_url"`
		Enabled        *bool   `json:"enabled"`
		AuthType       *string `json:"auth_type"`
		Host           *string `json:"host"`
		Username       *string `json:"username"`
		Password       *string `json:"password"`
		Token          *string `json:"token"`
		OverrideInline *bool   `json:"override_inline"`
		SSLNoVerify    *bool   `json:"ssl_no_verify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}

	gitURL := strings.TrimSpace(req.GitURL)
	if gitURL == "" {
		writeJSONError(w, http.StatusBadRequest, "请输入待测试的仓库地址 (git_url)")
		return
	}

	creds, _ := s.resolveGitCredentialsWithMeta()
	if req.Enabled != nil {
		creds.Enabled = *req.Enabled
	} else {
		creds.Enabled = true
	}
	if req.AuthType != nil && *req.AuthType != "" {
		creds.AuthType = strings.ToLower(strings.TrimSpace(*req.AuthType))
	}
	if req.Host != nil {
		creds.Host = scheduler.NormalizeGitHost(*req.Host)
	}
	if req.Username != nil {
		creds.Username = strings.TrimSpace(*req.Username)
	}
	if req.Password != nil && *req.Password != "" {
		creds.Password = *req.Password
	}
	if req.Token != nil && *req.Token != "" {
		creds.Token = *req.Token
	}
	if req.OverrideInline != nil {
		creds.OverrideInline = *req.OverrideInline
	}
	if req.SSLNoVerify != nil {
		creds.SSLNoVerify = *req.SSLNoVerify
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := scheduler.TestGitConnection(ctx, gitURL, creds); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "Git 仓库认证与连通性校验成功",
	})
}
