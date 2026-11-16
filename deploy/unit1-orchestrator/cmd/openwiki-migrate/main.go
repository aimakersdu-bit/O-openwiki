package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/openwiki/orchestrator/internal/config"
	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/storage"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	action := flag.String("action", "plan", "Action to perform: plan, migrate, rollback, cleanup, ingest-stock")
	repoID := flag.String("repo", "", "Repository ID to migrate")
	toVolume := flag.String("to", "", "Target volume ID for migration")
	migrationID := flag.Int64("id", 0, "Migration ID for rollback/cleanup")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.DBDSN == "" {
		log.Fatalf("DB_DSN is required (MySQL DSN)")
	}
	if err := db.InitDBWithOptions(cfg.DBDSN, cfg.DBAutoMigrate); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.CloseDB()

	database := db.GetDB()
	linksDir := filepath.Join(filepath.Dir(cfg.ReposBaseDir), "disk0", "links", "repos")
	vm := storage.NewVolumeManager(database, linksDir)
	migrator := storage.NewMigrator(database, vm, nil, linksDir)

	switch *action {
	case "ingest-stock":
		// Adopt stock repositories in ReposBaseDir onto vol1 and create links
		log.Println("Ingesting stock repositories into vol1 and establishing symlinks...")
		vol1, err := vm.RegisterVolume("vol1", cfg.ReposBaseDir)
		if err != nil {
			log.Fatalf("Register vol1 failed: %v", err)
		}
		repos, err := database.ListRepos()
		if err != nil {
			log.Fatalf("List repos failed: %v", err)
		}
		ingested := 0
		for _, r := range repos {
			if r.VolumeID == "" {
				// Create symlink
				_, symlinkPath, err := vm.CreateRepoPlacement(r.ID, vol1.ID)
				if err != nil {
					log.Printf("Warning: failed to create placement for repo %s: %v", r.ID, err)
					continue
				}
				_ = database.UpdateRepoVolumeAndSize(r.ID, vol1.ID, r.SizeBytes)
				r.LocalPath = symlinkPath
				_ = database.UpdateRepo(&r)
				ingested++
			}
		}
		log.Printf("Successfully ingested %d stock repositories into vol1 with symlink indirection.", ingested)

	case "plan":
		plan, err := migrator.GenerateRebalancePlan()
		if err != nil {
			log.Fatalf("Generate rebalance plan failed: %v", err)
		}
		if len(plan) == 0 {
			fmt.Println("Storage volumes are balanced. No migrations recommended.")
			return
		}
		fmt.Printf("=== Recommended Rebalance Plan (%d actions) ===\n", len(plan))
		for i, act := range plan {
			fmt.Printf("[%d] Repo: %s (%s) | %s -> %s | Size: %.1f MB\n    Reason: %s\n",
				i+1, act.RepoID, act.RepoName, act.FromVolumeID, act.ToVolumeID, float64(act.SizeBytes)/(1024*1024), act.Reason)
		}

	case "migrate":
		if *repoID == "" || *toVolume == "" {
			log.Fatalf("Both -repo and -to must be specified for migrate action")
		}
		log.Printf("Starting online migration of repo %s to volume %s...", *repoID, *toVolume)
		mig, err := migrator.Migrate(context.Background(), *repoID, *toVolume)
		if err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		log.Printf("Migration completed successfully! ID=%d, Status=%s", mig.ID, mig.Status)

	case "rollback":
		if *migrationID <= 0 {
			log.Fatalf("-id must be specified for rollback action")
		}
		if err := migrator.Rollback(context.Background(), *migrationID); err != nil {
			log.Fatalf("Rollback failed: %v", err)
		}
		log.Printf("Migration %d successfully rolled back.", *migrationID)

	case "cleanup":
		if *migrationID <= 0 {
			log.Fatalf("-id must be specified for cleanup action")
		}
		if err := migrator.CleanupRetained(*migrationID); err != nil {
			log.Fatalf("Cleanup failed: %v", err)
		}
		log.Printf("Old replica for migration %d successfully cleaned up.", *migrationID)

	default:
		log.Fatalf("Unknown action %q. Supported actions: plan, migrate, rollback, cleanup, ingest-stock", *action)
	}
}
