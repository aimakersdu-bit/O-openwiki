package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/llmproxy"
	"github.com/openwiki/orchestrator/internal/mcp"
	"github.com/openwiki/orchestrator/internal/qa"
	"github.com/openwiki/orchestrator/internal/scheduler"
	"github.com/openwiki/orchestrator/internal/storage"
)

func logPrintf(format string, v ...interface{}) {
	log.Printf("[API] "+format, v...)
}

// Server holds API handlers and dependencies.
type Server struct {
	config        *config.Config
	scheduler     *scheduler.Scheduler
	qaPool        *qa.Pool
	qaManager     *qa.Manager
	mcpServer     *mcp.Server
	llmProxy      *llmproxy.LLMProxy
	volumeManager *storage.VolumeManager
	migrator      *storage.Migrator
	mux           *http.ServeMux

	sseMu    sync.RWMutex
	sseChans map[chan string]bool
}

// NewServer creates a new Orchestrator API server.
func NewServer(cfg *config.Config, sched *scheduler.Scheduler, pool *qa.Pool, manager *qa.Manager, mcpServer *mcp.Server, proxy ...*llmproxy.LLMProxy) *Server {
	s := &Server{
		config:    cfg,
		scheduler: sched,
		qaPool:    pool,
		qaManager: manager,
		mcpServer: mcpServer,
		mux:       http.NewServeMux(),
		sseChans:  make(map[chan string]bool),
	}
	if len(proxy) > 0 && proxy[0] != nil {
		s.llmProxy = proxy[0]
	} else if cfg != nil {
		var throttleCallback func(int)
		if sched != nil && sched.GetRateLimiter() != nil {
			throttleCallback = func(delta int) {
				lim := sched.GetRateLimiter()
				cur := lim.GetConcurrency()
				if cur > 1 {
					lim.SetConcurrency(cur - 1)
				}
			}
		}
		s.llmProxy = llmproxy.NewLLMProxy(cfg.OpenAIUpstreamURL, cfg.OpenwikiAPIKey, nil, throttleCallback)
	}

	linksDir := "/data/disk0/links/repos"
	if cfg != nil && cfg.ReposBaseDir != "" {
		linksDir = filepath.Join(filepath.Dir(cfg.ReposBaseDir), "disk0", "links", "repos")
	}
	s.volumeManager = storage.NewVolumeManager(s.getDB(), linksDir)
	s.migrator = storage.NewMigrator(s.getDB(), s.volumeManager, manager, linksDir)

	if s.llmProxy != nil && sched != nil {
		if sched.GetBuilder() != nil {
			port := "3000"
			if cfg != nil && cfg.ListenAddr != "" {
				port = strings.TrimPrefix(cfg.ListenAddr, ":")
			}
			sched.GetBuilder().SetLLMBaseURL(fmt.Sprintf("http://127.0.0.1:%s/internal/llm/v1", port))
		}
		if sched.GetPipeline() != nil {
			sched.GetPipeline().SetLLMProxy(s.llmProxy)
			if cfg != nil && cfg.LogBaseDir != "" {
				sched.GetPipeline().SetLogBaseDir(cfg.LogBaseDir)
			}
		}
	}

	s.routes()
	return s
}

// GetLLMProxy returns the configured LLMProxy instance.
func (s *Server) GetLLMProxy() *llmproxy.LLMProxy {
	return s.llmProxy
}

// GetVolumeManager returns the VolumeManager instance.
func (s *Server) GetVolumeManager() *storage.VolumeManager {
	return s.volumeManager
}

// SetVolumeManager overrides the VolumeManager (useful for testing).
func (s *Server) SetVolumeManager(vm *storage.VolumeManager) {
	s.volumeManager = vm
}

// GetMigrator returns the configured Migrator instance.
func (s *Server) GetMigrator() *storage.Migrator {
	return s.migrator
}

// SetMigrator overrides the Migrator (useful for testing).
func (s *Server) SetMigrator(m *storage.Migrator) {
	s.migrator = m
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/repos", s.handleRepos)
	s.mux.HandleFunc("/api/projects", s.handleProjects)
	s.mux.HandleFunc("/api/projects/", s.handleProjectSubroutes)
	s.mux.HandleFunc("/api/projects/build", s.handleProjectBuild)
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/api/qa/history", s.handleQAHistory)
	s.mux.HandleFunc("/api/qa/sessions", s.handleUserQASessions)
	s.mux.HandleFunc("/api/qa/messages", s.handleQAMessages)
	s.mux.HandleFunc("/api/build/status", s.handleBuildStatus)
	s.mux.HandleFunc("/api/build/trigger", s.handleBuildTrigger)

	// Batch Scheduler & Rate Limiter Endpoints
	s.mux.HandleFunc("/api/repos/batch-import", s.handleBatchImport)
	s.mux.HandleFunc("/api/batch/concurrency", s.handleBatchConcurrency)
	s.mux.HandleFunc("/api/batch/status", s.handleBatchStatus)
	s.mux.HandleFunc("/api/batch/pause", s.handleBatchControl)
	s.mux.HandleFunc("/api/batch/resume", s.handleBatchControl)
	s.mux.HandleFunc("/api/batch/cancel", s.handleBatchControl)

	// Daily Token Telemetry & Feishu/Lark Multi-dimensional Table Export
	s.mux.HandleFunc("/api/telemetry/tokens/daily", s.handleDailyTokens)
	s.mux.HandleFunc("/api/telemetry/tokens/export", s.handleTokensExport)

	// OpenAI-Compatible Internal LLM Gateway
	if s.llmProxy != nil {
		s.mux.Handle("/internal/llm/v1/", s.llmProxy)
		s.mux.Handle("/internal/llm/v1", s.llmProxy)
	}

	// LLM Quota & Dynamic Rate Limit Settings API
	s.mux.HandleFunc("/api/settings/llm-quota", s.handleLLMQuota)

	// Storage Pool & Volumes Management API
	s.mux.HandleFunc("/api/storage/volumes", s.handleStorageVolumes)
	s.mux.HandleFunc("/api/storage/rebalance/plan", s.handleRebalancePlan)
	s.mux.HandleFunc("/api/storage/migrations", s.handleMigrations)
	s.mux.HandleFunc("/api/storage/migrations/", s.handleMigrationAction)

	// Dedicated Log Disk Management Endpoints
	s.mux.HandleFunc("/api/logs/stats", s.handleLogStats)
	s.mux.HandleFunc("/api/logs/cleanup", s.handleLogCleanup)

	// MCP Protocol Endpoints (MCP 2025 Streamable HTTP & Legacy Remote SSE fallback)
	if s.mcpServer != nil {
		s.mux.HandleFunc("/mcp", s.mcpServer.HandleStreamableHTTP)
		s.mux.HandleFunc("/mcp/", s.mcpServer.HandleStreamableHTTP)
		s.mux.HandleFunc("/mcp/sse", s.mcpServer.HandleLegacySSE)
		s.mux.HandleFunc("/mcp/messages", s.mcpServer.HandleLegacyMessages)
	}

	// SSE events endpoint for visualizer hot reload (OpenWiki EventSource /events protocol)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/events", s.handleEvents)

	// Health check endpoint
	s.mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"unit1-orchestrator"}`))
	})
}

// handleEvents handles SSE client subscriptions for live reload notifications.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	msgChan := make(chan string, 10)
	s.sseMu.Lock()
	if s.sseChans == nil {
		s.sseChans = make(map[chan string]bool)
	}
	s.sseChans[msgChan] = true
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseChans, msgChan)
		s.sseMu.Unlock()
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	flusher.Flush()

	for {
		select {
		case msg := <-msgChan:
			w.Write([]byte(msg))
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// BroadcastReload sends a reload event to all connected SSE clients.
func (s *Server) BroadcastReload() {
	s.sseMu.RLock()
	defer s.sseMu.RUnlock()
	for ch := range s.sseChans {
		select {
		case ch <- "event: reload\ndata: 1\n\n":
		default:
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for Portal requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, X-OpenWiki-User, X-OpenWiki-Role, X-OpenWiki-Domains, X-OpenWiki-Sig")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.authMiddleware(s.mux).ServeHTTP(w, r)
}

func (s *Server) handleLLMQuota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	database := s.getDB()

	switch r.Method {
	case http.MethodGet:
		maxTPMStr, _ := database.GetSetting("max_tpm", "0")
		maxRPMStr, _ := database.GetSetting("max_rpm", "0")
		dayConcStr, _ := database.GetSetting("day_max_concurrency", "2")
		nightConcStr, _ := database.GetSetting("night_max_concurrency", "4")

		maxTPM, _ := strconv.Atoi(maxTPMStr)
		maxRPM, _ := strconv.Atoi(maxRPMStr)
		dayConc, _ := strconv.Atoi(dayConcStr)
		nightConc, _ := strconv.Atoi(nightConcStr)

		var curTPM, curRPM, peakTPM, peakRPM, count429 int
		if s.llmProxy != nil {
			curTPM, curRPM, peakTPM, peakRPM = s.llmProxy.GetLimiter().GetStats()
			count429 = s.llmProxy.Get429Count()
		}

		resp := map[string]interface{}{
			"max_tpm":              maxTPM,
			"max_rpm":              maxRPM,
			"current_tpm":          curTPM,
			"current_rpm":          curRPM,
			"peak_tpm":             peakTPM,
			"peak_rpm":             peakRPM,
			"rate_limit_429_count": count429,
			"day_max_concurrency":   dayConc,
			"night_max_concurrency": nightConc,
		}
		json.NewEncoder(w).Encode(resp)

	case http.MethodPut, http.MethodPost:
		var req struct {
			MaxTPM              *int `json:"max_tpm"`
			MaxRPM              *int `json:"max_rpm"`
			DayMaxConcurrency   *int `json:"day_max_concurrency"`
			NightMaxConcurrency *int `json:"night_max_concurrency"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}

		if req.MaxTPM != nil {
			_ = database.SetSetting("max_tpm", strconv.Itoa(*req.MaxTPM))
		}
		if req.MaxRPM != nil {
			_ = database.SetSetting("max_rpm", strconv.Itoa(*req.MaxRPM))
		}
		if req.DayMaxConcurrency != nil {
			_ = database.SetSetting("day_max_concurrency", strconv.Itoa(*req.DayMaxConcurrency))
		}
		if req.NightMaxConcurrency != nil {
			_ = database.SetSetting("night_max_concurrency", strconv.Itoa(*req.NightMaxConcurrency))
		}

		// Apply immediately to running rate limiter
		if s.llmProxy != nil {
			maxTPMStr, _ := database.GetSetting("max_tpm", "0")
			maxRPMStr, _ := database.GetSetting("max_rpm", "0")
			tpm, _ := strconv.Atoi(maxTPMStr)
			rpm, _ := strconv.Atoi(maxRPMStr)
			s.llmProxy.GetLimiter().SetQuota(tpm, rpm)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","message":"llm quota settings updated successfully"}`))

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStorageVolumes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	database := s.getDB()

	switch r.Method {
	case http.MethodGet:
		volumes, err := database.ListVolumes()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"list volumes: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"volumes": volumes,
		})

	case http.MethodPost:
		var req struct {
			ID         string `json:"id"`
			MountPoint string `json:"mount_point"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		if req.ID == "" || req.MountPoint == "" {
			http.Error(w, `{"error":"id and mount_point are required"}`, http.StatusBadRequest)
			return
		}

		if s.volumeManager != nil {
			vol, err := s.volumeManager.RegisterVolume(req.ID, req.MountPoint)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"register volume: %v"}`, err), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(vol)
			return
		}

		v := &db.Volume{
			ID:         req.ID,
			MountPoint: req.MountPoint,
			Status:     "active",
		}
		if err := database.CreateVolume(v); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"create volume: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(v)

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRebalancePlan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.migrator == nil {
		http.Error(w, `{"error":"migrator not initialized"}`, http.StatusInternalServerError)
		return
	}
	plan, err := s.migrator.GenerateRebalancePlan()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"generate rebalance plan: %v"}`, err), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"plan": plan,
	})
}

func (s *Server) handleMigrations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	database := s.getDB()

	switch r.Method {
	case http.MethodGet:
		migrations, err := database.ListVolumeMigrations()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"list migrations: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"migrations": migrations,
		})

	case http.MethodPost:
		var req struct {
			RepoID     string `json:"repo_id"`
			ToVolumeID string `json:"to_volume_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid json: %v"}`, err), http.StatusBadRequest)
			return
		}
		if req.RepoID == "" || req.ToVolumeID == "" {
			http.Error(w, `{"error":"repo_id and to_volume_id are required"}`, http.StatusBadRequest)
			return
		}
		if s.migrator == nil {
			http.Error(w, `{"error":"migrator not initialized"}`, http.StatusInternalServerError)
			return
		}

		mig, err := s.migrator.Migrate(r.Context(), req.RepoID, req.ToVolumeID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"migrate error: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(mig)

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMigrationAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.migrator == nil {
		http.Error(w, `{"error":"migrator not initialized"}`, http.StatusInternalServerError)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/storage/migrations/"), "/")
	if len(parts) < 2 {
		http.Error(w, `{"error":"invalid migration action url"}`, http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid migration id"}`, http.StatusBadRequest)
		return
	}
	action := parts[1]

	switch action {
	case "rollback":
		if err := s.migrator.Rollback(r.Context(), id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"rollback failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "message": "migration rolled back"})

	case "cleanup":
		if err := s.migrator.CleanupRetained(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"cleanup failed: %v"}`, err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "message": "old replica cleaned up"})

	default:
		http.Error(w, fmt.Sprintf(`{"error":"unknown action %s"}`, action), http.StatusBadRequest)
	}
}
