package auth

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/openwiki/portal/internal/db"
)

const CookieName = "openwiki_session"

// CreateSession generates a new session token and saves it to SQLite DB.
func CreateSession(user *UserInfo, ttlHours int) (*db.Session, error) {
	if ttlHours <= 0 {
		ttlHours = 24
	}

	token := uuid.New().String()
	expiresAt := time.Now().Add(time.Duration(ttlHours) * time.Hour)

	sess := &db.Session{
		Token:       token,
		UserID:      user.UserID,
		DisplayName: user.DisplayName,
		ExpiresAt:   expiresAt,
	}

	if err := db.SaveSession(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetSessionFromRequest reads session token from HTTP cookie or Authorization header.
func GetSessionFromRequest(r *http.Request) (*db.Session, error) {
	var token string

	cookie, err := r.Cookie(CookieName)
	if err == nil && cookie.Value != "" {
		token = cookie.Value
	} else {
		// Fallback to Bearer token header
		authHeader := r.Header.Get("Authorization")
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token = authHeader[7:]
		}
	}

	if token == "" {
		return nil, nil
	}

	return db.GetSession(token)
}

// SetSessionCookie sets session cookie in HTTP response.
func SetSessionCookie(w http.ResponseWriter, session *db.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    session.Token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie deletes session cookie in HTTP response.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}
