package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openwiki/orchestrator/internal/api"
	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/qa"
	"github.com/openwiki/orchestrator/internal/scheduler"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	log.Println("Starting openwiki-orchestrator (Unit 1)...")

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("Warning: failed to load config file: %v. Using default settings.", err)
		cfg = config.DefaultConfig()
	}

	// Initialize SQLite Database
	if err := db.InitDB(cfg.DBPath); err != nil {
		log.Fatalf("Failed to initialize database at %s: %v", cfg.DBPath, err)
	}
	defer db.CloseDB()
	log.Printf("Database initialized at %s", cfg.DBPath)

	// Create builder, runner, pool, scheduler
	builder := scheduler.NewBuilder(cfg.OpenwikiCLI, cfg.VendorAssetsDir, cfg.OpenwikiDistDir, cfg.StaticOutputDir)
	sched := scheduler.NewScheduler(builder)

	qaRunner := qa.NewRunner(cfg.OpenwikiCLI, time.Duration(cfg.QATimeoutSec)*time.Second)
	qaPool := qa.NewPool(qaRunner, cfg.MaxConcurrentQA)

	// Start cron scheduler
	if err := sched.Start(); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}
	defer sched.Stop()
	log.Println("Cron scheduler started successfully")

	// Create API router
	server := api.NewServer(cfg, sched, qaPool)

	addr := cfg.ListenAddr
	if addr == "" {
		addr = ":3000"
	}
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	// Listen in goroutine
	go func() {
		log.Printf("Orchestrator HTTP API listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown handling
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down orchestrator server gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Println("Orchestrator stopped.")
}
