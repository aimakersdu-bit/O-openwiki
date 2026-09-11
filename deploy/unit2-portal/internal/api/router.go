package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openwiki/portal/internal/config"
)

type Server struct {
	config     *config.Config
	httpClient *http.Client
	mux        *http.ServeMux
	publicFS   http.Handler
}

func NewServer(cfg *config.Config, publicFS http.Handler) *Server {
	s := &Server{
		config: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		mux:      http.NewServeMux(),
		publicFS: publicFS,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Redirect root / to /portal/
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/portal/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// Authentication
	s.mux.HandleFunc("/portal/login", s.handleLogin)
	s.mux.HandleFunc("/portal/logout", s.handleLogout)
	s.mux.HandleFunc("/portal/me", s.handleMe)

	// Portal data APIs
	s.mux.HandleFunc("/portal/repos", s.handleRepos)
	s.mux.HandleFunc("/portal/sessions", s.handleSessions)
	s.mux.HandleFunc("/portal/qa/sessions", s.handleQASessions)
	s.mux.HandleFunc("/portal/qa/messages", s.handleQAMessages)
	s.mux.HandleFunc("/portal/build/trigger", s.handleBuildTrigger)
	s.mux.HandleFunc("/portal/audit", s.handleAudit)

	// Health check
	s.mux.HandleFunc("/portal/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"unit2-portal"}`))
	})

	// Static web portal assets
	if s.publicFS != nil {
		s.mux.Handle("/portal/", http.StripPrefix("/portal/", s.publicFS))
	}

	// Serve static Wiki pages under /wiki/ in standalone dev execution
	if s.config.StaticOutputDir != "" {
		wikiFS := http.StripPrefix("/wiki/", http.FileServer(http.Dir(s.config.StaticOutputDir)))
		s.mux.HandleFunc("/wiki/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			wikiFS.ServeHTTP(w, r)
		})
	}

	// Intercept /api/graph to serve static graph JSON based on Referer or query parameter
	s.mux.HandleFunc("/api/graph", s.handleApiGraph)

	// Reverse proxy /api/ to Orchestrator URL for standalone dev execution
	if orchURL, err := url.Parse(s.config.OrchestratorURL); err == nil && orchURL.Scheme != "" {
		proxy := httputil.NewSingleHostReverseProxy(orchURL)
		proxy.FlushInterval = -1 // Immediate flush for SSE streaming
		s.mux.Handle("/api/", proxy)
	}
}

func (s *Server) handleApiGraph(w http.ResponseWriter, r *http.Request) {
	if s.config.StaticOutputDir == "" {
		http.Error(w, "Static output directory not configured", http.StatusNotFound)
		return
	}

	repoID := r.URL.Query().Get("repo")
	if repoID == "" {
		if ref := r.Referer(); ref != "" {
			if u, err := url.Parse(ref); err == nil {
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) >= 2 && parts[0] == "wiki" {
					repoID = parts[1]
				}
			}
		}
	}

	if repoID == "" {
		entries, err := os.ReadDir(s.config.StaticOutputDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					graphPath := filepath.Join(s.config.StaticOutputDir, entry.Name(), "api", "graph")
					if _, err := os.Stat(graphPath); err == nil {
						repoID = entry.Name()
						break
					}
				}
			}
		}
	}

	if repoID == "" {
		http.Error(w, "Graph not found", http.StatusNotFound)
		return
	}

	graphFile := filepath.Join(s.config.StaticOutputDir, repoID, "api", "graph")
	data, err := os.ReadFile(graphFile)
	if err != nil {
		http.Error(w, "Failed to read graph file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(data)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.mux.ServeHTTP(w, r)
}
