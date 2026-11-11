package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
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
	TriCode        string `json:"tri_code,omitempty"`
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

	domainItems := make(map[string][]BatchImportRepoItem)
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

		domainItems[item.BusinessDomain] = append(domainItems[item.BusinessDomain], item)
		importedCount++
	}

	// Auto-aggregate microservices by business domain into semantic virtual projects
	aggregatedProjects := make([]*db.Project, 0, len(domainItems))
	for domain, items := range domainItems {
		if proj := s.aggregateDomainProject(database, domain, items); proj != nil {
			aggregatedProjects = append(aggregatedProjects, proj)
		}
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
		"success":             true,
		"batch_id":            batchID,
		"total_imported":      importedCount,
		"status":              "pending",
		"aggregated_projects": aggregatedProjects,
	}
	if skipped > 0 {
		resp["skipped"] = skipped
		resp["errors"] = importErrors
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

var (
	domainPrefixCodeRe  = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9]{1,5})[-_/\s:：]`)
	domainBracketCodeRe = regexp.MustCompile(`[(\[【]\s*([A-Za-z][A-Za-z0-9]{1,5})\s*[)\]】]`)
	pureCodeRe          = regexp.MustCompile(`^[A-Za-z]{2,6}$`)
	triLetterSegRe      = regexp.MustCompile(`^[A-Za-z]{3}$`)
	flatPrefixCodeRe    = regexp.MustCompile(`^([A-Za-z]{3})[-_]`)
	genericSegStopWords = map[string]bool{
		"GIT": true, "COM": true, "ORG": true, "NET": true, "SVC": true,
		"APP": true, "API": true, "WEB": true, "DEV": true, "UAT": true,
		"PRD": true, "SRC": true, "LIB": true, "SRV": true, "SYS": true,
	}
)

// extractTriCode resolves a domain's three-letter (or short system) code from
// explicit tri_code, domain naming conventions, or repo ID / Git URL hierarchy (e.g. bxbank/DEO/xone-auth -> DEO).
func extractTriCode(domain string, items []BatchImportRepoItem) string {
	for _, it := range items {
		code := strings.ToUpper(strings.TrimSpace(it.TriCode))
		if code != "" && pureCodeRe.MatchString(code) {
			return code
		}
	}

	if code := extractTriCodeFromDomainText(domain); code != "" {
		return code
	}

	// Count 3-letter hierarchy segments in repo IDs or Git URL paths (e.g. bxbank/DEO/xone-auth -> DEO)
	segCounts := make(map[string]int)
	for _, it := range items {
		for _, candidate := range extractPathTriCodeCandidates(it.ID, it.GitURL) {
			segCounts[candidate]++
		}
	}
	if best := pickMostFrequentCode(segCounts); best != "" {
		return best
	}

	// Fallback: shared 3-letter prefix across all repo IDs in the domain (e.g. cbs-order, cbs-pay -> CBS)
	var sharedPrefix string
	for _, it := range items {
		baseID := it.ID
		if idx := strings.LastIndex(baseID, "/"); idx != -1 {
			baseID = baseID[idx+1:]
		}
		m := flatPrefixCodeRe.FindStringSubmatch(baseID)
		if len(m) < 2 {
			sharedPrefix = ""
			break
		}
		cand := strings.ToUpper(m[1])
		if genericSegStopWords[cand] {
			sharedPrefix = ""
			break
		}
		if sharedPrefix == "" {
			sharedPrefix = cand
		} else if sharedPrefix != cand {
			sharedPrefix = ""
			break
		}
	}
	return sharedPrefix
}

func extractTriCodeFromDomainText(domain string) string {
	d := strings.TrimSpace(domain)
	if d == "" {
		return ""
	}
	if pureCodeRe.MatchString(d) {
		return strings.ToUpper(d)
	}
	if m := domainPrefixCodeRe.FindStringSubmatch(d); len(m) >= 2 {
		return strings.ToUpper(m[1])
	}
	if m := domainBracketCodeRe.FindStringSubmatch(d); len(m) >= 2 {
		return strings.ToUpper(m[1])
	}
	return ""
}

func extractPathTriCodeCandidates(repoID, gitURL string) []string {
	seen := make(map[string]bool)
	var candidates []string

	checkParts := func(raw string, isURL bool) {
		clean := strings.TrimSpace(raw)
		if clean == "" {
			return
		}
		if isURL {
			if idx := strings.Index(clean, "://"); idx != -1 {
				clean = clean[idx+3:]
				if slash := strings.Index(clean, "/"); slash != -1 {
					clean = clean[slash+1:]
				} else {
					clean = ""
				}
			} else if colon := strings.Index(clean, ":"); colon != -1 {
				clean = clean[colon+1:]
			}
			clean = strings.TrimSuffix(clean, ".git")
		}
		parts := strings.Split(strings.Trim(clean, "/"), "/")
		if len(parts) < 2 {
			return
		}
		// Inspect parent segments (not leaf repo name), checking nearest parent first
		for i := len(parts) - 2; i >= 0; i-- {
			seg := strings.TrimSpace(parts[i])
			if triLetterSegRe.MatchString(seg) {
				up := strings.ToUpper(seg)
				if !genericSegStopWords[up] && !seen[up] {
					seen[up] = true
					candidates = append(candidates, up)
				}
			}
		}
	}

	checkParts(repoID, false)
	checkParts(gitURL, true)
	return candidates
}

func pickMostFrequentCode(counts map[string]int) string {
	bestCode := ""
	bestCount := 0
	for code, count := range counts {
		if count > bestCount || (count == bestCount && (bestCode == "" || code < bestCode)) {
			bestCode = code
			bestCount = count
		}
	}
	return bestCode
}

func cleanDomainLabel(domain, triCode string) string {
	label := strings.TrimSpace(domain)
	if triCode == "" || label == "" {
		return label
	}
	upperLabel := strings.ToUpper(label)
	upperCode := strings.ToUpper(triCode)
	for _, sep := range []string{"-", "_", "/", ":", "：", " "} {
		prefix := upperCode + sep
		if strings.HasPrefix(upperLabel, prefix) {
			trimmed := strings.TrimSpace(label[len(prefix):])
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return label
}

func formatAggregatedProjectMeta(domain, triCode string) (projID, projName, domainLabel string) {
	domainLabel = cleanDomainLabel(domain, triCode)
	if triCode != "" {
		projID = "proj-" + strings.ToLower(triCode)
		if domainLabel != "" && !strings.EqualFold(domainLabel, triCode) {
			projName = fmt.Sprintf("%s-%s微服务集群", triCode, domainLabel)
		} else {
			projName = fmt.Sprintf("%s微服务集群", triCode)
		}
		return projID, projName, domainLabel
	}

	h := md5.Sum([]byte(domain))
	slug := hex.EncodeToString(h[:4])
	projID = fmt.Sprintf("proj-%s", slug)
	projName = domain + "微服务集群"
	return projID, projName, domainLabel
}

// aggregateDomainProject groups repos belonging to the same domain into a Monorepo Project
// using a semantic tri-code ID/name when available (e.g. proj-deo / DEO-数字员工微服务集群).
func (s *Server) aggregateDomainProject(database *db.DB, domain string, items []BatchImportRepoItem) *db.Project {
	triCode := extractTriCode(domain, items)
	projID, projName, _ := formatAggregatedProjectMeta(domain, triCode)

	projects, _ := database.ListProjects()
	var targetProject *db.Project
	for i := range projects {
		p := projects[i]
		if (triCode != "" && (strings.EqualFold(p.TriCode, triCode) || p.ID == projID)) ||
			p.BusinessDomain == domain ||
			strings.Contains(p.Name, domain) {
			copyP := p
			targetProject = &copyP
			break
		}
	}

	mcpKey := projID
	if triCode != "" {
		mcpKey = triCode
	}

	if targetProject == nil {
		targetProject = &db.Project{
			ID:             projID,
			Name:           projName,
			TriCode:        triCode,
			BusinessDomain: domain,
			Description:    fmt.Sprintf("由基线发版清单自动聚合的 %s 业务域微服务 Monorepo (MCP Key: %s)", domain, mcpKey),
			LocalPath:      filepath.Join(s.config.ProjectsBaseDir, projID),
			Status:         "active",
		}
		_ = database.CreateProject(targetProject)
	} else {
		needUpdate := false
		if targetProject.BusinessDomain == "" && domain != "" {
			targetProject.BusinessDomain = domain
			needUpdate = true
		}
		if targetProject.TriCode == "" && triCode != "" {
			targetProject.TriCode = triCode
			if !strings.Contains(strings.ToUpper(targetProject.Name), triCode) {
				targetProject.Name = projName
			}
			needUpdate = true
		}
		if needUpdate {
			_ = database.UpdateProject(targetProject)
		}
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

	for _, item := range items {
		rid := item.ID
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
	return targetProject
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

// handleBatchControl manages start, pause, resume, cancel, disable-night-mode, and retry-failed commands.
func (s *Server) handleBatchControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	database := s.getDB()
	action := filepath.Base(r.URL.Path) // start, pause, resume, cancel, disable-night-mode, retry-failed
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
	var retriedCount int64
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
	case "retry-failed":
		var retryReq struct {
			RepoIDs []string `json:"repo_ids"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&retryReq)
		}
		retriedCount, err = bs.RetryFailedTasks(context.Background(), batchID, retryReq.RepoIDs)
	default:
		http.Error(w, "Unknown batch action: "+action, http.StatusBadRequest)
		return
	}

	if err != nil {
		if strings.Contains(err.Error(), "no failed or pending tasks") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "Failed to execute "+action+": "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success":  true,
		"action":   action,
		"batch_id": batchID,
	}
	if action == "retry-failed" {
		resp["retried_count"] = retriedCount
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
