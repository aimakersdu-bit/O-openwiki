package auth

import (
	"testing"

	"github.com/openwiki/portal/internal/config"
)

func TestAuthenticateMock(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LDAP.URL = "mock"

	// Test valid credentials with mock LDAP
	user, err := Authenticate(cfg, "john_doe", "secret123")
	if err != nil {
		t.Fatalf("Authenticate failed with mock LDAP: %v", err)
	}
	if user == nil {
		t.Fatalf("expected user, got nil")
	}
	if user.UserID != "john_doe" {
		t.Errorf("expected UserID john_doe, got %s", user.UserID)
	}
	if user.DisplayName != "Dev User (john_doe)" {
		t.Errorf("expected DisplayName 'Dev User (john_doe)', got %s", user.DisplayName)
	}
}

func TestAuthenticateEmptyCredentials(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LDAP.URL = "mock"

	_, err := Authenticate(cfg, "", "secret")
	if err == nil {
		t.Errorf("expected error for empty username, got nil")
	}

	_, err = Authenticate(cfg, "user", "")
	if err == nil {
		t.Errorf("expected error for empty password, got nil")
	}
}
