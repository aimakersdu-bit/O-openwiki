package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/openwiki/portal/internal/api"
	"github.com/openwiki/portal/internal/config"
	"github.com/openwiki/portal/internal/db"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	publicDir := flag.String("public", "public", "Path to public static frontend directory")
	flag.Parse()

	log.Println("Starting openwiki-portal (Unit 2)...")

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Printf("Warning: failed to load config file: %v. Using default settings.", err)
		cfg = config.DefaultConfig()
	}

	// Initialize SQLite Database
	if err := db.InitDB(cfg.DBPath); err != nil {
		log.Fatalf("Failed to initialize portal database at %s: %v", cfg.DBPath, err)
	}
	defer db.CloseDB()
	log.Printf("Portal database initialized at %s", cfg.DBPath)

	// Resolve public static frontend directory dynamically with fallback candidates
	publicPath := *publicDir
	if _, err := os.Stat(filepath.Join(publicPath, "login.html")); os.IsNotExist(err) {
		execPath, _ := os.Executable()
		execDir := filepath.Dir(execPath)
		cwd, _ := os.Getwd()

		candidates := []string{
			filepath.Join(cwd, "deploy", "unit2-portal", "public"),
			filepath.Join(cwd, "unit2-portal", "public"),
			filepath.Join(execDir, "public"),
			filepath.Join(execDir, "..", "unit2-portal", "public"),
			filepath.Join(execDir, "..", "public"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(filepath.Join(c, "login.html")); err == nil {
				publicPath = c
				break
			}
		}
	}
	log.Printf("Portal serving static web assets from: %s", publicPath)
	publicFS := http.FileServer(http.Dir(publicPath))

	// Create Portal API server
	server := api.NewServer(cfg, publicFS)

	addr := fmt.Sprintf(":%d", cfg.Port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	// Start server in background
	go func() {
		log.Printf("Portal HTTP Server listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down portal server gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Println("Portal server stopped.")
}
