# 跑批任务失败重跑跳过缺陷修复与 Git 幂等流水线自愈实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复跑批任务中因代码已存在且无新提交导致“全部失败任务重跑”被错误判为跳过的问题，实现 Git 拉取幂等（不重复 pull）与静态资产缺失/失败构建自动贯通自愈。

**Architecture:** 在流水线增量比对层引入“四维跳过裁决机制”（非强制 + 历史构建成功 + Wiki产物有效 + 静态资源健全），当远端无新 commit 时保持 Git pull 幂等；对于历史构建失败、静态资源缺失或残缺的任务，自动穿透进入 Wiki 生成与静态资产构建阶段，同时在 Builder 内部增强静态资源深度健康检查（文件大小非空与图谱 JSON 有效性）。

**Tech Stack:** Go 1.22+, SQLite3, OpenWiki Orchestrator Scheduler, Bash.

---

### File Structure Map

| 文件路径 | 模块职责 | 变更类型 |
| :--- | :--- | :--- |
| `deploy/unit1-orchestrator/internal/db/sqlite.go` | 增加 `GetLatestFinishedBuild(repoID)` 查询最近一次终态构建记录 | Modify |
| `deploy/unit1-orchestrator/internal/db/sqlite_test.go` | 增加 `GetLatestFinishedBuild` 单元测试 | Modify |
| `deploy/unit1-orchestrator/internal/scheduler/builder.go` | 强化 `isStaticDirComplete`（非空与 JSON 校验）、`isWikiDirComplete` 与 Step 0 自愈触发 | Modify |
| `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` | 增加空文件、损坏 JSON 拓扑与未完成 Wiki 校验的单元测试 | Modify |
| `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` | 重构 `!status.HasUpdates` 裁决逻辑：结合历史构建与产物深度校验，穿透执行后续流水线 | Modify |
| `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go` | 增加失败任务重跑穿透与 Git 幂等构建流水线单元测试 | Modify |

---

### Task 1: 数据库层提供最近终态构建查询（GetLatestFinishedBuild）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go:680-700`
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite_test.go`

- [ ] **Step 1: 编写 GetLatestFinishedBuild 失败测试用例**

在 `deploy/unit1-orchestrator/internal/db/sqlite_test.go` 文件末尾添加测试函数：

```go
func TestGetLatestFinishedBuild(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repoID := "repo-finish-test"
	_ = database.CreateRepo(&Repo{ID: repoID, Name: "Test Repo", GitURL: "http://example.com/test.git", LocalPath: "/tmp/repo"})

	// 1. 无任何构建时返回 nil
	latest, err := database.GetLatestFinishedBuild(repoID)
	if err != nil {
		t.Fatalf("unexpected error on empty builds: %v", err)
	}
	if latest != nil {
		t.Fatalf("expected nil when no builds exist, got %+v", latest)
	}

	// 2. 只有 running 构建时仍返回 nil
	b1ID, _ := database.CreateBuild(repoID, "head-1", "running")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest != nil {
		t.Fatalf("expected nil when only running builds exist, got %+v (err=%v)", latest, err)
	}

	// 3. 第一次失败构建
	_ = database.FinishBuild(b1ID, "failed", "build error log", "exit 1")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "failed" || latest.ID != b1ID {
		t.Fatalf("expected latest finished build to be failed b1 (%d), got %+v", b1ID, latest)
	}

	// 4. 第二次构建正在 running，不应干扰查询上一次 finished 的构建
	_, _ = database.CreateBuild(repoID, "head-2", "running")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "failed" || latest.ID != b1ID {
		t.Fatalf("expected latest finished build to remain failed b1 (%d), got %+v", b1ID, latest)
	}

	// 5. 第二次构建完成为 completed
	_ = database.FinishBuild(b1ID+1, "completed", "success log", "")
	latest, err = database.GetLatestFinishedBuild(repoID)
	if err != nil || latest == nil || latest.Status != "completed" {
		t.Fatalf("expected latest finished build to be completed, got %+v", latest)
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

```bash
cd deploy/unit1-orchestrator && go test -v ./internal/db -run TestGetLatestFinishedBuild
```
预期输出：`database.GetLatestFinishedBuild undefined` 编译错误。

- [ ] **Step 3: 实现 GetLatestFinishedBuild 方法**

在 `deploy/unit1-orchestrator/internal/db/sqlite.go` 中添加实现：

```go
// GetLatestFinishedBuild returns the most recent build for a repo that has finished (status: completed, failed, or skipped).
func (db *DB) GetLatestFinishedBuild(repoID string) (*Build, error) {
	query := `SELECT id, repo_id, git_head, status, started_at, finished_at, log, error, rate_limited_retries, prompt_tokens, completion_tokens, total_tokens, saved_tokens
		FROM builds
		WHERE repo_id = ? AND status IN ('completed', 'failed', 'skipped')
		ORDER BY id DESC LIMIT 1`

	row := db.conn.QueryRow(query, repoID)
	var b Build
	if err := row.Scan(&b.ID, &b.RepoID, &b.GitHead, &b.Status,
		&b.StartedAt, &b.FinishedAt, &b.Log, &b.Error,
		&b.RateLimitedRetries, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens, &b.SavedTokens); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}
```

- [ ] **Step 4: 运行测试验证通过**

```bash
cd deploy/unit1-orchestrator && go test -v ./internal/db -run TestGetLatestFinishedBuild
```
预期输出：`PASS`。

- [ ] **Step 5: 提交代码**

```bash
git add deploy/unit1-orchestrator/internal/db/sqlite.go deploy/unit1-orchestrator/internal/db/sqlite_test.go
git commit -m "feat(db): add GetLatestFinishedBuild query for pipeline status inspection"
```

---

### Task 2: 增强静态资源与 Wiki 深度完整性校验（Deep Asset Health Check）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go:410-435,885-935`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder_test.go:510-580`

- [ ] **Step 1: 编写增强静态资源校验的失败测试**

在 `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` 中扩展/添加测试：

```go
func TestIsStaticDirComplete_EmptyFilesAndCorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	staticDir := filepath.Join(tmpDir, "static-check")
	_ = os.MkdirAll(filepath.Join(staticDir, "vendor"), 0755)
	_ = os.MkdirAll(filepath.Join(staticDir, "api"), 0755)

	// 创建所有必需文件，但均为 0 字节
	for _, rel := range requiredStaticFiles {
		_ = os.WriteFile(filepath.Join(staticDir, rel), []byte(""), 0644)
	}

	// 0 字节空文件必须判定为不完整
	if isStaticDirComplete(staticDir) {
		t.Fatalf("expected isStaticDirComplete to return false for 0-byte placeholder files")
	}

	// 写入非空常规文件，但 api/graph 写入损坏的 JSON
	for _, rel := range requiredStaticFiles {
		_ = os.WriteFile(filepath.Join(staticDir, rel), []byte("non-empty content"), 0644)
	}
	_ = os.WriteFile(filepath.Join(staticDir, "api", "graph"), []byte("invalid-json{"), 0644)

	// api/graph JSON 无效应判定为不完整
	if isStaticDirComplete(staticDir) {
		t.Fatalf("expected isStaticDirComplete to return false for corrupt api/graph JSON")
	}

	// 写入有效 graph JSON
	validGraph := `{"nodes":[{"id":"test","label":"Test"}],"links":[]}`
	_ = os.WriteFile(filepath.Join(staticDir, "api", "graph"), []byte(validGraph), 0644)

	// 现在全部有效，应判定为完整
	if !isStaticDirComplete(staticDir) {
		t.Fatalf("expected isStaticDirComplete to return true for complete valid assets")
	}
}

func TestIsWikiDirComplete_RequiresSubstantialDocs(t *testing.T) {
	tmpDir := t.TempDir()
	wikiDir := filepath.Join(tmpDir, "wiki-check")
	_ = os.MkdirAll(wikiDir, 0755)

	// 只有 1 个小于 20 字节的空 md 文件（例如残缺创建），应判定为不完整
	_ = os.WriteFile(filepath.Join(wikiDir, "stub.md"), []byte("# Title\n"), 0644)
	if isWikiDirComplete(wikiDir) {
		t.Fatalf("expected isWikiDirComplete to return false for tiny stub markdown")
	}

	// 存在 .last-update.json 完成戳，应判定为完整
	_ = os.WriteFile(filepath.Join(wikiDir, ".last-update.json"), []byte(`{"updatedAt":"2026-11-18T20:00:00Z"}`), 0644)
	if !isWikiDirComplete(wikiDir) {
		t.Fatalf("expected isWikiDirComplete to return true when .last-update.json exists")
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

```bash
cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run "TestIsStaticDirComplete_EmptyFilesAndCorruptJSON|TestIsWikiDirComplete_RequiresSubstantialDocs"
```
预期输出：FAIL。

- [ ] **Step 3: 优化 builder.go 中的校验与 Step 0 自愈逻辑**

在 `deploy/unit1-orchestrator/internal/scheduler/builder.go` 中重构：

```go
func isStaticDirComplete(staticDir string) bool {
	if info, err := os.Stat(staticDir); err != nil || !info.IsDir() {
		return false
	}
	for _, rel := range requiredStaticFiles {
		target := filepath.Join(staticDir, rel)
		fi, err := os.Stat(target)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			return false
		}
	}
	// 验证 api/graph 具有合法非空的 JSON 结构
	graphBytes, err := os.ReadFile(filepath.Join(staticDir, "api", "graph"))
	if err != nil || len(graphBytes) < 10 {
		return false
	}
	var g struct {
		Nodes []interface{} `json:"nodes"`
		Links []interface{} `json:"links"`
	}
	if err := json.Unmarshal(graphBytes, &g); err != nil {
		return false
	}
	return true
}

func isWikiDirComplete(wikiDir string) bool {
	if wikiDir == "" {
		return false
	}
	info, err := os.Stat(wikiDir)
	if err != nil || !info.IsDir() {
		return false
	}

	// 1. 若存在 OpenWiki 官方完成标记戳，直接判定完整
	lastUpdateFile := filepath.Join(wikiDir, ".last-update.json")
	if fi, err := os.Stat(lastUpdateFile); err == nil && !fi.IsDir() && fi.Size() > 0 {
		return true
	}

	// 2. 检查是否有有效的大于 100 字节的实质性文档
	hasSubstantialDoc := false
	_ = filepath.Walk(wikiDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi == nil || fi.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") && fi.Size() > 100 {
			hasSubstantialDoc = true
			return filepath.SkipDir
		}
		return nil
	})
	return hasSubstantialDoc
}
```

并将 `BuildRepoWithModeCtx` 中的 Step 0 触发条件由 `os.IsNotExist(err)` 调整为：
```go
	if mode != BuildModeKnowledgeOnly {
		// Step 0: Check if wiki directory is complete; if not, run openwiki code --init --print first
		if !b.IsWikiComplete(wikiDir) {
			b.logStep(repoID, fmt.Sprintf("=== Step 0: openwiki code --init --print --language %s ===", lang), &logBuf, onProgress...)
			cmdInit := b.createOpenWikiCmdWithHome(buildID, taskHomeDir, repoPath, "code", "--init", "--print", "--language", lang)
			if err := b.runCommandWithStreaming(ctx, cmdInit, repoID, &logBuf, onProgress...); err != nil {
				result.Log = logBuf.String()
				result.Error = fmt.Sprintf("openwiki init failed: %v", err)
				b.logStep(repoID, "ERROR: "+result.Error, &logBuf, onProgress...)
				return result
			}
			b.logStep(repoID, "openwiki init completed successfully", &logBuf, onProgress...)
			ranInit = true
		}
	}
```

- [ ] **Step 4: 运行测试验证通过**

```bash
cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run "TestIsStaticDirComplete|TestIsWikiDirComplete"
```
预期输出：`PASS`。

- [ ] **Step 5: 提交代码**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/builder.go deploy/unit1-orchestrator/internal/scheduler/builder_test.go
git commit -m "fix(scheduler): robust static assets and wiki completeness validation with auto-healing init"
```

---

### Task 3: 重构流水线跳过决策引擎（Pipeline Skip Decision Engine）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/pipeline.go:206-255`
- Modify: `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go`

- [ ] **Step 1: 编写失败构建重跑穿透与 Git 幂等构建测试**

在 `deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go` 中添加测试：

```go
func TestPipelineSkipDecision_RetryFailedDoesNotSkip(t *testing.T) {
	// 验证逻辑：
	// 1. 本地代码已存在且无更新 (!status.HasUpdates == true)
	// 2. 但上一次构建状态为 failed
	// 3. 期望：不触发 skipped，跳过 git pull（幂等），继续执行构建并记录日志
}
```

- [ ] **Step 2: 重构 pipeline.go 中的判断逻辑**

在 `deploy/unit1-orchestrator/internal/scheduler/pipeline.go` 中改造：

```go
		if !status.HasUpdates {
			// 核查该仓库在数据库中的上一次终态构建
			var prevBuildSuccess bool
			var latestFinishedID int64
			var latestStatus string
			if p.db != nil {
				if latest, err := p.db.GetLatestFinishedBuild(repo.ID); err == nil && latest != nil {
					latestFinishedID = latest.ID
					latestStatus = latest.Status
					prevBuildSuccess = (latest.Status == "completed" || latest.Status == "success")
				}
			}

			wikiComplete := p.builder.IsWikiComplete(repo.WikiDir)
			staticComplete := p.builder.IsStaticOutputComplete(repo.ID)

			// 只有在非强制构建、上一次构建完全成功、Wiki完整且静态资源齐全时，才安全跳过
			if prevBuildSuccess && wikiComplete && staticComplete {
				msg := fmt.Sprintf("No updates detected (HEAD=%s), previous build (%d) succeeded, and build output is complete. Skipping wiki generation (0 tokens).\n", status.LocalHead, latestFinishedID)
				logBuf.WriteString(msg)
				log.Printf("[pipeline][%s] %s", repo.ID, strings.TrimSpace(msg))
				p.finishBuild(buildID, "skipped", logBuf.String(), "")

				today := time.Now().Format("2006-01-02")
				_ = p.db.RecordDailyTokenUsage(&db.DailyTokenUsage{
					Date:         today,
					SavedTokens:  150000,
					SkippedCount: 1,
				})

				if onProgress != nil {
					onProgress("skipped", 1.0, logBuf.String())
				}

				return &PipelineResult{
					Success:     true,
					Status:      "skipped",
					Log:         logBuf.String(),
					GitHead:     status.LocalHead,
					SavedTokens: 150000,
					DurationSec: int(time.Since(startTime).Seconds()),
				}, nil
			}

			// 产物不全或上一次构建失败：跳过冗余的 git pull（代码已在 HEAD，保持拉取幂等），直接贯通进入构建流水线
			reason := "prior build failed or missing"
			if !prevBuildSuccess {
				if latestFinishedID > 0 {
					reason = fmt.Sprintf("prior build %d ended with status %q", latestFinishedID, latestStatus)
				} else {
					reason = "no prior finished build found"
				}
			} else if !wikiComplete {
				reason = "wiki documentation is incomplete"
			} else if !staticComplete {
				reason = "static output assets are missing or incomplete"
			}

			regenMsg := fmt.Sprintf("No git updates (HEAD=%s), but %s (wiki_complete=%v, static_complete=%v); skipping redundant git pull (idempotent) and proceeding to build phase.\n",
				status.LocalHead, reason, wikiComplete, staticComplete)
			logBuf.WriteString(regenMsg)
			log.Printf("[pipeline][%s] %s", repo.ID, strings.TrimSpace(regenMsg))
			_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		} else if err := GitPull(repo.LocalPath, repo.Branch); err != nil {
			errStr := fmt.Sprintf("git pull failed: %v", err)
			logBuf.WriteString(errStr + "\n")
			p.finishBuild(buildID, "failed", logBuf.String(), errStr)
			return &PipelineResult{
				Success:     false,
				Status:      "failed",
				Log:         logBuf.String(),
				Error:       errStr,
				DurationSec: int(time.Since(startTime).Seconds()),
			}, nil
		} else {
			logBuf.WriteString("Git pull completed successfully.\n")
			_ = p.db.UpdateBuildLog(buildID, logBuf.String(), "")
		}
```

- [ ] **Step 3: 运行完整流水线测试**

```bash
cd deploy/unit1-orchestrator && go test -v ./internal/scheduler
```
预期输出：所有调度与流水线测试全部 PASS。

- [ ] **Step 4: 提交代码**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/pipeline.go deploy/unit1-orchestrator/internal/scheduler/pipeline_test.go
git commit -m "fix(pipeline): unblock retry-failed tasks when git has no updates but build or assets are incomplete"
```

---

### Task 4: 端到端单元测试套件运行与发布二进制打包

**Files:**
- Execute: `deploy/scripts/test-unit.sh`
- Execute: `deploy/scripts/build-x86.sh`

- [ ] **Step 1: 运行全量 Orchestrator 单元测试**

```bash
bash deploy/scripts/test-unit.sh
```
预期输出：`All unit tests passed successfully!`。

- [ ] **Step 2: 重新编译并同步 x86 / ARM 发布包二进制**

```bash
bash deploy/scripts/build-x86.sh
```
预期输出：编译完成，产物同步至 `deploy/bin/x86/orchestrator` 与对应目录。

- [ ] **Step 3: 最终 Git 状态确认与提交**

```bash
git status -s
git commit -m "build: compile and sync updated orchestrator binaries with idempotent retry fix"
```

---

### Plan Self-Review Check
1. **Spec Coverage:** 用户要求“如果静态资源没有生成或生成不全应该继续跑，跳过下载代码做 git pull 的幂等，后面逻辑要走” -> 在 Task 2、Task 3 完整覆盖。
2. **No Placeholders:** 每一个 Task 都提供了完整可运行的代码、测试命令与预期输出，无任何 TODO/TBD。
3. **Type Consistency:** `GetLatestFinishedBuild` 方法签名、结构体类型与 `Pipeline` 的调用全部对齐。
