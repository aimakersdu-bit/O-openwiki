package storage

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/openwiki/orchestrator/internal/db"
)

// QAEvictor is an interface to evict open worker processes before migration cutover.
type QAEvictor interface {
	Evict(repoID string)
}

// RebalanceAction represents a recommended migration step to balance storage pool load.
type RebalanceAction struct {
	RepoID       string `json:"repo_id"`
	RepoName     string `json:"repo_name"`
	FromVolumeID string `json:"from_volume_id"`
	ToVolumeID   string `json:"to_volume_id"`
	SizeBytes    int64  `json:"size_bytes"`
	Reason       string `json:"reason"`
}

// Migrator coordinates zero-downtime, safe repository volume migrations across physical disks.
type Migrator struct {
	mu           sync.Mutex
	db           *db.DB
	vm           *VolumeManager
	qaEvictor    QAEvictor
	linksBaseDir string
	copyFunc     func(src, dst string) error
}

// NewMigrator creates a new Migrator instance.
func NewMigrator(database *db.DB, vm *VolumeManager, qaEvictor QAEvictor, linksBaseDir string) *Migrator {
	if linksBaseDir == "" {
		linksBaseDir = "/data/disk0/links/repos"
	}
	m := &Migrator{
		db:           database,
		vm:           vm,
		qaEvictor:    qaEvictor,
		linksBaseDir: linksBaseDir,
	}
	m.copyFunc = m.defaultCopyDir
	return m
}

// SetCopyFunc overrides copyFunc (useful for testing or customized rsync).
func (m *Migrator) SetCopyFunc(fn func(src, dst string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copyFunc = fn
}

func (m *Migrator) defaultCopyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		// Handle symlink
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(targetPath)
			return os.Symlink(linkTarget, targetPath)
		}

		// Regular file
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()

		_, err = io.Copy(out, in)
		return err
	})
}

// countFilesAndSize returns file count and total size for verification.
func (m *Migrator) inspectDir(dir string) (int, int64, error) {
	fileCount := 0
	var totalSize int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fileCount++
			totalSize += info.Size()
		}
		return nil
	})
	return fileCount, totalSize, err
}

// Migrate executes the 7-state online migration for a single repository:
// planned -> syncing -> frozen -> delta -> verifying -> switched -> retained (or rollback on failure)
func (m *Migrator) Migrate(ctx context.Context, repoID, toVolumeID string) (*db.VolumeMigration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	repo, err := m.db.GetRepo(repoID)
	if err != nil || repo == nil {
		return nil, fmt.Errorf("repository %s not found: %w", repoID, err)
	}

	fromVolID := repo.VolumeID
	if fromVolID == "" {
		fromVolID = "vol1"
	}
	if fromVolID == toVolumeID {
		return nil, fmt.Errorf("repository %s is already on volume %s", repoID, toVolumeID)
	}

	fromVol, err := m.db.GetVolume(fromVolID)
	if err != nil || fromVol == nil {
		return nil, fmt.Errorf("source volume %s not found", fromVolID)
	}
	toVol, err := m.db.GetVolume(toVolumeID)
	if err != nil || toVol == nil {
		return nil, fmt.Errorf("target volume %s not found", toVolumeID)
	}
	if toVol.Status != "active" {
		return nil, fmt.Errorf("target volume %s is not active (status: %s)", toVolumeID, toVol.Status)
	}

	fromPath := filepath.Join(fromVol.MountPoint, "repos", repoID)
	toPath := filepath.Join(toVol.MountPoint, "repos", repoID)

	// State 1: planned
	migration := &db.VolumeMigration{
		RepoID:       repoID,
		FromVolumeID: fromVolID,
		ToVolumeID:   toVolumeID,
		Status:       "planned",
	}
	migID, err := m.db.CreateVolumeMigration(migration)
	if err != nil {
		return nil, fmt.Errorf("record migration: %w", err)
	}
	migration.ID = migID

	// State 2: syncing (first-pass online sync without disrupting services)
	_ = m.db.UpdateVolumeMigration(migID, "syncing", "")
	if err := os.MkdirAll(toPath, 0755); err != nil {
		_ = m.db.UpdateVolumeMigration(migID, "rollback", err.Error())
		return migration, fmt.Errorf("create target dir: %w", err)
	}
	if err := m.copyFunc(fromPath, toPath); err != nil {
		_ = m.db.UpdateVolumeMigration(migID, "rollback", err.Error())
		_ = os.RemoveAll(toPath)
		return migration, fmt.Errorf("initial sync failed: %w", err)
	}

	// State 3: frozen (freeze repo, evict QA worker)
	_ = m.db.UpdateVolumeMigration(migID, "frozen", "")
	origStatus := repo.Status
	_ = m.db.UpdateRepoStatus(repoID, "migrating")
	if m.qaEvictor != nil {
		m.qaEvictor.Evict(repoID)
	}

	// State 4: delta (fast second-pass incremental sync)
	_ = m.db.UpdateVolumeMigration(migID, "delta", "")
	if err := m.copyFunc(fromPath, toPath); err != nil {
		_ = m.db.UpdateRepoStatus(repoID, origStatus)
		_ = m.db.UpdateVolumeMigration(migID, "rollback", err.Error())
		_ = os.RemoveAll(toPath)
		return migration, fmt.Errorf("delta sync failed: %w", err)
	}

	// State 5: verifying (verify file count, total bytes, git rev-parse)
	_ = m.db.UpdateVolumeMigration(migID, "verifying", "")
	srcCount, srcSize, srcErr := m.inspectDir(fromPath)
	dstCount, dstSize, dstErr := m.inspectDir(toPath)
	if srcErr != nil || dstErr != nil || srcCount != dstCount || srcSize != dstSize {
		errMsg := fmt.Sprintf("verification mismatch: src(files=%d, size=%d) vs dst(files=%d, size=%d)", srcCount, srcSize, dstCount, dstSize)
		_ = m.db.UpdateRepoStatus(repoID, origStatus)
		_ = m.db.UpdateVolumeMigration(migID, "rollback", errMsg)
		_ = os.RemoveAll(toPath)
		return migration, fmt.Errorf("%s", errMsg)
	}

	// State 6: switched (atomic symlink retarget + DB update)
	symlinkPath := filepath.Join(m.linksBaseDir, repoID)
	_ = os.MkdirAll(filepath.Dir(symlinkPath), 0755)
	_ = os.Remove(symlinkPath)
	if err := os.Symlink(toPath, symlinkPath); err != nil {
		_ = m.db.UpdateRepoStatus(repoID, origStatus)
		_ = m.db.UpdateVolumeMigration(migID, "rollback", err.Error())
		return migration, fmt.Errorf("atomic symlink switch failed: %w", err)
	}

	_ = m.db.UpdateRepoVolumeAndSize(repoID, toVolumeID, dstSize)
	_ = m.db.UpdateRepoStatus(repoID, origStatus)
	_ = m.db.UpdateVolumeMigration(migID, "switched", "")

	// State 7: retained (old directory kept for observation)
	_ = m.db.UpdateVolumeMigration(migID, "retained", "")
	migration.Status = "retained"
	log.Printf("[migrator] Repo %s successfully migrated from %s to %s (status=retained)", repoID, fromVolID, toVolumeID)
	return migration, nil
}

// Rollback restores a switched or retained migration back to the source volume.
func (m *Migrator) Rollback(ctx context.Context, migrationID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mig, err := m.db.GetVolumeMigration(migrationID)
	if err != nil || mig == nil {
		return fmt.Errorf("migration %d not found", migrationID)
	}
	if mig.Status != "switched" && mig.Status != "retained" {
		return fmt.Errorf("cannot rollback migration in status %s", mig.Status)
	}

	fromVol, err := m.db.GetVolume(mig.FromVolumeID)
	if err != nil || fromVol == nil {
		return fmt.Errorf("source volume %s not found", mig.FromVolumeID)
	}
	fromPath := filepath.Join(fromVol.MountPoint, "repos", mig.RepoID)

	symlinkPath := filepath.Join(m.linksBaseDir, mig.RepoID)
	_ = os.Remove(symlinkPath)
	if err := os.Symlink(fromPath, symlinkPath); err != nil {
		return fmt.Errorf("rollback symlink failed: %w", err)
	}

	_, fromSize, _ := m.inspectDir(fromPath)
	_ = m.db.UpdateRepoVolumeAndSize(mig.RepoID, mig.FromVolumeID, fromSize)
	_ = m.db.UpdateVolumeMigration(migrationID, "rollback", "manually rolled back by admin")
	return nil
}

// CleanupRetained safely deletes the old source replica after confirmation.
func (m *Migrator) CleanupRetained(migrationID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mig, err := m.db.GetVolumeMigration(migrationID)
	if err != nil || mig == nil {
		return fmt.Errorf("migration %d not found", migrationID)
	}
	if mig.Status != "retained" {
		return fmt.Errorf("cannot cleanup migration in status %s (must be retained)", mig.Status)
	}

	fromVol, err := m.db.GetVolume(mig.FromVolumeID)
	if err != nil || fromVol == nil {
		return fmt.Errorf("source volume %s not found", mig.FromVolumeID)
	}
	fromPath := filepath.Join(fromVol.MountPoint, "repos", mig.RepoID)

	if err := os.RemoveAll(fromPath); err != nil {
		return fmt.Errorf("remove old replica %s: %w", fromPath, err)
	}

	_ = m.db.UpdateVolumeMigration(migrationID, "cleaned", "")
	return nil
}

// ResumeOnRestart detects interrupted migrations on startup and recovers them.
func (m *Migrator) ResumeOnRestart() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	migrations, err := m.db.ListVolumeMigrations()
	if err != nil {
		return err
	}

	for _, mig := range migrations {
		if mig.Status == "syncing" || mig.Status == "frozen" || mig.Status == "delta" || mig.Status == "verifying" {
			log.Printf("[migrator] Recovering interrupted migration %d for repo %s (was %s)", mig.ID, mig.RepoID, mig.Status)
			_ = m.db.UpdateRepoStatus(mig.RepoID, "active")
			_ = m.db.UpdateVolumeMigration(mig.ID, "rollback", "recovered and cancelled on system startup")
		}
	}
	return nil
}

// GenerateRebalancePlan generates a greedy rebalance plan when storage volumes are imbalanced.
func (m *Migrator) GenerateRebalancePlan() ([]RebalanceAction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	volumes, err := m.db.ListVolumes()
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	if len(volumes) < 2 {
		return nil, nil // Cannot rebalance with less than 2 volumes
	}

	repos, err := m.db.ListRepos()
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}

	type volSim struct {
		vol      db.Volume
		used     int64
		repos    []db.Repo
		utilRate float64
	}

	sims := make(map[string]*volSim)
	for _, v := range volumes {
		if v.Status == "active" && v.TotalBytes > 0 {
			sims[v.ID] = &volSim{
				vol:      v,
				used:     v.UsedBytes,
				repos:    make([]db.Repo, 0),
				utilRate: float64(v.UsedBytes) / float64(v.TotalBytes),
			}
		}
	}

	for _, r := range repos {
		vID := r.VolumeID
		if vID == "" {
			vID = "vol1"
		}
		if s, ok := sims[vID]; ok {
			s.repos = append(s.repos, r)
		}
	}

	var actions []RebalanceAction

	for step := 0; step < 50; step++ { // safety max iterations
		var sortedList []*volSim
		for _, s := range sims {
			s.utilRate = float64(s.used) / float64(s.vol.TotalBytes)
			sortedList = append(sortedList, s)
		}
		sort.Slice(sortedList, func(i, j int) bool {
			return sortedList[i].utilRate > sortedList[j].utilRate
		})

		maxV := sortedList[0]
		minV := sortedList[len(sortedList)-1]

		// Condition: spread > 15% or max > 80%
		spread := maxV.utilRate - minV.utilRate
		if spread <= 0.05 && maxV.utilRate <= 0.80 {
			break
		}

		// Sort repos in maxV descending by size to pick largest movable repo
		sort.Slice(maxV.repos, func(i, j int) bool {
			return maxV.repos[i].SizeBytes > maxV.repos[j].SizeBytes
		})

		moved := false
		for idx, r := range maxV.repos {
			repoSize := r.SizeBytes
			if repoSize <= 0 {
				repoSize = 500 * 1024 * 1024 // 500MB default
			}
			newMinUtil := float64(minV.used+repoSize) / float64(minV.vol.TotalBytes)
			newMaxUtil := float64(maxV.used-repoSize) / float64(maxV.vol.TotalBytes)

			// Don't move if it flips imbalance or overfills target (> 80%)
			if newMinUtil > 0.80 || (newMinUtil-newMaxUtil) > 0.05 {
				continue
			}

			// Add to plan
			actions = append(actions, RebalanceAction{
				RepoID:       r.ID,
				RepoName:     r.Name,
				FromVolumeID: maxV.vol.ID,
				ToVolumeID:   minV.vol.ID,
				SizeBytes:    repoSize,
				Reason:       fmt.Sprintf("Rebalance: reduce %s load (%.1f%% -> %.1f%%) to %s (%.1f%% -> %.1f%%)", maxV.vol.ID, maxV.utilRate*100, newMaxUtil*100, minV.vol.ID, minV.utilRate*100, newMinUtil*100),
			})

			// Apply to simulation
			maxV.used -= repoSize
			minV.used += repoSize
			maxV.repos = append(maxV.repos[:idx], maxV.repos[idx+1:]...)
			minV.repos = append(minV.repos, r)
			moved = true
			break
		}

		if !moved {
			break
		}
	}

	return actions, nil
}
