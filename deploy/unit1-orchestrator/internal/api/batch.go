package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
	"github.com/openwiki/orchestrator/internal/project"
)

type BatchImportRepoItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	GitURL         string `json:"git_url"`
	Branch         string `json:"branch"`
	Tier           string `json:"tier"`
	BusinessDomain string `json:"business_domain"`
	LastReleaseAt  string `json:"last_release_at"`
}

type BatchImportRequest struct {
	BatchName     string                `json:"batch_name"`
	Concurrency   int                   `json:"concurrency"`
	NightModeOnly bool                  `json:"night_mode_only"`
	AutoStart     bool                  `json:"auto_start"`
	Repos         []BatchImportRepoItem `json:"repos"`
}

type ConcurrencyUpdateRequest struct {
	BatchID     string `json:"batch_id"`
	Concurrency int    `json:"concurrency"`
}

func (s *Server) getDB() *db.DB {
	if s.scheduler != nil && s.scheduler.GetDB() != nil {
		return s.scheduler.GetDB()
	}
	return db.GetDB()
}

// handleBatchImport parses repository lists, registers them with manual schedules,
// aggregates microservices into virtual domain projects, and queues batch run tasks.
func (s *Server) handleBatchImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req BatchImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Repos) == 0 {
		http.Error(w, "No repos provided for import", http.StatusBadRequest)
		return
	}

	if req.BatchName == "" {
		req.BatchName = "全行生产发版基线批量构建"
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 2
	}

	batchID := fmt.Sprintf("batch_%d", time.Now().UnixNano())
	database := s.getDB()

	domainRepos := make(map[string][]string)
	importErrors := make([]string, 0)
	importedCount := 0

	for _, item := range req.Repos {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		// G2: business_domain is required for domain aggregation and RBAC.
		item.BusinessDomain = strings.TrimSpace(item.BusinessDomain)
		if item.BusinessDomain == "" {
			importErrors = append(importErrors, fmt.Sprintf("仓库 %s 缺少 business_domain", item.ID))
			continue
		}
		if item.Branch == "" {
			item.Branch = "master"
		}
		localPath := filepath.Join(s.config.ReposBaseDir, item.ID)
		var volumeID string
		if s.volumeManager != nil {
			if vol, err := s.volumeManager.SelectVolumeForRepo(item.ID, 0); err == nil && vol != nil {
				volumeID = vol.ID
				if _, symlinkPath, err := s.volumeManager.CreateRepoPlacement(item.ID, vol.ID); err == nil && symlinkPath != "" {
					localPath = symlinkPath
				}
			}
		}

		repo := &db.Repo{
			ID:             item.ID,
			Name:           item.Name,
			GitURL:         item.GitURL,
			Branch:         item.Branch,
			LocalPath:      localPath,
			WikiDir:        filepath.Join(localPath, "openwiki"),
			Schedule:       "manual", // Force manual schedule to prevent midnight cron spikes
			Tier:           item.Tier,
			BusinessDomain: item.BusinessDomain,
			LastReleaseAt:  item.LastReleaseAt,
			VolumeID:       volumeID,
			Status:         "active",
		}

		existing, _ := database.GetRepo(item.ID)
		if existing == nil {
			_ = database.CreateRepo(repo)
		} else {
			_ = database.UpdateRepo(repo)
		}

		domainRepos[item.BusinessDomain] = append(domainRepos[item.BusinessDomain], item.ID)
		importedCount++
	}

	// Auto-aggregate microservices by business domain into virtual projects
	for domain, repoIDs := range domainRepos {
		s.aggregateDomainProject(database, domain, repoIDs)
	}

	// Create BatchRun and BatchTasks
	batchRun := &db.BatchRun{
		ID:             batchID,
		Name:           req.BatchName,
		TotalCount:     len(req.Repos),
		Status:         "pending",
		MaxConcurrency: req.Concurrency,
		NightModeOnly:  req.NightModeOnly,
	}
	_ = database.CreateBatchRun(batchRun)

	for _, item := range req.Repos {
		task := &db.BatchTask{
			BatchID: batchID,
			RepoID:  item.ID,
			Status:  "pending",
		}
		_ = database.CreateBatchTask(task)
	}

	if req.AutoStart && s.scheduler != nil && s.scheduler.GetBatchScheduler() != nil {
		_ = s.scheduler.GetBatchScheduler().StartBatch(context.Background(), batchID)
	}

	skipped := len(req.Repos) - importedCount
	resp := map[string]interface{}{
		"success":        true,
		"batch_id":       batchID,
		"total_imported": importedCount,
		"status":         "pending",
	}
	if skipped > 0 {
		resp["skipped"] = skipped
		resp["errors"] = importErrors
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// aggregateDomainProject groups repos belonging to the same domain into a Monorepo Project.
func (s *Server) aggregateDomainProject(database *db.DB, domain string, repoIDs []string) {
	projects, _ := database.ListProjects()
	var targetProject *db.Project
	for _, p := range projects {
		if strings.Contains(p.Name, domain) {
			targetProject = &p
			break
		}
	}

	if targetProject == nil {
		h := md5.Sum([]byte(domain))
		slug := hex.EncodeToString(h[:4])
		projID := fmt.Sprintf("proj-%s", slug)
		targetProject = &db.Project{
			ID:          projID,
			Name:        domain + "微服务集群",
			Description: fmt.Sprintf("由基线发版清单自动聚合的 %s 业务域微服务 Monorepo", domain),
			LocalPath:   filepath.Join(s.config.ProjectsBaseDir, projID),
			Status:      "active",
		}
		_ = database.CreateProject(targetProject)
	}

	existingRepos, _ := database.GetProjectRepos(targetProject.ID)
	seen := make(map[string]bool)
	var mappings []db.ProjectRepo
	for _, er := range existingRepos {
		seen[er.ID] = true
		mappings = append(mappings, db.ProjectRepo{
			ProjectID: targetProject.ID,
			RepoID:    er.ID,
			AliasName: er.AliasName,
			Role:      er.Role,
		})
	}

	for _, rid := range repoIDs {
		if !seen[rid] {
			seen[rid] = true
			mappings = append(mappings, db.ProjectRepo{
				ProjectID: targetProject.ID,
				RepoID:    rid,
				AliasName: rid,
				Role:      "service",
			})
		}
	}

	_ = database.SetProjectRepos(targetProject.ID, mappings)
	reposWithAlias, _ := database.GetProjectRepos(targetProject.ID)
	wsMgr := project.NewWorkspaceManager(s.config.ProjectsBaseDir)
	_, _ = wsMgr.PrepareWorkspace(targetProject, reposWithAlias)
}

// handleBatchConcurrency dynamically updates concurrency for rate limiting.
func (s *Server) handleBatchConcurrency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ConcurrencyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Concurrency < 1 {
		req.Concurrency = 1
	}

	if s.scheduler != nil && s.scheduler.GetBatchScheduler() != nil {
		_ = s.scheduler.GetBatchScheduler().SetConcurrency(req.BatchID, req.Concurrency)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"batch_id":    req.BatchID,
		"concurrency": req.Concurrency,
		"updated_at":  time.Now().Format(time.RFC3339),
	})
}

// handleBatchStatus returns slot usage, live progress, and dynamic ETA estimation.
func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	database := s.getDB()
	batchID := r.URL.Query().Get("batch_id")
	if batchID == "" && database != nil {
		latest, err := database.GetLatestActiveBatchRun()
		if err == nil && latest != nil {
			batchID = latest.ID
		}
	}
	if batchID == "" {
		http.Error(w, "batch_id parameter is required and no active batch found", http.StatusBadRequest)
		return
	}

	if s.scheduler == nil || s.scheduler.GetBatchScheduler() == nil {
		http.Error(w, "Batch scheduler not initialized", http.StatusInternalServerError)
		return
	}

	status, err := s.scheduler.GetBatchScheduler().GetDetailedStatus(batchID)
	if err != nil {
		http.Error(w, "Failed to get batch status: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleBatchControl manages start, pause, resume, cancel, and disable-night-mode commands.
func (s *Server) handleBatchControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	database := s.getDB()
	action := filepath.Base(r.URL.Path) // start, pause, resume, cancel, disable-night-mode
	batchID := r.URL.Query().Get("batch_id")
	if batchID == "" && database != nil {
		latest, err := database.GetLatestActiveBatchRun()
		if err == nil && latest != nil {
			batchID = latest.ID
		}
	}
	if batchID == "" {
		http.Error(w, "batch_id parameter is required", http.StatusBadRequest)
		return
	}

	if s.scheduler == nil || s.scheduler.GetBatchScheduler() == nil {
		http.Error(w, "Batch scheduler not initialized", http.StatusInternalServerError)
		return
	}

	bs := s.scheduler.GetBatchScheduler()
	var err error
	switch action {
	case "start", "resume":
		err = bs.ResumeBatch(batchID)
	case "pause":
		err = bs.PauseBatch(batchID)
	case "cancel":
		err = bs.CancelBatch(batchID)
	case "disable-night-mode":
		if database != nil {
			_ = database.SetBatchNightMode(batchID, false)
		}
		err = bs.ResumeBatch(batchID)
	default:
		http.Error(w, "Unknown batch action: "+action, http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, "Failed to execute "+action+": "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"action":  action,
		"batch_id": batchID,
	})
}

// handleDailyTokens returns aggregated daily token telemetry.
func (s *Server) handleDailyTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 30
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}

	authCtx := GetAuthContext(r)
	domainFilter := r.URL.Query().Get("domain")
	if authCtx.Role == "group_leader" && len(authCtx.AuthorizedDomains) > 0 {
		if domainFilter != "" && !authCtx.IsDomainAuthorized(domainFilter) {
			http.Error(w, "Forbidden: unauthorized domain", http.StatusForbidden)
			return
		}
		if domainFilter == "" {
			domainFilter = authCtx.AuthorizedDomains[0]
		}
	}

	database := s.getDB()
	var list []db.DailyTokenUsage
	var err error
	if domainFilter != "" {
		list, err = database.ListDailyTokenUsage(limit, domainFilter)
	} else {
		list, err = database.ListDailyTokenUsage(limit)
	}
	if err != nil {
		http.Error(w, "Failed to list daily tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// handleTokensExport exports token usage reports formatted for Feishu/Lark sheets (TSV) or CSV.
func (s *Server) handleTokensExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "tsv"
	}

	authCtx := GetAuthContext(r)
	domainFilter := r.URL.Query().Get("domain")
	if authCtx.Role == "group_leader" && len(authCtx.AuthorizedDomains) > 0 {
		if domainFilter != "" && !authCtx.IsDomainAuthorized(domainFilter) {
			http.Error(w, "Forbidden: unauthorized domain", http.StatusForbidden)
			return
		}
		if domainFilter == "" {
			domainFilter = authCtx.AuthorizedDomains[0]
		}
	}

	database := s.getDB()
	var list []db.DailyTokenUsage
	var err error
	if domainFilter != "" {
		list, err = database.ListDailyTokenUsage(365, domainFilter)
	} else {
		list, err = database.ListDailyTokenUsage(365)
	}
	if err != nil {
		http.Error(w, "Failed to export token telemetry: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var sb strings.Builder
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=openwiki_daily_tokens.csv")
		// Write UTF-8 BOM for Microsoft Excel compatibility
		sb.WriteString("\xEF\xBB\xBF")
		sb.WriteString("日期,业务域,构建成功仓数,增量跳过仓数,总消耗Token,输入PromptToken,补全CompletionToken,节约Token估算,模型限流重试次数,平均单仓耗时\n")
		for _, u := range list {
			domainName := u.BusinessDomain
			if domainName == "" {
				domainName = "全部业务域"
			}
			avgSec := 0.0
			if u.BuildCount > 0 {
				avgSec = float64(u.TotalDurationSec) / float64(u.BuildCount)
			}
			sb.WriteString(fmt.Sprintf("%s,%s,%d,%d,%d,%d,%d,%d,%d,%.1fs\n",
				u.Date, domainName, u.BuildCount, u.SkippedCount, u.TotalTokens, u.PromptTokens, u.CompletionTokens, u.SavedTokens, u.ThrottledCount, avgSec))
		}
	} else {
		// TSV format for direct paste into Feishu / Lark Base & Docs
		w.Header().Set("Content-Type", "text/tab-separated-values; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=openwiki_daily_tokens.tsv")
		sb.WriteString("日期\t业务域\t构建成功仓数\t增量跳过仓数\t总消耗Token\t输入PromptToken\t补全CompletionToken\t节约Token估算\t模型限流重试次数\t平均单仓耗时\n")
		for _, u := range list {
			domainName := u.BusinessDomain
			if domainName == "" {
				domainName = "全部业务域"
			}
			avgSec := 0.0
			if u.BuildCount > 0 {
				avgSec = float64(u.TotalDurationSec) / float64(u.BuildCount)
			}
			sb.WriteString(fmt.Sprintf("%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1fs\n",
				u.Date, domainName, u.BuildCount, u.SkippedCount, u.TotalTokens, u.PromptTokens, u.CompletionTokens, u.SavedTokens, u.ThrottledCount, avgSec))
		}
	}

	w.Write([]byte(sb.String()))
}
