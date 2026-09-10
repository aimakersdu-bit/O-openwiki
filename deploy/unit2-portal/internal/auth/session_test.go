package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openwiki/portal/internal/db"
)

func setupTestDB(t *testing.T) func() {
	tmpDir, err := os.MkdirTemp("", "unit2_sess_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "portal_test.db")
	err = db.InitDB(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("InitDB failed: %v", err)
	}

	return func() {
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}
}

func TestCreateAndRetrieveSession(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	user := &UserInfo{
		UserID:      "alice",
		DisplayName: "Alice Smith",
	}

	sess, err := CreateSession(user, 12)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if sess.Token == "" {
		t.Fatalf("expected non-empty token")
	}
	if sess.UserID != "alice" {
		t.Errorf("expected UserID alice, got %s", sess.UserID)
	}

	// Retrieve via HTTP Cookie
	reqCookie := httptest.NewRequest("GET", "/portal/me", nil)
	reqCookie.AddCookie(&http.Cookie{
		Name:  CookieName,
		Value: sess.Token,
	})

	gotSess, err := GetSessionFromRequest(reqCookie)
	if err != nil {
		t.Fatalf("GetSessionFromRequest (cookie) failed: %v", err)
	}
	if gotSess == nil || gotSess.UserID != "alice" {
		t.Errorf("expected session for alice via cookie")
	}

	// Retrieve via Bearer Header
	reqHeader := httptest.NewRequest("GET", "/portal/me", nil)
	reqHeader.Header.Set("Authorization", "Bearer "+sess.Token)

	gotSessHeader, err := GetSessionFromRequest(reqHeader)
	if err != nil {
		t.Fatalf("GetSessionFromRequest (bearer header) failed: %v", err)
	}
	if gotSessHeader == nil || gotSessHeader.UserID != "alice" {
		t.Errorf("expected session for alice via header")
	}
}

func TestSetAndClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()

	sess := &db.Session{
		Token:     "test-token-123",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	SetSessionCookie(w, sess)
	resp := w.Result()
	cookies := resp.Cookies()

	if len(cookies) == 0 {
		t.Fatalf("expected cookie to be set in response")
	}
	if cookies[0].Name != CookieName || cookies[0].Value != "test-token-123" {
		t.Errorf("cookie name/value mismatch: %+v", cookies[0])
	}
	if !cookies[0].HttpOnly {
		t.Errorf("expected HttpOnly cookie")
	}

	// Clear Cookie
	wClear := httptest.NewRecorder()
	ClearSessionCookie(wClear)
	respClear := wClear.Result()
	clearCookies := respClear.Cookies()

	if len(clearCookies) == 0 {
		t.Fatalf("expected clear cookie in response")
	}
	if clearCookies[0].MaxAge >= 0 && clearCookies[0].Value != "" {
		t.Errorf("expected max-age < 0 or empty value, got MaxAge=%d, Value=%s", clearCookies[0].MaxAge, clearCookies[0].Value)
	}
}
