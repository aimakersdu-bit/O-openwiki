package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/project"
)

type ProjectRepoInput struct {
	RepoID    string `json:"repo_id"`
	AliasName string `json:"alias_name"`
	Role      string `json:"role"`
}

type CreateProjectRequest struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	BusinessDomain string             `json:"business_domain"`
	ArchitectureMD string             `json:"architecture_md"`
	LocalPath      string             `json:"local_path,omitempty"`
	Repos          []ProjectRepoInput `json:"repos,omitempty"`
}

type ProjectResponse struct {
	db.Project
	Repos []db.RepoWithAlias `json:"repos"`
}

type TriggerProjectBuildRequest struct {
	ProjectID string `json:"project_id"`
}

// handleProjects routes GET, POST, PUT, DELETE for /api/projects
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listProjects(w, r)
	case http.MethodPost:
		s.createProject(w, r)
	case http.MethodPut:
		s.updateProject(w, r)
	case http.MethodDelete:
		s.deleteProject(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// listProjects returns all registered projects along with their child repos.
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	authCtx := GetAuthContext(r)
	projects, err := db.ListProjects()
	if err != nil {
		http.Error(w, "Failed to query projects: "+err.Error(), http.StatusInternalServerError)
		return
	}

	res := make([]ProjectResponse, 0, len(projects))
	for _, p := range projects {
		if authCtx.Role == "group_leader" && !authCtx.IsDomainAuthorized(p.BusinessDomain) {
			continue
		}
		repos, err := db.GetProjectRepos(p.ID)
		if err != nil {
			repos = []db.RepoWithAlias{}
		}
		res = append(res, ProjectResponse{
			Project: p,
			Repos:   repos,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// createProject registers a new project workspace in SQLite.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	if req.ID == "" || req.Name == "" {
		http.Error(w, "Project id and name are required", http.StatusBadRequest)
		return
	}

	authCtx := GetAuthContext(r)
	if authCtx.Role == "group_leader" {
		if req.BusinessDomain == "" || !authCtx.IsDomainAuthorized(req.BusinessDomain) {
			http.Error(w, fmt.Sprintf("Forbidden: not authorized for business domain '%s'", req.BusinessDomain), http.StatusForbidden)
			return
		}
		for _, repoInput := range req.Repos {
			repo, err := db.GetRepo(repoInput.RepoID)
			if err != nil || repo == nil {
				http.Error(w, fmt.Sprintf("Repository '%s' not found", repoInput.RepoID), http.StatusBadRequest)
				return
			}
			if repo.BusinessDomain != "" && !authCtx.IsDomainAuthorized(repo.BusinessDomain) {
				http.Error(w, fmt.Sprintf("Forbidden: repository '%s' belongs to domain '%s' which is outside your authorized domains", repo.ID, repo.BusinessDomain), http.StatusForbidden)
				return
			}
		}
	} else if authCtx.Role != "super_admin" && authCtx.Role != "admin" {
		http.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		return
	}

	localPath := req.LocalPath
	if localPath == "" {
		localPath = filepath.Join(s.config.ProjectsBaseDir, req.ID)
	}

	proj := &db.Project{
		ID:             req.ID,
		Name:           req.Name,
		Description:    req.Description,
		BusinessDomain: req.BusinessDomain,
		ArchitectureMD: req.ArchitectureMD,
		LocalPath:      localPath,
		Status:         "active",
	}

	if err := db.CreateProject(proj); err != nil {
		http.Error(w, "Failed to create project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Save repo mappings if provided
	if len(req.Repos) > 0 {
		mappings := make([]db.ProjectRepo, 0, len(req.Repos))
		for _, rm := range req.Repos {
			mappings = append(mappings, db.ProjectRepo{
				ProjectID: req.ID,
				RepoID:    rm.RepoID,
				AliasName: rm.AliasName,
				Role:      rm.Role,
			})
		}
		if err := db.SetProjectRepos(req.ID, mappings); err != nil {
			http.Error(w, "Failed to map project repos: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Prepare virtual workspace directories & symlinks
	repos, _ := db.GetProjectRepos(req.ID)
	wsMgr := project.NewWorkspaceManager(s.config.ProjectsBaseDir)
	_, _ = wsMgr.PrepareWorkspace(proj, repos)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(proj)
}

// updateProject updates an existing project and replaces its repo mappings.
func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		http.Error(w, "Project id is required", http.StatusBadRequest)
		return
	}

	existing, err := db.GetProject(req.ID)
	if err != nil || existing == nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	authCtx := GetAuthContext(r)
	if authCtx.Role == "group_leader" {
		if !authCtx.IsDomainAuthorized(existing.BusinessDomain) {
			http.Error(w, fmt.Sprintf("Forbidden: cannot modify project in domain '%s'", existing.BusinessDomain), http.StatusForbidden)
			return
		}
		if req.BusinessDomain != "" && !authCtx.IsDomainAuthorized(req.BusinessDomain) {
			http.Error(w, fmt.Sprintf("Forbidden: cannot change project to unauthorized domain '%s'", req.BusinessDomain), http.StatusForbidden)
			return
		}
		for _, repoInput := range req.Repos {
			repo, err := db.GetRepo(repoInput.RepoID)
			if err != nil || repo == nil {
				http.Error(w, fmt.Sprintf("Repository '%s' not found", repoInput.RepoID), http.StatusBadRequest)
				return
			}
			if repo.BusinessDomain != "" && !authCtx.IsDomainAuthorized(repo.BusinessDomain) {
				http.Error(w, fmt.Sprintf("Forbidden: repository '%s' belongs to domain '%s' which is outside your authorized domains", repo.ID, repo.BusinessDomain), http.StatusForbidden)
				return
			}
		}
	} else if authCtx.Role != "super_admin" && authCtx.Role != "admin" {
		http.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		return
	}

	if req.Name != "" {
		existing.Name = req.Name
	}
	existing.Description = req.Description
	if req.BusinessDomain != "" {
		existing.BusinessDomain = req.BusinessDomain
	}
	existing.ArchitectureMD = req.ArchitectureMD
	if req.LocalPath != "" {
		existing.LocalPath = req.LocalPath
	}

	if err := db.UpdateProject(existing); err != nil {
		http.Error(w, "Failed to update project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Repos != nil {
		mappings := make([]db.ProjectRepo, 0, len(req.Repos))
		for _, rm := range req.Repos {
			mappings = append(mappings, db.ProjectRepo{
				ProjectID: req.ID,
				RepoID:    rm.RepoID,
				AliasName: rm.AliasName,
				Role:      rm.Role,
			})
		}
		if err := db.SetProjectRepos(req.ID, mappings); err != nil {
			http.Error(w, "Failed to update repo mappings: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	repos, _ := db.GetProjectRepos(req.ID)
	wsMgr := project.NewWorkspaceManager(s.config.ProjectsBaseDir)
	_, _ = wsMgr.PrepareWorkspace(existing, repos)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(existing)
}

// deleteProject deletes a project and cascades repo mappings.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Project id query parameter is required", http.StatusBadRequest)
		return
	}

	existing, err := db.GetProject(id)
	if err != nil || existing == nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	authCtx := GetAuthContext(r)
	if authCtx.Role == "group_leader" {
		if !authCtx.IsDomainAuthorized(existing.BusinessDomain) {
			http.Error(w, fmt.Sprintf("Forbidden: cannot delete project in domain '%s'", existing.BusinessDomain), http.StatusForbidden)
			return
		}
	} else if authCtx.Role != "super_admin" && authCtx.Role != "admin" {
		http.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		return
	}

	if err := db.DeleteProject(id); err != nil {
		http.Error(w, "Failed to delete project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"deleted"}`))
}

// handleProjectBuild triggers the virtual workspace preparation, linker scan, and summary generation.
func (s *Server) handleProjectBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TriggerProjectBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}

	proj, err := db.GetProject(req.ProjectID)
	if err != nil || proj == nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	authCtx := GetAuthContext(r)
	if authCtx.Role == "group_leader" {
		if !authCtx.IsDomainAuthorized(proj.BusinessDomain) {
			http.Error(w, fmt.Sprintf("Forbidden: cannot trigger build for project in domain '%s'", proj.BusinessDomain), http.StatusForbidden)
			return
		}
	} else if authCtx.Role != "super_admin" && authCtx.Role != "admin" {
		http.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		return
	}

	go s.BuildProjectWorkspace(req.ProjectID)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"building","message":"Project build triggered"}`))
}

// BuildProjectWorkspace runs workspace preparation, linker scan and quickstart aggregation.
func (s *Server) BuildProjectWorkspace(projectID string) error {
	proj, err := db.GetProject(projectID)
	if err != nil || proj == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	proj.Status = "building"
	_ = db.UpdateProject(proj)

	repos, err := db.GetProjectRepos(projectID)
	if err != nil {
		proj.Status = "failed"
		_ = db.UpdateProject(proj)
		return err
	}

	wsMgr := project.NewWorkspaceManager(s.config.ProjectsBaseDir)
	wsDir, err := wsMgr.PrepareWorkspace(proj, repos)
	if err != nil {
		proj.Status = "failed"
		_ = db.UpdateProject(proj)
		return fmt.Errorf("prepare workspace: %w", err)
	}

	// Run linker.js
	linkerCandidates := []string{
		filepath.Join("scripts", "linker.js"),
		filepath.Join("deploy", "unit1-orchestrator", "scripts", "linker.js"),
		filepath.Join("..", "unit1-orchestrator", "scripts", "linker.js"),
		"/app/scripts/linker.js",
	}

	var linkerScript string
	for _, c := range linkerCandidates {
		if _, err := os.Stat(c); err == nil {
			linkerScript = c
			break
		}
	}

	if linkerScript != "" {
		cmd := exec.Command("node", linkerScript, wsDir)
		_ = cmd.Run()
	}

	// Generate _project_hub/openwiki/quickstart.md
	hubOpenwikiDir := filepath.Join(wsDir, "_project_hub", "openwiki")
	_ = os.MkdirAll(hubOpenwikiDir, 0755)

	quickstartContent := fmt.Sprintf(`# %s 微服务系统架构总览

## 1. 项目简介
%s

## 2. 包含微服务组件清单 (%d 个子系统)

| 服务名称 | 仓库角色 | 本地路径 | 单仓文档 |
| :--- | :--- | :--- | :--- |
`, proj.Name, proj.Description, len(repos))

	for _, r := range repos {
		wikiLink := fmt.Sprintf("/wiki/%s/", r.ID)
		quickstartContent += fmt.Sprintf("| **%s** (`%s`) | `%s` | `%s` | [查看 Wiki](%s) |\n",
			r.Name, r.AliasName, r.Role, r.LocalPath, wikiLink)
	}

	quickstartContent += `
## 3. 全局服务拓扑与调用链
请点击左侧导航栏中的 **[微服务调用与事件依赖拓扑 (Service Topology)](./architecture/topology.md)** 查看端到端链路。
`

	_ = os.WriteFile(filepath.Join(hubOpenwikiDir, "quickstart.md"), []byte(quickstartContent), 0644)

	// === P2.3: Write architecture.md (domain-maintained architecture description) ===
	archPath := filepath.Join(hubOpenwikiDir, "architecture.md")
	if strings.TrimSpace(proj.ArchitectureMD) != "" {
		_ = os.WriteFile(archPath, []byte(proj.ArchitectureMD), 0644)
	} else {
		tmpl := fmt.Sprintf("# %s 架构描述\n\n> 请域管理员维护此文档，描述各底层仓库的作用与调用关系。\n\n## 服务职责\n\n## 调用关系\n", proj.Name)
		_ = os.WriteFile(archPath, []byte(tmpl), 0644)
	}

	// === P2.4: Multi-repo full openwiki build (skip Git; sub-repos have independent builds) ===
	if s.scheduler != nil && s.scheduler.GetBuilder() != nil {
		builder := s.scheduler.GetBuilder()
		wikiDir := hubOpenwikiDir
		buildID, _ := db.CreateBuild(projectID, "", "running")
		log.Printf("[project-build][%s] starting multi-repo openwiki build (buildID=%d, wsDir=%s)", projectID, buildID, wsDir)

		repoTarget := filepath.Join("projects", projectID)
		buildRes := builder.BuildRepoWithBuildID(buildID, repoTarget, wsDir, wikiDir,
			func(currentLog string) {
				_ = db.GetDB().UpdateBuildLog(buildID, currentLog, "")
			})

		buildStatus := "completed"
		if !buildRes.Success {
			buildStatus = "failed"
		}

		// Token accounting via LLMProxy
		var prompt, completion, total, saved int64
		var retries int
		if s.llmProxy != nil {
			if usage, ok := s.llmProxy.PopBuildUsage(fmt.Sprintf("%d", buildID)); ok {
				prompt = int64(usage.PromptTokens)
				completion = int64(usage.CompletionTokens)
				total = int64(usage.TotalTokens)
				retries = usage.RateLimitedRetries
			}
		}
		_ = db.FinishBuildWithTokens(buildID, buildStatus, buildRes.Log, buildRes.Error,
			prompt, completion, total, saved, retries)

		if buildRes.Success {
			log.Printf("[project-build][%s] multi-repo build completed successfully", projectID)
		} else {
			log.Printf("[project-build][%s] multi-repo build FAILED: %s", projectID, buildRes.Error)
			proj.Status = "failed"
			_ = db.UpdateProject(proj)
			return fmt.Errorf("multi-repo openwiki build failed: %s", buildRes.Error)
		}
	}

	// Ensure visualizer assets (index.html, client.js, client-lib.js, vendor) and api/graph are deployed into StaticOutputDir/projects/<projectID>
	if s.config.StaticOutputDir != "" {
		staticProjectDir := filepath.Join(s.config.StaticOutputDir, "projects", proj.ID)
		_ = os.MkdirAll(staticProjectDir, 0755)
		if s.scheduler != nil && s.scheduler.GetBuilder() != nil {
			builder := s.scheduler.GetBuilder()
			_ = builder.DeployVisualizer(hubOpenwikiDir, staticProjectDir)
		}
	}

	proj.Status = "active"
	return db.UpdateProject(proj)
}

// handleProjectSubroutes routes /api/projects/build and /api/projects/{id}/links/*
func (s *Server) handleProjectSubroutes(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/api/projects/")
	if relPath == "build" {
		s.handleProjectBuild(w, r)
		return
	}

	parts := strings.Split(relPath, "/")
	// Expected: {id}/links/{action}
	if len(parts) >= 3 && parts[1] == "links" {
		projectID := parts[0]
		action := parts[2]
		s.handleProjectLinksAction(w, r, projectID, action)
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleProjectLinksAction(w http.ResponseWriter, r *http.Request, projectID, action string) {
	proj, err := db.GetProject(projectID)
	if err != nil || proj == nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	authCtx := GetAuthContext(r)
	if authCtx.Role == "group_leader" {
		if !authCtx.IsDomainAuthorized(proj.BusinessDomain) {
			http.Error(w, fmt.Sprintf("Forbidden: cannot inspect project in domain '%s'", proj.BusinessDomain), http.StatusForbidden)
			return
		}
	} else if authCtx.Role != "super_admin" && authCtx.Role != "admin" {
		http.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
		return
	}

	repos, err := db.GetProjectRepos(projectID)
	if err != nil {
		http.Error(w, "Failed to query project repos: "+err.Error(), http.StatusInternalServerError)
		return
	}

	wsMgr := project.NewWorkspaceManager(s.config.ProjectsBaseDir)

	var preview *project.WorkspaceLinkPreview
	switch action {
	case "preview":
		preview, err = wsMgr.PreviewWorkspaceLinks(proj, repos)
	case "health":
		preview, err = wsMgr.CheckWorkspaceHealth(proj, repos)
	case "repair":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed, POST required for repair", http.StatusMethodNotAllowed)
			return
		}
		preview, err = wsMgr.RepairWorkspaceLinks(proj, repos)
	default:
		http.NotFound(w, r)
		return
	}

	if err != nil {
		http.Error(w, "Failed to perform links "+action+": "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(preview)
}
