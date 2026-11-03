package storage_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/storage"
)

type mockQAEvictor struct {
	evictedRepos []string
}

func (m *mockQAEvictor) Evict(repoID string) {
	m.evictedRepos = append(m.evictedRepos, repoID)
}

func setupMigratorTest(t *testing.T) (*storage.Migrator, *storage.VolumeManager, *db.DB, *mockQAEvictor, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_migrator.db")
	_ = db.InitDB(dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}

	linksDir := filepath.Join(tmpDir, "disk0", "links", "repos")
	vm := storage.NewVolumeManager(database, linksDir)
	evictor := &mockQAEvictor{}
	migrator := storage.NewMigrator(database, vm, evictor, linksDir)

	return migrator, vm, database, evictor, tmpDir
}

func TestMigrateStateMachineHappyPath(t *testing.T) {
	migrator, vm, database, evictor, tmpDir := setupMigratorTest(t)
	defer database.Close()

	vol1Path := filepath.Join(tmpDir, "vol1")
	vol2Path := filepath.Join(tmpDir, "vol2")
	_ = os.MkdirAll(vol1Path, 0755)
	_ = os.MkdirAll(vol2Path, 0755)
	_, _ = vm.RegisterVolume("vol1", vol1Path)
	_, _ = vm.RegisterVolume("vol2", vol2Path)

	repoID := "loan-core"
	physicalVol1, symlinkPath, err := vm.CreateRepoPlacement(repoID, "vol1")
	if err != nil {
		t.Fatalf("CreateRepoPlacement failed: %v", err)
	}

	// Create files in source repo
	_ = os.WriteFile(filepath.Join(physicalVol1, "service.go"), []byte("package main"), 0644)
	_ = os.WriteFile(filepath.Join(physicalVol1, "README.md"), []byte("# Loan Core"), 0644)

	repo := &db.Repo{
		ID:        repoID,
		Name:      "Loan Core",
		GitURL:    "git@bank.com/loan-core.git",
		VolumeID:  "vol1",
		Status:    "active",
		LocalPath: symlinkPath,
	}
	_ = database.CreateRepo(repo)

	// Execute migration to vol2
	mig, err := migrator.Migrate(context.Background(), repoID, "vol2")
	if err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	if mig.Status != "retained" {
		t.Fatalf("expected final status 'retained', got %s", mig.Status)
	}

	// Verify QA worker was evicted during frozen stage
	if len(evictor.evictedRepos) != 1 || evictor.evictedRepos[0] != repoID {
		t.Fatalf("expected QA eviction for %s, got %v", repoID, evictor.evictedRepos)
	}

	// Verify symlink now points to vol2
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("readlink failed: %v", err)
	}
	expectedTarget := filepath.Join(vol2Path, "repos", repoID)
	if target != expectedTarget {
		t.Fatalf("expected symlink target %s, got %s", expectedTarget, target)
	}

	// Verify DB record updated
	updatedRepo, _ := database.GetRepo(repoID)
	if updatedRepo.VolumeID != "vol2" {
		t.Fatalf("expected DB volume_id 'vol2', got %s", updatedRepo.VolumeID)
	}
	if updatedRepo.Status != "active" {
		t.Fatalf("expected DB status 'active', got %s", updatedRepo.Status)
	}

	// Test cleanup old replica on vol1
	if err := migrator.CleanupRetained(mig.ID); err != nil {
		t.Fatalf("CleanupRetained failed: %v", err)
	}
	if _, err := os.Stat(physicalVol1); !os.IsNotExist(err) {
		t.Fatalf("expected old replica on vol1 to be deleted")
	}
}

func TestMigrateVerifyFailAutoRollback(t *testing.T) {
	migrator, vm, database, _, tmpDir := setupMigratorTest(t)
	defer database.Close()

	vol1Path := filepath.Join(tmpDir, "vol1")
	vol2Path := filepath.Join(tmpDir, "vol2")
	_ = os.MkdirAll(vol1Path, 0755)
	_ = os.MkdirAll(vol2Path, 0755)
	_, _ = vm.RegisterVolume("vol1", vol1Path)
	_, _ = vm.RegisterVolume("vol2", vol2Path)

	repoID := "risk-engine"
	physicalVol1, symlinkPath, _ := vm.CreateRepoPlacement(repoID, "vol1")
	_ = os.WriteFile(filepath.Join(physicalVol1, "data.bin"), []byte("complete payload data"), 0644)

	repo := &db.Repo{
		ID:        repoID,
		Name:      "Risk Engine",
		VolumeID:  "vol1",
		Status:    "active",
		LocalPath: symlinkPath,
	}
	_ = database.CreateRepo(repo)

	// Mock copyFunc to simulate corrupted copy (mismatched size)
	migrator.SetCopyFunc(func(src, dst string) error {
		_ = os.MkdirAll(dst, 0755)
		_ = os.WriteFile(filepath.Join(dst, "data.bin"), []byte("corrupted"), 0644)
		return nil
	})

	_, err := migrator.Migrate(context.Background(), repoID, "vol2")
	if err == nil {
		t.Fatalf("expected migration to fail verification, but it succeeded")
	}

	// Verify symlink still points to vol1
	target, _ := os.Readlink(symlinkPath)
	if target != physicalVol1 {
		t.Fatalf("expected symlink rolled back to %s, got %s", physicalVol1, target)
	}

	// Verify repo status restored to active
	r, _ := database.GetRepo(repoID)
	if r.Status != "active" || r.VolumeID != "vol1" {
		t.Fatalf("expected repo active on vol1, got status=%s vol=%s", r.Status, r.VolumeID)
	}
}

func TestMigrateResumeOnRestart(t *testing.T) {
	migrator, _, database, _, _ := setupMigratorTest(t)
	defer database.Close()

	repo := &db.Repo{
		ID:       "interrupted-repo",
		Name:     "Interrupted",
		VolumeID: "vol1",
		Status:   "migrating",
	}
	_ = database.CreateRepo(repo)

	mig := &db.VolumeMigration{
		RepoID:       "interrupted-repo",
		FromVolumeID: "vol1",
		ToVolumeID:   "vol2",
		Status:       "frozen",
	}
	migID, _ := database.CreateVolumeMigration(mig)

	// Simulate system restart
	if err := migrator.ResumeOnRestart(); err != nil {
		t.Fatalf("ResumeOnRestart failed: %v", err)
	}

	recoveredMig, _ := database.GetVolumeMigration(migID)
	if recoveredMig.Status != "rollback" {
		t.Fatalf("expected migration status rollback, got %s", recoveredMig.Status)
	}

	recoveredRepo, _ := database.GetRepo("interrupted-repo")
	if recoveredRepo.Status != "active" {
		t.Fatalf("expected repo status recovered to active, got %s", recoveredRepo.Status)
	}
}

func TestRebalancePlanGeneration(t *testing.T) {
	migrator, _, database, _, tmpDir := setupMigratorTest(t)
	defer database.Close()

	// vol1: 1000GB total, 800GB used (80%)
	// vol2: 1000GB total, 100GB used (10%)
	vol1Path := filepath.Join(tmpDir, "vol1")
	vol2Path := filepath.Join(tmpDir, "vol2")
	_ = database.CreateVolume(&db.Volume{
		ID:         "vol1",
		MountPoint: vol1Path,
		TotalBytes: 1000 * 1024 * 1024 * 1024,
		UsedBytes:  800 * 1024 * 1024 * 1024,
		Status:     "active",
	})
	_ = database.CreateVolume(&db.Volume{
		ID:         "vol2",
		MountPoint: vol2Path,
		TotalBytes: 1000 * 1024 * 1024 * 1024,
		UsedBytes:  100 * 1024 * 1024 * 1024,
		Status:     "active",
	})

	// Add 3 repos on vol1: 300GB, 250GB, 250GB
	for i, sizeGB := range []int64{300, 250, 250} {
		_ = database.CreateRepo(&db.Repo{
			ID:        fmt.Sprintf("repo-%d", i+1),
			Name:      fmt.Sprintf("Heavy Repo %d", i+1),
			VolumeID:  "vol1",
			SizeBytes: sizeGB * 1024 * 1024 * 1024,
			Status:    "active",
		})
	}

	plan, err := migrator.GenerateRebalancePlan()
	if err != nil {
		t.Fatalf("GenerateRebalancePlan failed: %v", err)
	}

	if len(plan) == 0 {
		t.Fatalf("expected rebalance actions, got 0")
	}

	// Verify plan moves repos from vol1 to vol2
	for _, a := range plan {
		if a.FromVolumeID != "vol1" || a.ToVolumeID != "vol2" {
			t.Fatalf("expected action from vol1 to vol2, got %+v", a)
		}
	}
}
