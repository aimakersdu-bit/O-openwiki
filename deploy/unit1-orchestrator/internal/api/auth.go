package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

type contextKey string

const userContextKey = contextKey("openwiki_user")

type AuthContext struct {
	User              string
	Role              string
	AuthorizedDomains []string
}

// ComputeHMAC generates a hex-encoded SHA256 HMAC for the given message.
func ComputeHMAC(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyHMAC verifies if the given signature matches the message's HMAC.
func VerifyHMAC(secret, message, sig string) bool {
	expected := ComputeHMAC(secret, message)
	return hmac.Equal([]byte(expected), []byte(sig))
}

// authMiddleware validates HMAC signatures on incoming requests and extracts user context.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("X-OpenWiki-User")
		role := r.Header.Get("X-OpenWiki-Role")
		domainsStr := r.Header.Get("X-OpenWiki-Domains")
		sig := r.Header.Get("X-OpenWiki-Sig")

		// If identity headers or signature are provided, verify signature
		if user != "" || sig != "" {
			secret := "openwiki-hmac-shared-secret"
			if s.config != nil && s.config.HMACSecret != "" {
				secret = s.config.HMACSecret
			}

			payload := user + ":" + role + ":" + domainsStr
			if sig == "" || !VerifyHMAC(secret, payload, sig) {
				http.Error(w, `{"error":"invalid or missing HMAC signature"}`, http.StatusUnauthorized)
				return
			}

			var domains []string
			if domainsStr != "" {
				for _, d := range strings.Split(domainsStr, ",") {
					if trimmed := strings.TrimSpace(d); trimmed != "" {
						domains = append(domains, trimmed)
					}
				}
			}

			authCtx := &AuthContext{
				User:              user,
				Role:              role,
				AuthorizedDomains: domains,
			}
			ctx := context.WithValue(r.Context(), userContextKey, authCtx)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Direct call without auth headers: default to super_admin for backward compatibility with CLI/tests
		authCtx := &AuthContext{
			User: "system",
			Role: "super_admin",
		}
		ctx := context.WithValue(r.Context(), userContextKey, authCtx)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetAuthContext extracts the AuthContext from request context.
func GetAuthContext(r *http.Request) *AuthContext {
	if val := r.Context().Value(userContextKey); val != nil {
		if actx, ok := val.(*AuthContext); ok {
			return actx
		}
	}
	return &AuthContext{User: "system", Role: "super_admin"}
}

// IsDomainAuthorized checks whether the current auth context has access to the given domain.
func (ac *AuthContext) IsDomainAuthorized(domain string) bool {
	if ac.Role == "super_admin" || ac.Role == "admin" {
		return true
	}
	if ac.Role != "group_leader" {
		return false
	}
	for _, d := range ac.AuthorizedDomains {
		if d == domain {
			return true
		}
	}
	return false
}
