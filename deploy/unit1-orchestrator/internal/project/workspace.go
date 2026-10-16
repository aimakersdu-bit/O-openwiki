package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
