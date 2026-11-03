package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/openwiki/orchestrator/internal/db"
)

// StatfsFunc defines the function type to query disk capacity.
type StatfsFunc func(path string) (totalBytes, usedBytes int64, err error)

// DefaultStatfs uses system syscall.Statfs to inspect disk space.
func DefaultStatfs(path string) (totalBytes, usedBytes int64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	total := int64(stat.Blocks) * int64(stat.Bsize)
	free := int64(stat.Bavail) * int64(stat.Bsize)
	used := total - free
	return total, used, nil
}

// VolumeManager manages multi-disk storage placement, symlink indirection, and capacity monitoring.
type VolumeManager struct {
	mu           sync.RWMutex
	db           *db.DB
	linksBaseDir string
	activeTasks  map[string]int // volume_id -> current concurrent build tasks
	statfs       StatfsFunc
}

// NewVolumeManager creates a new VolumeManager instance.
func NewVolumeManager(database *db.DB, linksBaseDir string) *VolumeManager {
	if linksBaseDir == "" {
		linksBaseDir = "/data/disk0/links/repos"
	}
	return &VolumeManager{
		db:           database,
		linksBaseDir: linksBaseDir,
		activeTasks:  make(map[string]int),
		statfs:       DefaultStatfs,
	}
}

// SetStatfsFunc overrides the statfs implementation (useful for tests).
func (vm *VolumeManager) SetStatfsFunc(fn StatfsFunc) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.statfs = fn
}

// SetTaskBusy updates the busy build count for a given volume.
func (vm *VolumeManager) SetTaskBusy(volumeID string, delta int) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.activeTasks[volumeID] += delta
	if vm.activeTasks[volumeID] < 0 {
		vm.activeTasks[volumeID] = 0
	}
}

// RegisterVolume registers or updates a storage volume.
func (vm *VolumeManager) RegisterVolume(id, mountPoint string) (*db.Volume, error) {
	total, used, _ := vm.statfs(mountPoint)
	v := &db.Volume{
		ID:         id,
		MountPoint: mountPoint,
		TotalBytes: total,
		UsedBytes:  used,
		Status:     "active",
	}
	if err := vm.db.CreateVolume(v); err != nil {
		// Update if already exists
		_ = vm.db.UpdateVolumeUsage(id, used, total)
	}
	return vm.db.GetVolume(id)
}

// SelectVolumeForRepo selects the optimal volume for a repository using the weighted least-loaded placement algorithm.
func (vm *VolumeManager) SelectVolumeForRepo(repoID string, estSizeBytes int64) (*db.Volume, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	volumes, err := vm.db.ListVolumes()
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	var activeVolumes []db.Volume
	for _, v := range volumes {
		if v.Status == "active" {
			activeVolumes = append(activeVolumes, v)
		}
	}

	if len(activeVolumes) == 0 {
		return nil, fmt.Errorf("no active storage volumes configured")
	}

	if estSizeBytes <= 0 {
		estSizeBytes = 1024 * 1024 * 1024 // default 1GB
	}

	repoCounts, err := vm.db.GetRepoCountByVolume()
	if err != nil {
		repoCounts = make(map[string]int)
	}
	totalN := 0
	for _, count := range repoCounts {
		totalN += count
	}

	var bestVol *db.Volume
	bestScore := -1.0
	eligibleCount := 0

	for i := range activeVolumes {
		v := &activeVolumes[i]
		total, used, err := vm.statfs(v.MountPoint)
		if err == nil && total > 0 {
			v.TotalBytes = total
			v.UsedBytes = used
			_ = vm.db.UpdateVolumeUsage(v.ID, used, total)
		}

		if v.TotalBytes <= 0 {
			continue
		}

		// Hard Constraint: Utilization after placement must not exceed 80%
		futureUsedRatio := float64(v.UsedBytes+estSizeBytes) / float64(v.TotalBytes)
		if futureUsedRatio > 0.80 {
			continue
		}
		eligibleCount++

		busy := vm.activeTasks[v.ID]
		ratioN := 0.0
		if totalN > 0 {
			ratioN = float64(repoCounts[v.ID]) / float64(totalN)
		}

		// Placement Score formula:
		// Score = 0.50 * (1 - futureUsedRatio) + 0.30 * (1 / (1 + busy)) + 0.20 * (1 - ratioN)
		score := 0.50*(1.0-futureUsedRatio) + 0.30*(1.0/(1.0+float64(busy))) + 0.20*(1.0-ratioN)

		if score > bestScore {
			bestScore = score
			chosen := *v
			bestVol = &chosen
		}
	}

	if eligibleCount == 0 || bestVol == nil {
		return nil, fmt.Errorf("all storage volumes exceeded 80%% utilization threshold (target size: %d MB), expansion required", estSizeBytes/(1024*1024))
	}

	return bestVol, nil
}

// CreateRepoPlacement creates the physical directory on the selected volume and atomic symlink at linksBaseDir.
func (vm *VolumeManager) CreateRepoPlacement(repoID string, volumeID string) (physicalPath string, symlinkPath string, err error) {
	v, err := vm.db.GetVolume(volumeID)
	if err != nil || v == nil {
		return "", "", fmt.Errorf("volume %s not found: %w", volumeID, err)
	}

	physicalPath = filepath.Join(v.MountPoint, "repos", repoID)
	symlinkPath = filepath.Join(vm.linksBaseDir, repoID)

	if err := os.MkdirAll(physicalPath, 0755); err != nil {
		return "", "", fmt.Errorf("create physical repo dir %s: %w", physicalPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(symlinkPath), 0755); err != nil {
		return "", "", fmt.Errorf("create symlink parent dir %s: %w", filepath.Dir(symlinkPath), err)
	}

	// Remove existing symlink or old file
	_ = os.Remove(symlinkPath)

	if err := os.Symlink(physicalPath, symlinkPath); err != nil {
		return "", "", fmt.Errorf("create symlink %s -> %s: %w", symlinkPath, physicalPath, err)
	}

	return physicalPath, symlinkPath, nil
}

// UpdateRepoSize scans the physical repo directory, calculates total bytes, and updates the database.
func (vm *VolumeManager) UpdateRepoSize(repoID, physicalPath string) (int64, error) {
	var totalSize int64
	err := filepath.Walk(physicalPath, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	if err == nil {
		_ = vm.db.UpdateRepoVolumeAndSize(repoID, "", totalSize)
	}
	return totalSize, err
}
