package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	// Create file server handler for static frontend
	publicFS := http.FileServer(http.Dir(*publicDir))

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
