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
	"strings"
	"time"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/qa"
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
			Description: "向指定代码仓库的架构与代码知识库发起问答，通过 LangGraph 深度检索与推理获取准确解答。必须提供 user_id 进行调用审计。",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"repo_name": {
						Type:        "string",
						Description: "目标代码仓库名称或 ID（例如: openwiki）",
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
			Name:        "list_repositories",
			Description: "列出当前平台已纳管的所有代码仓库及其知识库构建就绪状态。",
			InputSchema: ToolInputSchema{
				Type:       "object",
				Properties: map[string]PropertySchema{},
			},
		},
	}
}

// ExecuteTool dispatches and executes the requested tool.
func (s *Server) ExecuteTool(ctx context.Context, session *ClientSession, name string, args map[string]any) (*CallToolResult, error) {
	switch name {
	case "ask_repository":
		return s.handleAskRepository(ctx, session, args)
	case "list_repositories":
		return s.handleListRepositories(ctx)
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

func (s *Server) handleAskRepository(ctx context.Context, session *ClientSession, args map[string]any) (*CallToolResult, error) {
	repoName, _ := args["repo_name"].(string)
	userID, _ := args["user_id"].(string)
	question, _ := args["question"].(string)
	sessionID, _ := args["session_id"].(string)

	repoName = strings.TrimSpace(repoName)
	userID = strings.TrimSpace(userID)
	question = strings.TrimSpace(question)
	sessionID = strings.TrimSpace(sessionID)

	// Strict user_id audit check
	if userID == "" {
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: "user_id is required for audit. Please provide your username or employee ID.",
			}},
		}, nil
	}

	if repoName == "" {
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: "repo_name is required.",
			}},
		}, nil
	}

	if question == "" {
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: "question cannot be empty.",
			}},
		}, nil
	}

	// Lookup repository using disambiguated query
	targetRepo, err := s.database.GetRepoByNameOrID(repoName)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	if targetRepo == nil {
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: fmt.Sprintf("repository '%s' not found.", repoName),
			}},
		}, nil
	}

	// Generate session ID if not provided
	if sessionID == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		sessionID = fmt.Sprintf("mcp_sess_%d_%s", time.Now().Unix(), hex.EncodeToString(b))
	}

	// Create stream writer adapter to push tokens via MCP notification if session is active
	writer := NewMCPStreamWriter(func(token string) {
		if session != nil {
			notif := JSONRPCNotification{
				JSONRPC: "2.0",
				Method:  "notifications/message",
				Params: LoggingMessageParams{
					Level:  "info",
					Data:   token,
					Logger: "openwiki-mcp",
				},
			}
			session.Send(notif)
		}
	})

	if s.qaManager == nil {
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: "QA Manager is not available.",
			}},
		}, nil
	}

	// Stream chat with existing QA manager (zero changes to qa.Manager)
	err = s.qaManager.StreamChat(ctx, targetRepo, userID, sessionID, question, writer)
	writer.Flush()

	if err != nil {
		log.Printf("[MCP] StreamChat execution error: %v", err)
		return &CallToolResult{
			IsError: true,
			Content: []ContentItem{{
				Type: "text",
				Text: fmt.Sprintf("execution failed: %v", err),
			}},
		}, nil
	}

	fullAnswer := writer.FullAnswer()
	return &CallToolResult{
		IsError: false,
		Content: []ContentItem{{
			Type: "text",
			Text: fullAnswer,
		}},
	}, nil
}

func (s *Server) handleListRepositories(ctx context.Context) (*CallToolResult, error) {
	repos, err := s.database.ListRepos()
	if err != nil {
		return nil, fmt.Errorf("failed to list repos: %w", err)
	}

	type RepoSummary struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Branch          string `json:"branch"`
		Status          string `json:"status"`
		LastBuildStatus string `json:"last_build_status,omitempty"`
		WikiReady       bool   `json:"wiki_ready"`
	}

	var results []RepoSummary
	for _, r := range repos {
		summary := RepoSummary{
			ID:     r.ID,
			Name:   r.Name,
			Branch: r.Branch,
			Status: r.Status,
		}

		build, err := s.database.GetLatestBuild(r.ID)
		if err == nil && build != nil {
			summary.LastBuildStatus = build.Status
			if build.Status == "success" {
				summary.WikiReady = true
			}
		}

		results = append(results, summary)
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

		res, err := s.ExecuteTool(ctx, session, params.Name, params.Arguments)
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
