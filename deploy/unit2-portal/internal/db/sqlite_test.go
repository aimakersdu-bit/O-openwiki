package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) func() {
	tmpDir, err := os.MkdirTemp("", "unit2_test_db_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "portal_test.db")
	err = InitDB(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("InitDB failed: %v", err)
	}

	return func() {
		CloseDB()
		os.RemoveAll(tmpDir)
	}
}

func TestSessionLifecycle(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	token := "token-uuid-12345"
	sess := &Session{
		Token:       token,
		UserID:      "testuser",
		DisplayName: "Test User",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	// 1. Save Session
	err := SaveSession(sess)
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// 2. Get Valid Session
	got, err := GetSession(token)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got == nil {
		t.Fatalf("expected session, got nil")
	}
	if got.UserID != "testuser" {
		t.Errorf("expected UserID testuser, got %s", got.UserID)
	}

	// 3. Delete Session (Logout)
	err = DeleteSession(token)
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	gotAfterDelete, err := GetSession(token)
	if err != nil {
		t.Fatalf("GetSession after delete failed: %v", err)
	}
	if gotAfterDelete != nil {
		t.Errorf("expected nil after delete, got session: %+v", gotAfterDelete)
	}
}

func TestCleanExpiredSessions(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	expiredSess := &Session{
		Token:       "expired-token",
		UserID:      "user-expired",
		DisplayName: "Expired User",
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().Add(-1 * time.Hour),
	}

	err := SaveSession(expiredSess)
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Immediate query should return nil (GetSession filters by expires_at > CURRENT_TIMESTAMP)
	got, err := GetSession("expired-token")
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got != nil {
		t.Errorf("expected expired session to be filtered out")
	}

	// Run cleanup command
	err = CleanExpiredSessions()
	if err != nil {
		t.Fatalf("CleanExpiredSessions failed: %v", err)
	}
}
