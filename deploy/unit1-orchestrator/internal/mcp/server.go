package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/qa"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

type Server struct {
	cfg       *config.Config
	database  *db.DB
	qaManager *qa.Manager
	sessions  *SessionManager
}

func NewServer(cfg *config.Config, database *db.DB, qaMgr *qa.Manager) *Server {
	return &Server{
		cfg:       cfg,
		database:  database,
		qaManager: qaMgr,
		sessions:  NewSessionManager(100, 30*time.Minute, nil),
	}
}

// GetTools returns the supported MCP tools specification.
func (s *Server) GetTools() []Tool {
	return []Tool{
		{
			Name:        "ask_repository",
			Description: "向指定代码仓库或微服务聚合项目的知识库发起问答，通过 LangGraph 深度检索与推理获取准确解答。支持传入单仓 ID/名称，或微服务项目三字码（如 DEO）/ 项目 ID（如 proj-deo）。必须提供 user_id 进行调用审计。",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"repo_name": {
						Type:        "string",
						Description: "目标代码仓库 ID/名称，或微服务项目三字码 / 项目 ID（例如: DEO、proj-deo、bxbank/DEO/xone-auth）",
					},
					"user_id": {
						Type:        "string",
						Description: "调用者的用户标识/工号/用户名（必填，用于审计归属）",
					},
					"question": {
						Type:        "string",
						Description: "技术问题、架构设计、业务流程或代码实现细节",
					},
					"session_id": {
						Type:        "string",
						Description: "可选会话 ID，用于多轮上下文连续问答",
					},
				},
				Required: []string{"repo_name", "user_id", "question"},
			},
		},
		{
			Name:        "ask_project",
			Description: "向指定微服务聚合项目（虚拟 Monorepo）的系统全景知识库发起问答，解答跨服务调用链路、整体系统架构与业务域职责。支持传入三字码（如 DEO）、项目 ID（如 proj-deo）或项目名称。必须提供 user_id 进行调用审计。",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"project_key": {
						Type:        "string",
						Description: "微服务项目查询键：支持三字码（如 DEO）、项目 ID（如 proj-deo）或项目名称",
					},
					"user_id": {
						Type:        "string",
						Description: "调用者的用户标识/工号/用户名（必填，用于审计归属）",
					},
					"question": {
						Type:        "string",
						Description: "跨微服务调用关系、系统整体架构、业务域职责等问题",
					},
					"session_id": {
						Type:        "string",
						Description: "可选会话 ID，用于多轮上下文连续问答",
					},
				},
				Required: []string{"project_key", "user_id", "question"},
			},
		},
		{
			Name:        "list_repositories",
			Description: "列出当前平台已纳管的所有代码仓库及微服务聚合项目摘要，返回各自的 type、query_key、tri_code（三字码）与知识库构建就绪状态。",
			InputSchema: ToolInputSchema{
				Type:       "object",
				Properties: map[string]PropertySchema{},
			},
		},
		{
			Name:        "list_projects",
			Description: "列出当前平台所有微服务聚合项目（虚拟 Monorepo），返回项目 ID、三字码（tri_code）、推荐 MCP 查询 Key（query_key）、所属业务域、子仓列表与任务运行状态。",
			InputSchema: ToolInputSchema{
				Type:       "object",
				Properties: map[string]PropertySchema{},
			},
		},
		{
			Name:        "list_repository_docs",
			Description: "列出指定代码仓库中已构建的所有知识文档清单，包含系统自动生成的架构文档与团队维护的专题业务文档（knowledge/）。返回文档相对路径、类型、标题与字节大小。",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"repo_name": {
						Type:        "string",
						Description: "目标代码仓库 ID 或名称（例如: zk-service、test）",
					},
					"category": {
						Type:        "string",
						Description: "可选筛选类别: 'all'(默认全部), 'knowledge'(仅团队专题文档), 'architecture'(仅架构文档)",
					},
				},
				Required: []string{"repo_name"},
			},
		},
		{
			Name:        "read_repository_doc",
			Description: "直接读取目标代码仓库中特定 Wiki 文档的完整 Markdown 正文内容。支持传入从 list_repository_docs 获取到的相对路径（如 'architecture/overview.md' 或 'knowledge/pay_flow.md'）。内置目录沙箱防护。",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"repo_name": {
						Type:        "string",
						Description: "目标代码仓库 ID 或名称",
					},
					"doc_path": {
						Type:        "string",
						Description: "文档相对路径（例如: 'knowledge/zk_guide.md' 或 'architecture/overview.md'）",
					},
				},
				Required: []string{"repo_name", "doc_path"},
			},
		},
	}
}

// ExecuteTool dispatches and executes the requested tool.
func (s *Server) ExecuteTool(ctx context.Context, session *ClientSession, name string, args map[string]any, meta *RequestMeta, onStreamToken func(token string)) (*CallToolResult, error) {
	switch name {
	case "ask_repository":
		return s.handleAskRepository(ctx, session, args, meta, onStreamToken)
	case "ask_project":
		return s.handleAskProject(ctx, session, args, meta, onStreamToken)
	case "list_repositories":
		return s.handleListRepositories(ctx)
	case "list_projects":
		return s.handleListProjects(ctx)
	case "list_repository_docs":
		return s.handleListRepositoryDocs(ctx, args)
	case "read_repository_doc":
		return s.handleReadRepositoryDoc(ctx, args)
	default:
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: fmt.Sprintf("unknown tool: %s", name),
			}},
		}, nil
	}
}

func firstStringArg(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func (s *Server) handleAskRepository(ctx context.Context, session *ClientSession, args map[string]any, meta *RequestMeta, onStreamToken func(token string)) (*CallToolResult, error) {
	repoName := firstStringArg(args, "repo_name", "key", "id")
	userID := firstStringArg(args, "user_id")
	question := firstStringArg(args, "question")
	sessionID := firstStringArg(args, "session_id")

	if userID == "" {
		return mcpErrorResult("user_id is required for audit. Please provide your username or employee ID."), nil
	}
	if repoName == "" {
		return mcpErrorResult("repo_name is required."), nil
	}
	if question == "" {
		return mcpErrorResult("question cannot be empty."), nil
	}

	targetRepo, err := s.database.GetRepoByNameOrID(repoName)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	if targetRepo != nil {
		return s.streamQAWithProgress(ctx, session, sessionID, meta, onStreamToken, func(resolvedSessID string, writer *MCPStreamWriter) error {
			return s.qaManager.StreamChat(ctx, targetRepo, userID, resolvedSessID, question, writer)
		})
	}

	// Fallback: match aggregated microservice project by ID, tri_code, or name
	targetProj, err := s.database.GetProjectByNameOrID(repoName)
	if err != nil {
		return nil, fmt.Errorf("database project query failed: %w", err)
	}
	if targetProj != nil {
		return s.streamQAWithProgress(ctx, session, sessionID, meta, onStreamToken, func(resolvedSessID string, writer *MCPStreamWriter) error {
			return s.qaManager.StreamProjectChat(ctx, targetProj, userID, resolvedSessID, question, writer)
		})
	}

	return mcpErrorResult(fmt.Sprintf("repository or project '%s' not found.", repoName)), nil
}

func (s *Server) handleAskProject(ctx context.Context, session *ClientSession, args map[string]any, meta *RequestMeta, onStreamToken func(token string)) (*CallToolResult, error) {
	projKey := firstStringArg(args, "project_key", "project_id", "tri_code", "name")
	userID := firstStringArg(args, "user_id")
	question := firstStringArg(args, "question")
	sessionID := firstStringArg(args, "session_id")

	if userID == "" {
		return mcpErrorResult("user_id is required for audit. Please provide your username or employee ID."), nil
	}
	if projKey == "" {
		return mcpErrorResult("project_key (project_id, tri_code, or name) is required."), nil
	}
	if question == "" {
		return mcpErrorResult("question cannot be empty."), nil
	}

	targetProj, err := s.database.GetProjectByNameOrID(projKey)
	if err != nil {
		return nil, fmt.Errorf("database project query failed: %w", err)
	}
	if targetProj == nil {
		return mcpErrorResult(fmt.Sprintf("microservice project '%s' not found.", projKey)), nil
	}

	return s.streamQAWithProgress(ctx, session, sessionID, meta, onStreamToken, func(resolvedSessID string, writer *MCPStreamWriter) error {
		return s.qaManager.StreamProjectChat(ctx, targetProj, userID, resolvedSessID, question, writer)
	})
}

func mcpErrorResult(msg string) *CallToolResult {
	return &CallToolResult{
		IsError: true,
		Content: []ContentItem{{
			Type: "text",
			Text: msg,
		}},
	}
}

func (s *Server) streamQAWithProgress(
	ctx context.Context,
	session *ClientSession,
	sessionID string,
	meta *RequestMeta,
	onStreamToken func(token string),
	streamFn func(resolvedSessionID string, writer *MCPStreamWriter) error,
) (*CallToolResult, error) {
	var progressToken any
	if meta != nil {
		progressToken = meta.ProgressToken
	}

	sendNotification := func(notif any) {
		if session != nil {
			session.Send(notif)
		}
		if onStreamToken != nil {
			if data, err := json.Marshal(notif); err == nil {
				onStreamToken(string(data))
			}
		}
	}

	if sessionID == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		sessionID = fmt.Sprintf("mcp_sess_%d_%s", time.Now().Unix(), hex.EncodeToString(b))
	}

	if progressToken != nil {
		sendNotification(JSONRPCNotification{
			JSONRPC: "2.0",
			Method:  "notifications/progress",
			Params: ProgressParams{
				ProgressToken: progressToken,
				Progress:      5,
				Message:       "Connecting to knowledge base...",
			},
		})
	}

	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	if progressToken != nil {
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			step := 10.0
			for {
				select {
				case <-stopHeartbeat:
					return
				case <-ticker.C:
					if step < 90 {
						step += 10
					}
					sendNotification(JSONRPCNotification{
						JSONRPC: "2.0",
						Method:  "notifications/progress",
						Params: ProgressParams{
							ProgressToken: progressToken,
							Progress:      step,
							Message:       "Analyzing codebase and reasoning...",
						},
					})
				}
			}
		}()
	}

	writer := NewMCPStreamWriter(func(token string) {
		sendNotification(JSONRPCNotification{
			JSONRPC: "2.0",
			Method:  "notifications/message",
			Params: LoggingMessageParams{
				Level:  "info",
				Data:   token,
				Logger: "openwiki-mcp",
			},
		})
	})

	if s.qaManager == nil {
		return mcpErrorResult("QA Manager is not available."), nil
	}

	if err := streamFn(sessionID, writer); err != nil {
		writer.Flush()
		log.Printf("[MCP] StreamChat execution error: %v", err)
		return mcpErrorResult(fmt.Sprintf("execution failed: %v", err)), nil
	}
	writer.Flush()

	if progressToken != nil {
		sendNotification(JSONRPCNotification{
			JSONRPC: "2.0",
			Method:  "notifications/progress",
			Params: ProgressParams{
				ProgressToken: progressToken,
				Progress:      100,
				Message:       "Complete",
			},
		})
	}

	return &CallToolResult{
		IsError: false,
		Content: []ContentItem{{
			Type: "text",
			Text: writer.FullAnswer(),
		}},
	}, nil
}

func isProjectWikiReady(p db.Project, latestBuild *db.Build, staticBaseDir ...string) (taskStatus string, lastBuildStatus string, ready bool) {
	if p.Status == "queued" {
		taskStatus = "queued"
	} else if p.Status == "building" {
		taskStatus = "running"
	} else if latestBuild != nil {
		lastBuildStatus = latestBuild.Status
		switch latestBuild.Status {
		case "running":
			taskStatus = "running"
		case "completed", "success", "skipped":
			// 严格按资产深度完整性校验，确认 Wiki 与静态资源在磁盘上真正具备条件
			wikiDir := filepath.Join(p.LocalPath, "openwiki")
			wikiComplete := scheduler.IsWikiDirComplete(wikiDir)

			staticComplete := true
			if len(staticBaseDir) > 0 && staticBaseDir[0] != "" {
				staticDir := filepath.Join(staticBaseDir[0], "projects", p.ID)
				if !scheduler.IsStaticDirComplete(staticDir) {
					// 兼容根路径 StaticOutputDir/<projectID>
					staticDir = filepath.Join(staticBaseDir[0], p.ID)
				}
				staticComplete = scheduler.IsStaticDirComplete(staticDir)
			}

			if wikiComplete && staticComplete {
				taskStatus = "completed"
				ready = true
			} else {
				taskStatus = "incomplete"
				ready = false
			}
		case "failed", "interrupted":
			taskStatus = latestBuild.Status
		default:
			taskStatus = latestBuild.Status
		}
	} else if p.Status == "failed" {
		taskStatus = "failed"
	} else {
		taskStatus = "never_built"
	}
	if p.Status == "active" && latestBuild == nil {
		// Workspace metadata exists even before openwiki CLI build completes
		ready = false
	}
	return taskStatus, lastBuildStatus, ready
}

func (s *Server) handleListRepositories(ctx context.Context) (*CallToolResult, error) {
	repos, err := s.database.ListRepos()
	if err != nil {
		return nil, fmt.Errorf("failed to list repos: %w", err)
	}
	projects, _ := s.database.ListProjects()

	type CatalogEntry struct {
		Type            string `json:"type"`
		ID              string `json:"id"`
		QueryKey        string `json:"query_key"`
		TriCode         string `json:"tri_code,omitempty"`
		Name            string `json:"name"`
		BusinessDomain  string `json:"business_domain,omitempty"`
		Branch          string `json:"branch,omitempty"`
		Status          string `json:"status"`
		TaskStatus      string `json:"task_status,omitempty"`
		LastBuildStatus string `json:"last_build_status,omitempty"`
		WikiReady       bool   `json:"wiki_ready"`
	}

	var results []CatalogEntry
	for _, r := range repos {
		entry := CatalogEntry{
			Type:           "repository",
			ID:             r.ID,
			QueryKey:       r.ID,
			Name:           r.Name,
			BusinessDomain: r.BusinessDomain,
			Branch:         r.Branch,
			Status:         r.Status,
		}
		if build, err := s.database.GetLatestBuild(r.ID); err == nil && build != nil {
			entry.LastBuildStatus = build.Status
			if build.Status == "success" || build.Status == "completed" || build.Status == "skipped" {
				// 严格校验是否真正具备 Wiki 文档与静态资源完整条件
				wikiDir := r.WikiDir
				if wikiDir == "" {
					wikiDir = filepath.Join(r.LocalPath, "openwiki")
				}
				wikiComplete := scheduler.IsWikiDirComplete(wikiDir)
				staticComplete := true
				if s.cfg != nil && s.cfg.StaticOutputDir != "" {
					staticDir := filepath.Join(s.cfg.StaticOutputDir, r.ID)
					staticComplete = scheduler.IsStaticDirComplete(staticDir)
				}
				if wikiComplete && staticComplete {
					entry.WikiReady = true
				}
			}
		}
		results = append(results, entry)
	}

	staticBaseDir := ""
	if s.cfg != nil {
		staticBaseDir = s.cfg.StaticOutputDir
	}

	for _, p := range projects {
		latestBuild, _ := s.database.GetLatestBuild(p.ID)
		taskStatus, lastBuildStatus, ready := isProjectWikiReady(p, latestBuild, staticBaseDir)
		queryKey := p.ID
		if strings.TrimSpace(p.TriCode) != "" {
			queryKey = strings.TrimSpace(p.TriCode)
		}
		results = append(results, CatalogEntry{
			Type:            "project",
			ID:              p.ID,
			QueryKey:        queryKey,
			TriCode:         p.TriCode,
			Name:            p.Name,
			BusinessDomain:  p.BusinessDomain,
			Status:          p.Status,
			TaskStatus:      taskStatus,
			LastBuildStatus: lastBuildStatus,
			WikiReady:       ready,
		})
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return nil, err
	}

	return &CallToolResult{
		IsError: false,
		Content: []ContentItem{{
			Type: "text",
			Text: string(data),
		}},
	}, nil
}

func (s *Server) handleListProjects(ctx context.Context) (*CallToolResult, error) {
	projects, err := s.database.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	type MemberRepoSummary struct {
		RepoID    string `json:"repo_id"`
		AliasName string `json:"alias_name"`
		Role      string `json:"role"`
	}
	type ProjectSummary struct {
		ID              string              `json:"id"`
		TriCode         string              `json:"tri_code,omitempty"`
		QueryKey        string              `json:"query_key"`
		Name            string              `json:"name"`
		BusinessDomain  string              `json:"business_domain,omitempty"`
		Description     string              `json:"description,omitempty"`
		Status          string              `json:"status"`
		TaskStatus      string              `json:"task_status"`
		LastBuildStatus string              `json:"last_build_status,omitempty"`
		WikiReady       bool                `json:"wiki_ready"`
		MemberRepos     []MemberRepoSummary `json:"member_repos"`
	}

	staticBaseDir := ""
	if s.cfg != nil {
		staticBaseDir = s.cfg.StaticOutputDir
	}

	results := make([]ProjectSummary, 0, len(projects))
	for _, p := range projects {
		latestBuild, _ := s.database.GetLatestBuild(p.ID)
		taskStatus, lastBuildStatus, ready := isProjectWikiReady(p, latestBuild, staticBaseDir)
		queryKey := p.ID
		if strings.TrimSpace(p.TriCode) != "" {
			queryKey = strings.TrimSpace(p.TriCode)
		}
		repos, _ := s.database.GetProjectRepos(p.ID)
		members := make([]MemberRepoSummary, 0, len(repos))
		for _, r := range repos {
			members = append(members, MemberRepoSummary{
				RepoID:    r.ID,
				AliasName: r.AliasName,
				Role:      r.Role,
			})
		}
		results = append(results, ProjectSummary{
			ID:              p.ID,
			TriCode:         p.TriCode,
			QueryKey:        queryKey,
			Name:            p.Name,
			BusinessDomain:  p.BusinessDomain,
			Description:     p.Description,
			Status:          p.Status,
			TaskStatus:      taskStatus,
			LastBuildStatus: lastBuildStatus,
			WikiReady:       ready,
			MemberRepos:     members,
		})
	}

	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return nil, err
	}

	return &CallToolResult{
		IsError: false,
		Content: []ContentItem{{
			Type: "text",
			Text: string(data),
		}},
	}, nil
}

type RepoDocSummary struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	Type      string `json:"type"` // "custom_knowledge" or "architecture_wiki"
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (s *Server) handleListRepositoryDocs(ctx context.Context, args map[string]any) (*CallToolResult, error) {
	repoName := firstStringArg(args, "repo_name", "key", "id")
	if repoName == "" {
		return mcpErrorResult("repo_name is required."), nil
	}
	category := strings.ToLower(firstStringArg(args, "category"))

	targetRepo, err := s.database.GetRepoByNameOrID(repoName)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	if targetRepo == nil {
		return mcpErrorResult(fmt.Sprintf("repository '%s' not found.", repoName)), nil
	}

	wikiDir := targetRepo.WikiDir
	if wikiDir == "" {
		wikiDir = filepath.Join(targetRepo.LocalPath, "openwiki")
	}

	info, err := os.Stat(wikiDir)
	if err != nil || !info.IsDir() {
		return mcpErrorResult(fmt.Sprintf("wiki directory for repository '%s' not found or not built yet.", repoName)), nil
	}

	docs := make([]RepoDocSummary, 0)
	_ = filepath.Walk(wikiDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(fi.Name()), ".md") {
			return nil
		}

		rel, err := filepath.Rel(wikiDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		isKnowledge := strings.HasPrefix(rel, "knowledge/")
		if category == "knowledge" && !isKnowledge {
			return nil
		}
		if category == "architecture" && isKnowledge {
			return nil
		}

		docType := "architecture_wiki"
		if isKnowledge {
			docType = "custom_knowledge"
		}

		// Quick title extraction
		title := strings.TrimSuffix(fi.Name(), ".md")
		if data, err := os.ReadFile(path); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "title:") {
					title = strings.TrimSpace(strings.TrimPrefix(l, "title:"))
					title = strings.Trim(title, `"'`)
					break
				}
				if strings.HasPrefix(l, "# ") {
					title = strings.TrimSpace(strings.TrimPrefix(l, "# "))
					break
				}
			}
		}

		docs = append(docs, RepoDocSummary{
			Path:      rel,
			Title:     title,
			Type:      docType,
			SizeBytes: fi.Size(),
			UpdatedAt: fi.ModTime().Format(time.RFC3339),
		})
		return nil
	})

	data, err := json.MarshalIndent(docs, "", "  ")
	if err != nil {
		return nil, err
	}

	return &CallToolResult{
		IsError: false,
		Content: []ContentItem{{
			Type: "text",
			Text: string(data),
		}},
	}, nil
}

func (s *Server) handleReadRepositoryDoc(ctx context.Context, args map[string]any) (*CallToolResult, error) {
	repoName := firstStringArg(args, "repo_name", "key", "id")
	if repoName == "" {
		return mcpErrorResult("repo_name is required."), nil
	}
	docPath := firstStringArg(args, "doc_path", "path", "file")
	if docPath == "" {
		return mcpErrorResult("doc_path is required."), nil
	}

	targetRepo, err := s.database.GetRepoByNameOrID(repoName)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	if targetRepo == nil {
		return mcpErrorResult(fmt.Sprintf("repository '%s' not found.", repoName)), nil
	}

	wikiDir := targetRepo.WikiDir
	if wikiDir == "" {
		wikiDir = filepath.Join(targetRepo.LocalPath, "openwiki")
	}

	cleanDocPath := filepath.Clean(docPath)
	fullPath := filepath.Clean(filepath.Join(wikiDir, cleanDocPath))

	// Jail security check: strictly ensure target is inside wikiDir
	rel, err := filepath.Rel(wikiDir, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") || strings.HasPrefix(rel, "/") {
		return mcpErrorResult("Access denied: path escapes the repository wiki directory."), nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return mcpErrorResult(fmt.Sprintf("document '%s' not found in repository '%s'.", docPath, repoName)), nil
	}

	return &CallToolResult{
		IsError: false,
		Content: []ContentItem{{
			Type: "text",
			Text: string(data),
		}},
	}, nil
}

// ProcessJSONRPC processes an incoming JSON-RPC request and returns the response (or nil for notifications).
func (s *Server) ProcessJSONRPC(ctx context.Context, session *ClientSession, body []byte) (*JSONRPCResponse, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error: &JSONRPCError{
				Code:    CodeParseError,
				Message: "Parse error: invalid JSON",
			},
		}, nil
	}

	// Validate JSON-RPC 2.0
	if req.JSONRPC != "2.0" {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    CodeInvalidRequest,
				Message: "Invalid Request: missing or invalid jsonrpc version",
			},
		}, nil
	}

	switch req.Method {
	case "initialize":
		var params InitializeRequestParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		result := InitializeResult{
			ProtocolVersion: "2025-03-26",
			ServerInfo: Implementation{
				Name:    "openwiki-mcp-server",
				Version: "1.0.0",
			},
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{
					ListChanged: false,
				},
				Logging: map[string]any{},
			},
		}
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}, nil

	case "notifications/initialized":
		// No response required for notifications
		return nil, nil

	case "ping":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}, nil

	case "tools/list":
		tools := s.GetTools()
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ListToolsResult{
				Tools: tools,
			},
		}, nil

	case "tools/call":
		var params CallToolRequestParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    CodeInvalidParams,
					Message: "Invalid params for tools/call",
				},
			}, nil
		}

		res, err := s.ExecuteTool(ctx, session, params.Name, params.Arguments, params.Meta, nil)
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    CodeInternalError,
					Message: err.Error(),
				},
			}, nil
		}

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  res,
		}, nil

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    CodeMethodNotFound,
				Message: fmt.Sprintf("Method '%s' not found", req.Method),
			},
		}, nil
	}
}

// --- HTTP Handlers for Streamable HTTP & Legacy SSE ---

func generateSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HandleStreamableHTTP handles MCP 2025 Streamable HTTP on /mcp
func (s *Server) HandleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !s.sessions.CheckOrigin(origin, r.Host) {
		http.Error(w, "Forbidden: Origin not allowed", http.StatusForbidden)
		return
	}

	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("sessionId")
	}

	var session *ClientSession
	var err error

	if sessionID != "" {
		session, err = s.sessions.GetOrCreate(sessionID)
		if err != nil {
			if err == ErrMaxSessionsExceeded {
				http.Error(w, "Service Unavailable: max sessions reached", http.StatusServiceUnavailable)
				return
			}
		}
	}

	if r.Method == http.MethodGet {
		// Server-Sent Events stream initialization for Streamable HTTP
		if session == nil {
			sessionID = generateSessionID()
			session, err = s.sessions.GetOrCreate(sessionID)
			if err != nil {
				http.Error(w, "Service Unavailable: max sessions reached", http.StatusServiceUnavailable)
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Mcp-Session-Id", sessionID)
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if ok {
			flusher.Flush()
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case <-session.Done:
				return
			case msg, ok := <-session.MsgChan:
				if !ok {
					return
				}
				data, _ := json.Marshal(msg)
				fmt.Fprintf(w, "data: %s\n\n", data)
				if ok && flusher != nil {
					flusher.Flush()
				}
			}
		}
	}

	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req JSONRPCRequest
		isJSONRPC := json.Unmarshal(body, &req) == nil

		acceptHeader := r.Header.Get("Accept")
		wantsSSE := strings.Contains(acceptHeader, "text/event-stream")

		// If client requests Streamable HTTP SSE streaming and is calling a tool
		if isJSONRPC && req.Method == "tools/call" && wantsSSE {
			var params CallToolRequestParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				http.Error(w, "Invalid params for tools/call", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			if session != nil {
				w.Header().Set("Mcp-Session-Id", session.ID)
			}
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}

			onStreamToken := func(jsonStr string) {
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", jsonStr)
				if ok {
					flusher.Flush()
				}
			}

			res, err := s.ExecuteTool(r.Context(), session, params.Name, params.Arguments, params.Meta, onStreamToken)
			var resp *JSONRPCResponse
			if err != nil {
				resp = &JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &JSONRPCError{
						Code:    CodeInternalError,
						Message: err.Error(),
					},
				}
			} else {
				resp = &JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  res,
				}
			}

			respData, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", respData)
			if ok {
				flusher.Flush()
			}
			return
		}

		resp, err := s.ProcessJSONRPC(r.Context(), session, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if resp == nil {
			// Notification, 204 No Content
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if session != nil {
			w.Header().Set("Mcp-Session-Id", session.ID)
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// HandleLegacySSE handles legacy MCP Remote SSE on GET /mcp/sse
func (s *Server) HandleLegacySSE(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !s.sessions.CheckOrigin(origin, r.Host) {
		http.Error(w, "Forbidden: Origin not allowed", http.StatusForbidden)
		return
	}

	sessionID := generateSessionID()
	session, err := s.sessions.GetOrCreate(sessionID)
	if err != nil {
		http.Error(w, "Service Unavailable: max sessions reached", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if ok {
		flusher.Flush()
	}

	// 1. Emit legacy endpoint event
	endpointURL := fmt.Sprintf("/mcp/messages?sessionId=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURL)
	if ok {
		flusher.Flush()
	}

	// 2. Stream notifications and responses
	for {
		select {
		case <-r.Context().Done():
			s.sessions.Remove(sessionID)
			return
		case <-session.Done:
			return
		case msg, ok := <-session.MsgChan:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			if ok {
				flusher.Flush()
			}
		}
	}
}

// HandleLegacyMessages handles legacy MCP Remote SSE message dispatch on POST /mcp/messages
func (s *Server) HandleLegacyMessages(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !s.sessions.CheckOrigin(origin, r.Host) {
		http.Error(w, "Forbidden: Origin not allowed", http.StatusForbidden)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "Missing sessionId query parameter", http.StatusBadRequest)
		return
	}

	session, err := s.sessions.Get(sessionID)
	if err != nil {
		http.Error(w, "Session expired or not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err == nil && req.Method == "tools/call" {
		// Respond 202 Accepted immediately to avoid client POST timeout in tools/call
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("Accepted"))

		// Asynchronously execute tool and push events + result to the SSE session
		go func() {
			var params CallToolRequestParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				session.Send(&JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &JSONRPCError{
						Code:    CodeInvalidParams,
						Message: "Invalid params for tools/call",
					},
				})
				return
			}

			res, err := s.ExecuteTool(context.Background(), session, params.Name, params.Arguments, params.Meta, nil)
			if err != nil {
				session.Send(&JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &JSONRPCError{
						Code:    CodeInternalError,
						Message: err.Error(),
					},
				})
				return
			}

			session.Send(&JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  res,
			})
		}()
		return
	}

	resp, err := s.ProcessJSONRPC(r.Context(), session, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if resp != nil {
		// In legacy SSE, the response is pushed into the SSE stream
		session.Send(resp)
	}

	// Legacy spec responds 202 Accepted to the POST request
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("Accepted"))
}
