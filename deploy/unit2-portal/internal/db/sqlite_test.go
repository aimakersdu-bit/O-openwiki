package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func TestListUsersAndConcurrentWritesNoDeadlockOrLock(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		uname := fmt.Sprintf("leader-%d", i)
		if err := UpsertUser(&User{Username: uname, DisplayName: uname, Role: "group_leader"}); err != nil {
			t.Fatalf("UpsertUser failed: %v", err)
		}
		if err := GrantDomain(uname, fmt.Sprintf("domain-%d", i), "admin"); err != nil {
			t.Fatalf("GrantDomain failed: %v", err)
		}
	}

	// Verify ListUsers does not deadlock on single-connection pool
	users, err := ListUsers()
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 5 {
		t.Fatalf("expected 5 users, got %d", len(users))
	}
	for _, u := range users {
		if len(u.AuthorizedDomains) != 1 {
			t.Fatalf("expected 1 domain for user %s, got %v", u.Username, u.AuthorizedDomains)
		}
	}

	errCh := make(chan error, 128)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				tok := fmt.Sprintf("tok-%d-%d", id, i)
				if err := SaveSession(&Session{
					Token:     tok,
					UserID:    fmt.Sprintf("leader-%d", id%5),
					Role:      "group_leader",
					ExpiresAt: time.Now().Add(time.Hour),
				}); err != nil {
					errCh <- fmt.Errorf("SaveSession: %w", err)
					return
				}
				if err := RecordAuditLog("admin", "batch.status", "batch", tok, "{}"); err != nil {
					errCh <- fmt.Errorf("RecordAuditLog: %w", err)
					return
				}
				if _, err := GetSession(tok); err != nil {
					errCh <- fmt.Errorf("GetSession: %w", err)
					return
				}
				if _, err := ListUsers(); err != nil {
					errCh <- fmt.Errorf("ListUsers: %w", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent portal DB error: %v", err)
	}
}

func TestBusinessDomainCodeCRUD(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	if err := CreateBusinessDomain(&BusinessDomain{
		Name:        "数字员工",
		Code:        "DEO",
		Description: "数字员工业务域",
		SortOrder:   1,
		Status:      "active",
	}); err != nil {
		t.Fatalf("CreateBusinessDomain failed: %v", err)
	}

	list, err := ListBusinessDomains()
	if err != nil {
		t.Fatalf("ListBusinessDomains failed: %v", err)
	}
	var created *BusinessDomain
	for i := range list {
		if list[i].Name == "数字员工" {
			created = &list[i]
			break
		}
	}
	if created == nil || created.Code != "DEO" {
		t.Fatalf("expected domain 数字员工 with code DEO, got %+v", list)
	}

	created.Code = "DE2"
	if err := UpdateBusinessDomain(created); err != nil {
		t.Fatalf("UpdateBusinessDomain failed: %v", err)
	}
	all, err := ListAllBusinessDomains()
	if err != nil {
		t.Fatalf("ListAllBusinessDomains failed: %v", err)
	}
	foundUpdated := false
	for _, d := range all {
		if d.ID == created.ID && d.Code == "DE2" {
			foundUpdated = true
		}
	}
	if !foundUpdated {
		t.Fatalf("expected updated domain code DE2, got %+v", all)
	}
}
