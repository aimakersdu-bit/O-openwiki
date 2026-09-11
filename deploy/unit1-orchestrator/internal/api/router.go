package api

import (
	"log"
	"net/http"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/qa"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func logPrintf(format string, v ...interface{}) {
	log.Printf("[API] "+format, v...)
}

// Server holds API handlers and dependencies.
type Server struct {
	config    *config.Config
	scheduler *scheduler.Scheduler
	qaPool    *qa.Pool
	qaManager *qa.Manager
	mux       *http.ServeMux
}

// NewServer creates a new Orchestrator API server.
func NewServer(cfg *config.Config, sched *scheduler.Scheduler, pool *qa.Pool, manager *qa.Manager) *Server {
	s := &Server{
		config:    cfg,
		scheduler: sched,
		qaPool:    pool,
		qaManager: manager,
		mux:       http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/repos", s.handleRepos)
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/api/qa/history", s.handleQAHistory)
	s.mux.HandleFunc("/api/qa/sessions", s.handleUserQASessions)
	s.mux.HandleFunc("/api/qa/messages", s.handleQAMessages)
	s.mux.HandleFunc("/api/build/status", s.handleBuildStatus)
	s.mux.HandleFunc("/api/build/trigger", s.handleBuildTrigger)

	// Health check endpoint
	s.mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"unit1-orchestrator"}`))
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for Portal requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.mux.ServeHTTP(w, r)
}
