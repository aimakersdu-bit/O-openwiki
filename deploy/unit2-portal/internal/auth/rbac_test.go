package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openwiki/portal/internal/auth"
	"github.com/openwiki/portal/internal/config"
	"github.com/openwiki/portal/internal/db"
)

func setupRBACDB(t *testing.T) func() {
	tmpDir, err := os.MkdirTemp("", "rbac_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "rbac_test.db")
	err = db.InitDB(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Skipf("skipping test, mysql not reachable: %v", err)
	}

	return func() {
		db.CloseDB()
		os.RemoveAll(tmpDir)
	}
}

func TestGrantAndRevokeDomain(t *testing.T) {
	cleanup := setupRBACDB(t)
	defer cleanup()

	username := "leader_zhang"
	// Grant domain "消费金融" and "信用卡"
	err := db.GrantDomain(username, "消费金融", "admin_li")
	if err != nil {
		t.Fatalf("GrantDomain failed: %v", err)
	}
	err = db.GrantDomain(username, "信用卡", "admin_li")
	if err != nil {
		t.Fatalf("GrantDomain failed: %v", err)
	}

	domains, err := db.GetUserDomains(username)
	if err != nil || len(domains) != 2 {
		t.Fatalf("expected 2 domains, got %v", domains)
	}

	// Revoke domain "信用卡"
	err = db.RevokeDomain(username, "信用卡")
	if err != nil {
		t.Fatalf("RevokeDomain failed: %v", err)
	}

	domains, _ = db.GetUserDomains(username)
	if len(domains) != 1 || domains[0] != "消费金融" {
		t.Fatalf("expected only 消费金融, got %v", domains)
	}
}

func TestGroupLeaderDomainScope(t *testing.T) {
	cleanup := setupRBACDB(t)
	defer cleanup()

	leader := "leader_wang"
	_ = db.SetUserRole(leader, "group_leader")
	_ = db.GrantDomain(leader, "零售金融", "super_admin")

	// Authorized domain
	ok, err := db.IsUserAuthorizedForDomain(leader, "group_leader", "零售金融")
	if err != nil || !ok {
		t.Fatalf("expected leader authorized for 零售金融, got %v, err %v", ok, err)
	}

	// Unauthorized domain
	ok, err = db.IsUserAuthorizedForDomain(leader, "group_leader", "对公业务")
	if err != nil || ok {
		t.Fatalf("expected leader blocked on 对公业务, got %v", ok)
	}

	// Plain user has no domain access even if domain name matches
	ok, _ = db.IsUserAuthorizedForDomain("plain_user", "user", "零售金融")
	if ok {
		t.Fatalf("plain user should never be authorized for domains")
	}
}

func TestSuperAdminBreakGlass(t *testing.T) {
	cleanup := setupRBACDB(t)
	defer cleanup()

	// Super admin has universal break-glass access without explicit grants
	ok, err := db.IsUserAuthorizedForDomain("admin_root", "super_admin", "任意受限业务域")
	if err != nil || !ok {
		t.Fatalf("super_admin must have break-glass access, got %v", ok)
	}

	// Legacy "admin" role also has universal access
	ok, err = db.IsUserAuthorizedForDomain("admin_legacy", "admin", "任意受限业务域")
	if err != nil || !ok {
		t.Fatalf("admin must have break-glass access, got %v", ok)
	}
}

func TestAuthenticateSyncsProfileAndDomains(t *testing.T) {
	cleanup := setupRBACDB(t)
	defer cleanup()

	cfg := &config.Config{}
	cfg.Auth.AdminUsers = []string{"super_boss"}
	cfg.LDAP.Enabled = false // mock mode

	// 1. Authenticate super admin
	u1, err := auth.Authenticate(cfg, "super_boss", "password")
	if err != nil || u1.Role != "super_admin" {
		t.Fatalf("expected super_admin role, got %s, err %v", u1.Role, err)
	}

	// 2. Setup group leader in DB
	_ = db.SetUserRole("leader_zhao", "group_leader")
	_ = db.GrantDomain("leader_zhao", "普惠金融", "super_boss")

	// Authenticate group leader
	u2, err := auth.Authenticate(cfg, "leader_zhao", "password")
	if err != nil || u2.Role != "group_leader" {
		t.Fatalf("expected group_leader role, got %s", u2.Role)
	}
	if len(u2.AuthorizedDomains) != 1 || u2.AuthorizedDomains[0] != "普惠金融" {
		t.Fatalf("expected 普惠金融 domain, got %v", u2.AuthorizedDomains)
	}
}
