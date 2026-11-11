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
	TriCode        string             `json:"tri_code,omitempty"`
	Description    string             `json:"description"`
	BusinessDomain string             `json:"business_domain"`
	ArchitectureMD string             `json:"architecture_md"`
	LocalPath      string             `json:"local_path,omitempty"`
	Repos          []ProjectRepoInput `json:"repos,omitempty"`
}

type ProjectResponse struct {
	db.Project
	QueryKey    string             `json:"query_key"`
	TaskStatus  string             `json:"task_status"`
	LatestBuild *db.Build          `json:"latest_build,omitempty"`
	Repos       []db.RepoWithAlias `json:"repos"`
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

func deriveProjectTaskStatus(proj db.Project, latestBuild *db.Build) string {
	switch proj.Status {
	case "queued":
		return "queued"
	case "building":
		return "running"
	}
	if latestBuild != nil {
		switch latestBuild.Status {
		case "running":
			return "running"
		case "completed", "success":
			return "completed"
		case "failed":
			return "failed"
		case "interrupted":
			return "interrupted"
		}
	}
	if proj.Status == "failed" {
		return "failed"
	}
	return "never_built"
}

// listProjects returns all registered projects along with their child repos and latest task status.
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
		latestBuild, _ := db.GetLatestBuild(p.ID)
		var buildSummary *db.Build
		if latestBuild != nil {
			bCopy := *latestBuild
			bCopy.Log = "" // Fetch full logs lazily via /api/builds?repo_id=<project_id>
			buildSummary = &bCopy
		}
		queryKey := p.ID
		if strings.TrimSpace(p.TriCode) != "" {
			queryKey = strings.TrimSpace(p.TriCode)
		}
		res = append(res, ProjectResponse{
			Project:     p,
			QueryKey:    queryKey,
			TaskStatus:  deriveProjectTaskStatus(p, latestBuild),
			LatestBuild: buildSummary,
			Repos:       repos,
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

	triCode := strings.ToUpper(strings.TrimSpace(req.TriCode))
	if triCode == "" {
		var items []BatchImportRepoItem
		for _, rm := range req.Repos {
			items = append(items, BatchImportRepoItem{ID: rm.RepoID})
		}
		triCode = extractTriCode(req.BusinessDomain, items)
	}

	proj := &db.Project{
		ID:             req.ID,
		Name:           req.Name,
		TriCode:        triCode,
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
	if triCode := strings.ToUpper(strings.TrimSpace(req.TriCode)); triCode != "" {
		existing.TriCode = triCode
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

	proj.Status = "building"
	_ = db.UpdateProject(proj)
	go s.BuildProjectWorkspace(req.ProjectID)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"building","message":"Project build triggered"}`))
}

// enqueueProjectBuilds marks projects as queued and builds them sequentially
// so batch completions do not flood SQLite or LLM limits concurrently.
func (s *Server) enqueueProjectBuilds(projectIDs []string) {
	if len(projectIDs) == 0 {
		return
	}
	database := s.getDB()
	if database == nil {
		return
	}

	s.projQueueMu.Lock()
	if s.projQueuedSet == nil {
		s.projQueuedSet = make(map[string]bool)
	}
	added := 0
	for _, pid := range projectIDs {
		pid = strings.TrimSpace(pid)
		if pid == "" || s.projQueuedSet[pid] {
			continue
		}
		if proj, err := database.GetProject(pid); err == nil && proj != nil {
			if proj.Status != "building" {
				proj.Status = "queued"
				_ = database.UpdateProject(proj)
			}
			s.projQueuedSet[pid] = true
			s.projQueue = append(s.projQueue, pid)
			added++
		}
	}
	shouldStart := added > 0 && !s.projWorkerRunning
	if shouldStart {
		s.projWorkerRunning = true
	}
	s.projQueueMu.Unlock()

	if !shouldStart {
		return
	}

	go func() {
		for {
			s.projQueueMu.Lock()
			if len(s.projQueue) == 0 {
				s.projWorkerRunning = false
				s.projQueueMu.Unlock()
				return
			}
			nextID := s.projQueue[0]
			s.projQueue = s.projQueue[1:]
			delete(s.projQueuedSet, nextID)
			s.projQueueMu.Unlock()

			if err := s.BuildProjectWorkspace(nextID); err != nil {
				log.Printf("[project-build][%s] queued batch aggregation failed: %v", nextID, err)
			}
		}
	}()
}

// handleBatchCompletedProjects automatically triggers builds for microservice projects
// linked to repositories whose batch tasks finished with success or skipped.
func (s *Server) handleBatchCompletedProjects(batchID string, tasks []db.BatchTask) {
	database := s.getDB()
	if database == nil || len(tasks) == 0 {
		return
	}
	var readyRepoIDs []string
	for _, t := range tasks {
		if t.Status == "success" || t.Status == "skipped" {
			readyRepoIDs = append(readyRepoIDs, t.RepoID)
		}
	}
	if len(readyRepoIDs) == 0 {
		return
	}
	projectIDs, err := database.GetProjectIDsByRepoIDs(readyRepoIDs)
	if err != nil || len(projectIDs) == 0 {
		return
	}
	log.Printf("[batch_scheduler][%s] batch finished (%d ready repos), auto-queuing %d microservice projects for aggregation build: %v",
		batchID, len(readyRepoIDs), len(projectIDs), projectIDs)
	s.enqueueProjectBuilds(projectIDs)
}

// BuildProjectWorkspace runs workspace preparation, linker scan and quickstart aggregation.
func (s *Server) BuildProjectWorkspace(projectID string) error {
	database := s.getDB()
	if database == nil {
		return fmt.Errorf("database unavailable")
	}

	proj, err := database.GetProject(projectID)
	if err != nil || proj == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	proj.Status = "building"
	_ = database.UpdateProject(proj)

	buildID, _ := database.CreateBuild(projectID, "", "running")
	var logBuf strings.Builder
	appendStep := func(line string) {
		logBuf.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			logBuf.WriteByte('\n')
		}
		if buildID > 0 {
			_ = database.UpdateBuildLog(buildID, logBuf.String(), "")
		}
	}
	failBuild := func(err error, msg string) error {
		appendStep("ERROR: " + msg)
		if buildID > 0 {
			_ = database.FinishBuildWithTokens(buildID, "failed", logBuf.String(), msg, 0, 0, 0, 0, 0)
		}
		proj.Status = "failed"
		_ = database.UpdateProject(proj)
		return err
	}

	appendStep(fmt.Sprintf("=== 开始构建微服务聚合项目: %s (ID=%s, TriCode=%s) ===", proj.Name, proj.ID, proj.TriCode))

	repos, err := database.GetProjectRepos(projectID)
	if err != nil {
		return failBuild(err, fmt.Sprintf("获取子仓列表失败: %v", err))
	}
	appendStep(fmt.Sprintf("[1/4] 准备虚拟工作区 (共 %d 个子仓)...", len(repos)))

	wsMgr := project.NewWorkspaceManager(s.config.ProjectsBaseDir)
	wsDir, err := wsMgr.PrepareWorkspace(proj, repos)
	if err != nil {
		return failBuild(fmt.Errorf("prepare workspace: %w", err), fmt.Sprintf("工作区准备失败: %v", err))
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
		appendStep(fmt.Sprintf("[2/4] 执行跨仓拓扑扫描: %s ...", linkerScript))
		cmd := exec.Command("node", linkerScript, wsDir)
		if out, lErr := cmd.CombinedOutput(); lErr != nil {
			appendStep(fmt.Sprintf("[linker] warning: %v\n%s", lErr, string(out)))
		}
	} else {
		appendStep("[2/4] 未检测到 linker.js，跳过静态拓扑扫描")
	}

	// Generate _project_hub/openwiki/quickstart.md
	appendStep("[3/4] 生成系统全景总览与架构文档...")
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
	var prompt, completion, total, saved int64
	var retries int
	if s.scheduler != nil && s.scheduler.GetBuilder() != nil {
		builder := s.scheduler.GetBuilder()
		wikiDir := hubOpenwikiDir
		appendStep(fmt.Sprintf("[4/4] 执行 OpenWiki 聚合知识库构建 (buildID=%d, wsDir=%s)...", buildID, wsDir))
		log.Printf("[project-build][%s] starting multi-repo openwiki build (buildID=%d, wsDir=%s)", projectID, buildID, wsDir)

		repoTarget := filepath.Join("projects", projectID)
		buildRes := builder.BuildRepoWithBuildID(buildID, repoTarget, wsDir, wikiDir,
			func(currentLog string) {
				if buildID > 0 {
					_ = database.UpdateBuildLog(buildID, logBuf.String()+"\n"+currentLog, "")
				}
			})

		if buildRes.Log != "" {
			logBuf.WriteString("\n")
			logBuf.WriteString(buildRes.Log)
		}

		// Token accounting via LLMProxy
		if s.llmProxy != nil {
			if usage, ok := s.llmProxy.PopBuildUsage(fmt.Sprintf("%d", buildID)); ok {
				prompt = int64(usage.PromptTokens)
				completion = int64(usage.CompletionTokens)
				total = int64(usage.TotalTokens)
				retries = usage.RateLimitedRetries
			}
		}

		if !buildRes.Success {
			log.Printf("[project-build][%s] multi-repo build FAILED: %s", projectID, buildRes.Error)
			if buildID > 0 {
				_ = database.FinishBuildWithTokens(buildID, "failed", logBuf.String(), buildRes.Error,
					prompt, completion, total, saved, retries)
			}
			proj.Status = "failed"
			_ = database.UpdateProject(proj)
			return fmt.Errorf("multi-repo openwiki build failed: %s", buildRes.Error)
		}
		log.Printf("[project-build][%s] multi-repo build completed successfully", projectID)
	} else {
		appendStep("[4/4] 未配置 OpenWiki CLI 构建器，仅完成虚拟工作区与架构汇总生成")
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

	if buildID > 0 {
		_ = database.FinishBuildWithTokens(buildID, "completed", logBuf.String(), "",
			prompt, completion, total, saved, retries)
	}

	proj.Status = "active"
	return database.UpdateProject(proj)
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
