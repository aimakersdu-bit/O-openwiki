package project

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

// ProjectRepoMeta describes a child repo in .project-meta.json
type ProjectRepoMeta struct {
	RepoID    string `json:"repo_id"`
	RepoName  string `json:"repo_name"`
	AliasName string `json:"alias_name"`
	Role      string `json:"role"`
	LocalPath string `json:"local_path"`
}

// ProjectMeta describes the structure persisted to .project-meta.json
type ProjectMeta struct {
	ProjectID   string            `json:"project_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	UpdatedAt   string            `json:"updated_at"`
	Repos       []ProjectRepoMeta `json:"repos"`
}

// WorkspaceManager manages virtual monorepo project workspaces.
type WorkspaceManager struct {
	BaseProjectsDir string
}

// NewWorkspaceManager creates a new WorkspaceManager with base storage directory.
func NewWorkspaceManager(baseProjectsDir string) *WorkspaceManager {
	if baseProjectsDir == "" {
		baseProjectsDir = "/data/openwiki/projects"
	}
	return &WorkspaceManager{
		BaseProjectsDir: baseProjectsDir,
	}
}

// sanitizeAlias normalizes a repo alias so it is safe to use as a single filesystem path
// segment. Repo IDs often use gitlab group/project notation (e.g. "aimakersdu-bit/health")
// which would create a nested directory instead of a flat symlink. Path separators and other
// characters illegal in a single path segment are replaced with "__".
func sanitizeAlias(alias string) string {
	if alias == "" {
		return alias
	}
	// Replace OS path separators (already joined by filepath use "/" on linux)
	alias = strings.ReplaceAll(alias, "/", "__")
	alias = strings.ReplaceAll(alias, "\\", "__")
	return alias
}

// PrepareWorkspace sets up or updates the virtual project workspace directory and symlinks.
func (m *WorkspaceManager) PrepareWorkspace(project *db.Project, repos []db.RepoWithAlias) (string, error) {
	wsDir := project.LocalPath
	if wsDir == "" {
		wsDir = filepath.Join(m.BaseProjectsDir, project.ID)
	}

	// 1. Ensure project root directory exists
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		return "", fmt.Errorf("create workspace dir %s: %w", wsDir, err)
	}

	// Ensure workspace is a git repository so openwiki CLI can compute source fingerprint
	gitDir := filepath.Join(wsDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		cmdInit := exec.Command("git", "init")
		cmdInit.Dir = wsDir
		_ = cmdInit.Run()
	}

	// 2. Ensure _project_hub structure exists
	hubDir := filepath.Join(wsDir, "_project_hub")
	openwikiHubDir := filepath.Join(hubDir, "openwiki")
	contractsHubDir := filepath.Join(hubDir, "contracts")
	if err := os.MkdirAll(openwikiHubDir, 0755); err != nil {
		return "", fmt.Errorf("create hub openwiki dir: %w", err)
	}
	if err := os.MkdirAll(contractsHubDir, 0755); err != nil {
		return "", fmt.Errorf("create hub contracts dir: %w", err)
	}

	// 3. Track active aliases to clean up obsolete symlinks later
	activeAliases := make(map[string]bool)

	// 4. Create or update symlinks for each child repo
	for _, r := range repos {
		alias := r.AliasName
		if alias == "" {
			alias = r.ID
		}
		alias = sanitizeAlias(alias)
		activeAliases[alias] = true

		linkPath := filepath.Join(wsDir, alias)
		sourcePath := r.LocalPath

		// Check if target link/file already exists
		lstat, err := os.Lstat(linkPath)
		if err == nil {
			// Path exists
			if lstat.Mode()&os.ModeSymlink != 0 {
				currentDest, readErr := os.Readlink(linkPath)
				if readErr == nil && currentDest == sourcePath {
					// Symlink is already pointing to the correct destination
					continue
				}
			}
			// Destination changed or not a symlink; remove it first
			if remErr := os.Remove(linkPath); remErr != nil {
				return "", fmt.Errorf("remove stale link %s: %w", linkPath, remErr)
			}
		}

		// Create symlink
		if err := os.Symlink(sourcePath, linkPath); err != nil {
			return "", fmt.Errorf("create symlink %s -> %s: %w", linkPath, sourcePath, err)
		}
	}

	// 5. Clean up orphan symlinks
	entries, err := os.ReadDir(wsDir)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if name == "_project_hub" || name == ".project-meta.json" || name == ".openwiki" {
				continue
			}

			// Check if it is an orphan symlink
			entryPath := filepath.Join(wsDir, name)
			if lstat, err := os.Lstat(entryPath); err == nil && (lstat.Mode()&os.ModeSymlink != 0) {
				if !activeAliases[name] {
					_ = os.Remove(entryPath)
				}
			}
		}
	}

	// 6. Write or update .project-meta.json
	metaRepos := make([]ProjectRepoMeta, 0, len(repos))
	for _, r := range repos {
		alias := r.AliasName
		if alias == "" {
			alias = r.ID
		}
		alias = sanitizeAlias(alias)
		role := r.Role
		if role == "" {
			role = "service"
		}
		metaRepos = append(metaRepos, ProjectRepoMeta{
			RepoID:    r.ID,
			RepoName:  r.Name,
			AliasName: alias,
			Role:      role,
			LocalPath: r.LocalPath,
		})
	}

	meta := ProjectMeta{
		ProjectID:   project.ID,
		Name:        project.Name,
		Description: project.Description,
		UpdatedAt:   time.Now().Local().Format("2006-01-02 15:04:05"),
		Repos:       metaRepos,
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal project meta: %w", err)
	}

	metaFile := filepath.Join(wsDir, ".project-meta.json")
	if err := os.WriteFile(metaFile, metaBytes, 0644); err != nil {
		return "", fmt.Errorf("write project meta %s: %w", metaFile, err)
	}

	return wsDir, nil
}

// RepoLinkItem describes a child repository's logical symlink and physical disk location.
type RepoLinkItem struct {
	RepoID       string  `json:"repo_id"`
	RepoName     string  `json:"repo_name"`
	AliasName    string  `json:"alias_name"`
	LogicalPath  string  `json:"logical_path"`
	TargetLink   string  `json:"target_link"`
	PhysicalPath string  `json:"physical_path"`
	VolumeID     string  `json:"volume_id"`
	Status       string  `json:"status"` // "ok", "broken", "unlinked", "not_a_symlink"
	SizeBytes    int64   `json:"size_bytes"`
	SizeMB       float64 `json:"size_mb"`
}

// WorkspaceLinkPreview summarizes multi-disk symlink distribution and health status.
type WorkspaceLinkPreview struct {
	ProjectID    string         `json:"project_id"`
	ProjectName  string         `json:"project_name"`
	LocalPath    string         `json:"local_path"`
	TotalRepos   int            `json:"total_repos"`
	HealthyCount int            `json:"healthy_count"`
	BrokenCount  int            `json:"broken_count"`
	VolumeCounts map[string]int `json:"volume_counts"`
	Items        []RepoLinkItem `json:"items"`
}

// CheckWorkspaceHealth inspects each child repository symlink in the project workspace.
func (m *WorkspaceManager) CheckWorkspaceHealth(project *db.Project, repos []db.RepoWithAlias) (*WorkspaceLinkPreview, error) {
	wsDir := project.LocalPath
	if wsDir == "" {
		wsDir = filepath.Join(m.BaseProjectsDir, project.ID)
	}

	preview := &WorkspaceLinkPreview{
		ProjectID:    project.ID,
		ProjectName:  project.Name,
		LocalPath:    wsDir,
		TotalRepos:   len(repos),
		VolumeCounts: make(map[string]int),
		Items:        make([]RepoLinkItem, 0, len(repos)),
	}

	for _, r := range repos {
		alias := r.AliasName
		if alias == "" {
			alias = r.ID
		}
		alias = sanitizeAlias(alias)
		logicalPath := filepath.Join(wsDir, alias)
		status := "ok"
		targetLink := r.LocalPath

		lstat, err := os.Lstat(logicalPath)
		if err != nil {
			status = "unlinked"
		} else if lstat.Mode()&os.ModeSymlink == 0 {
			status = "not_a_symlink"
		} else {
			if linkTarget, readErr := os.Readlink(logicalPath); readErr == nil {
				targetLink = linkTarget
			}
			if _, statErr := os.Stat(logicalPath); statErr != nil {
				status = "broken"
			}
		}

		physicalPath, evalErr := filepath.EvalSymlinks(targetLink)
		if evalErr != nil {
			physicalPath = targetLink
		}

		volID := r.VolumeID
		if volID == "" {
			// Extract vol from physical path if present, e.g. /data/vol2/repos/...
			parts := strings.Split(physicalPath, string(filepath.Separator))
			for _, p := range parts {
				if strings.HasPrefix(p, "vol") {
					volID = p
					break
				}
			}
			if volID == "" {
				volID = "disk0"
			}
		}

		if status == "ok" {
			preview.HealthyCount++
		} else {
			preview.BrokenCount++
		}
		preview.VolumeCounts[volID]++

		preview.Items = append(preview.Items, RepoLinkItem{
			RepoID:       r.ID,
			RepoName:     r.Name,
			AliasName:    alias,
			LogicalPath:  logicalPath,
			TargetLink:   targetLink,
			PhysicalPath: physicalPath,
			VolumeID:     volID,
			Status:       status,
			SizeBytes:    r.SizeBytes,
			SizeMB:       float64(r.SizeBytes) / (1024 * 1024),
		})
	}

	return preview, nil
}

// PreviewWorkspaceLinks calculates link targets and storage distribution without requiring existing links.
func (m *WorkspaceManager) PreviewWorkspaceLinks(project *db.Project, repos []db.RepoWithAlias) (*WorkspaceLinkPreview, error) {
	return m.CheckWorkspaceHealth(project, repos)
}

// RepairWorkspaceLinks recreates the symlink forest and returns refreshed health status.
func (m *WorkspaceManager) RepairWorkspaceLinks(project *db.Project, repos []db.RepoWithAlias) (*WorkspaceLinkPreview, error) {
	if _, err := m.PrepareWorkspace(project, repos); err != nil {
		return nil, fmt.Errorf("prepare workspace: %w", err)
	}
	return m.CheckWorkspaceHealth(project, repos)
}
