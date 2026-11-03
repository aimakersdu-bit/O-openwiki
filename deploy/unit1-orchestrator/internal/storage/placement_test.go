package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/storage"
)

func setupTestStorage(t *testing.T) (*storage.VolumeManager, *db.DB, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_storage.db")
	_ = db.InitDB(dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open db failed: %v", err)
	}

	linksDir := filepath.Join(tmpDir, "disk0", "links", "repos")
	vm := storage.NewVolumeManager(database, linksDir)
	return vm, database, tmpDir
}

func TestWeightedPlacement(t *testing.T) {
	vm, database, tmpDir := setupTestStorage(t)
	defer database.Close()

	vol1Path := filepath.Join(tmpDir, "vol1")
	vol2Path := filepath.Join(tmpDir, "vol2")
	vol3Path := filepath.Join(tmpDir, "vol3")

	_ = os.MkdirAll(vol1Path, 0755)
	_ = os.MkdirAll(vol2Path, 0755)
	_ = os.MkdirAll(vol3Path, 0755)

	// Mock capacities: vol1 (50% used), vol2 (20% used), vol3 (70% used)
	mockUsage := map[string][2]int64{
		vol1Path: {1000 * 1024 * 1024 * 1024, 500 * 1024 * 1024 * 1024},
		vol2Path: {1000 * 1024 * 1024 * 1024, 200 * 1024 * 1024 * 1024},
		vol3Path: {1000 * 1024 * 1024 * 1024, 700 * 1024 * 1024 * 1024},
	}

	vm.SetStatfsFunc(func(path string) (int64, int64, error) {
		if u, ok := mockUsage[path]; ok {
			return u[0], u[1], nil
		}
		return 1000 * 1024 * 1024 * 1024, 100 * 1024 * 1024 * 1024, nil
	})

	_, _ = vm.RegisterVolume("vol1", vol1Path)
	_, _ = vm.RegisterVolume("vol2", vol2Path)
	_, _ = vm.RegisterVolume("vol3", vol3Path)

	// Select optimal volume for a 1GB repo
	chosen, err := vm.SelectVolumeForRepo("test-repo-1", 1024*1024*1024)
	if err != nil {
		t.Fatalf("expected successful placement, got %v", err)
	}

	// vol2 has only 20% usage, lowest load -> must be selected
	if chosen.ID != "vol2" {
		t.Fatalf("expected vol2 to be chosen, got %s", chosen.ID)
	}
}

func TestRejectWhenAllVolumesOver80Pct(t *testing.T) {
	vm, database, tmpDir := setupTestStorage(t)
	defer database.Close()

	vol1Path := filepath.Join(tmpDir, "vol1")
	vol2Path := filepath.Join(tmpDir, "vol2")
	_ = os.MkdirAll(vol1Path, 0755)
	_ = os.MkdirAll(vol2Path, 0755)

	// Both volumes are over 80% (85% and 90%)
	mockUsage := map[string][2]int64{
		vol1Path: {1000 * 1024 * 1024 * 1024, 850 * 1024 * 1024 * 1024},
		vol2Path: {1000 * 1024 * 1024 * 1024, 900 * 1024 * 1024 * 1024},
	}

	vm.SetStatfsFunc(func(path string) (int64, int64, error) {
		if u, ok := mockUsage[path]; ok {
			return u[0], u[1], nil
		}
		return 1000 * 1024 * 1024 * 1024, 900 * 1024 * 1024 * 1024, nil
	})

	_, _ = vm.RegisterVolume("vol1", vol1Path)
	_, _ = vm.RegisterVolume("vol2", vol2Path)

	_, err := vm.SelectVolumeForRepo("test-repo-overflow", 1024*1024*1024)
	if err == nil {
		t.Fatalf("expected rejection when all volumes > 80%%, but got success")
	}

	if !strings.Contains(err.Error(), "80% utilization threshold") {
		t.Fatalf("expected error message to mention 80%% utilization threshold, got %v", err)
	}
}

func TestLogicalSymlinkCreation(t *testing.T) {
	vm, database, tmpDir := setupTestStorage(t)
	defer database.Close()

	vol1Path := filepath.Join(tmpDir, "vol1")
	_ = os.MkdirAll(vol1Path, 0755)
	_, _ = vm.RegisterVolume("vol1", vol1Path)

	repoID := "trade-gateway"
	physicalPath, symlinkPath, err := vm.CreateRepoPlacement(repoID, "vol1")
	if err != nil {
		t.Fatalf("CreateRepoPlacement failed: %v", err)
	}

	// 1. Verify physical path exists
	fi, err := os.Stat(physicalPath)
	if err != nil || !fi.IsDir() {
		t.Fatalf("physical directory %s does not exist or is not a dir", physicalPath)
	}

	// 2. Verify symlink exists and resolves to physical path
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("readlink %s failed: %v", symlinkPath, err)
	}
	if target != physicalPath {
		t.Fatalf("expected symlink target %s, got %s", physicalPath, target)
	}

	// 3. Test I/O transparency through symlink
	testFile := filepath.Join(symlinkPath, "README.md")
	content := []byte("# Trade Gateway Microservice")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("write file through symlink failed: %v", err)
	}

	readDirect, err := os.ReadFile(filepath.Join(physicalPath, "README.md"))
	if err != nil || string(readDirect) != string(content) {
		t.Fatalf("expected direct file read to match symlink write")
	}

	// 4. Test directory size calculation
	size, err := vm.UpdateRepoSize(repoID, physicalPath)
	if err != nil || size <= 0 {
		t.Fatalf("expected positive repo size, got %d, err %v", size, err)
	}
}
