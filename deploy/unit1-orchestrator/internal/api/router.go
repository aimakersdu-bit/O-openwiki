package api

import (
	"log"
	"net/http"
	"sync"

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

	sseMu    sync.RWMutex
	sseChans map[chan string]bool
}

// NewServer creates a new Orchestrator API server.
func NewServer(cfg *config.Config, sched *scheduler.Scheduler, pool *qa.Pool, manager *qa.Manager) *Server {
	s := &Server{
		config:    cfg,
		scheduler: sched,
		qaPool:    pool,
		qaManager: manager,
		mux:       http.NewServeMux(),
		sseChans:  make(map[chan string]bool),
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
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.mux.ServeHTTP(w, r)
}
