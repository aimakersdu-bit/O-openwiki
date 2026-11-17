# OpenWiki 构建与实时问答全链路 OPENWIKI_HOME 隔离与零锁冲突实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 彻底根除多 Slot 并发构建与多仓库实时 Chat / MCP 问答争抢全局 `.openwiki/openwiki.sqlite` 导致的 `database is locked` 报错及单文件 16GB+ 磁盘膨胀问题，实现构建任务级（`.tmp`）与实时问答仓库级（`.qa_homes`）全链路物理隔离。

**Architecture:**
1. **三层目录物理隔离（同级于 `.openwiki`）**：
   - **全局模板层（`<root>/.openwiki/`）**：仅作为全局只读配置源（提供 `.env` 大模型密钥/网关配置与基础 `skills/`）。
   - **构建任务临时隔离层（`<root>/.tmp/build_<buildID>_<repo>_<nano>/`）**：仅在非纯知识同步模式（`mode != BuildModeKnowledgeOnly`）下为每个构建任务动态创建独立 `OPENWIKI_HOME` 并同步 `.env`；构建**完全成功**（`result.Success && result.Error == ""`）后自动清理，构建失败（含 `init` 失败或 `update` 报错降级）则完整保留现场并将路径写入最终落库的 `result.Log` 供排障。
   - **实时问答按仓隔离层（`<root>/.qa_homes/repos/<repoID>` 与 `<root>/.qa_homes/projects/<projID>`）**：`qa/manager.go` 拉起每个仓库或虚拟项目的 `qa-daemon.js` 时，分配各自独立的持久化 `OPENWIKI_HOME` 并同步最新 `.env`，既保留单仓多轮追问上下文记忆，又避免污染 Git 源码工作区（`<repo.LocalPath>`），实现跨仓库并发 Chat / MCP `ask_repository` / `ask_project` 零锁冲突。
2. **SQLite 双重兜底加固**：在 `SqliteSaver.setup()` 中注入 `PRAGMA busy_timeout = 30000;` 防止瞬时读写冲突直接抛错；在 `qa-daemon.js` 闲置 2 小时自毁退出时自动检测并清理超过 100MB 的历史快照文件，根治单库无限膨胀。

**Tech Stack:** Go 1.22+, Node.js 22 ESM (`qa-daemon.js`), TypeScript / `bun:sqlite` / `better-sqlite3` (`SqliteSaver`)

---

### File Structure Map

| 文件路径 | 职责范围 |
| :--- | :--- |
| `deploy/unit1-orchestrator/internal/scheduler/builder.go` | 构建隔离：`.tmp` 同级目录解析、按需创建 `taskHomeDir`、`Success && Error==""` 清理判定、`defer` 日志落盘 |
| `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` | 单元测试：验证 `.tmp` 路径解析、成功自动删除、`init`/`update` 失败保留现场且 `result.Log` 含排查路径、`KnowledgeOnly` 模式不创建目录 |
| `deploy/unit1-orchestrator/internal/qa/manager.go` | 问答隔离：新增 `resolveQAHomesRootDir` 与 `prepareWorkerHomeDir`，为单仓与项目 `qa-daemon` 注入专属 `OPENWIKI_HOME`，修复 `getOrStartProjectWorker` 漏赋 `cmd.Env` 的 Bug |
| `deploy/unit1-orchestrator/internal/qa/manager_test.go` | 单元测试：验证单仓/多段 RepoID/聚合项目的 `.qa_homes` 隔离路径、`.env` 自动同步与进程环境变量注入 |
| `src/agent/sqlite-saver.ts` & `dist/agent/sqlite-saver.js` | SQLite 锁超时兜底：在 `setup()` 中配置 `PRAGMA busy_timeout = 30000;` |
| `deploy/unit1-orchestrator/scripts/qa-daemon.js` | 容量自愈：Worker 闲置超时退出前检查专属 `openwiki.sqlite` 大小，超 100MB 自动清理重置 |

---

### Task 1: 修复并完善构建流水线 `.tmp` 任务隔离与生命周期管理

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/scheduler/builder.go:335-445`
- Test: `deploy/unit1-orchestrator/internal/scheduler/builder_test.go`

- [x] **Step 1: 在 `builder_test.go` 中编写生命周期与边界回归测试**

在 `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` 末尾追加以下测试，覆盖成功删除、`init` 失败保留、`update` 失败降级保留（且 `result.Log` 包含路径）、以及 `BuildModeKnowledgeOnly` 不创建目录 4 个核心场景：

```go
func TestBuilderTaskHomeLifecycle_SuccessDeletes_FailurePreserves(t *testing.T) {
	rootDir := t.TempDir()
	baseOpenwiki := filepath.Join(rootDir, ".openwiki")
	if err := os.MkdirAll(baseOpenwiki, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseOpenwiki, ".env"), []byte("OPENAI_API_KEY=test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENWIKI_HOME", baseOpenwiki)

	tmpSiblingDir := filepath.Join(rootDir, ".tmp")

	// 准备伪造的成功脚本与失败脚本
	okScript := filepath.Join(rootDir, "mock-openwiki-ok.sh")
	if err := os.WriteFile(okScript, []byte("#!/bin/sh\nmkdir -p openwiki\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	failScript := filepath.Join(rootDir, "mock-openwiki-fail.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/sh\necho 'database is locked' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// 场景 1: BuildModeKnowledgeOnly 不应创建任何 .tmp/build_* 子目录
	repoKnowledge := filepath.Join(rootDir, "repo-knowledge")
	_ = os.MkdirAll(repoKnowledge, 0755)
	bKnowledge := NewBuilder(okScript, "", "", filepath.Join(rootDir, "static"))
	resKnowledge := bKnowledge.BuildRepoWithMode(101, "org/repo-k", repoKnowledge, "", BuildModeKnowledgeOnly)
	if !resKnowledge.Success {
		t.Fatalf("expected knowledge-only build to succeed, got error: %s", resKnowledge.Error)
	}
	if entries, err := os.ReadDir(tmpSiblingDir); err == nil && len(entries) > 0 {
		t.Fatalf("expected no .tmp/build_* directories for KnowledgeOnly mode, found %d", len(entries))
	}

	// 场景 2: Full 模式成功构建后，应自动删除 .tmp/build_* 目录
	repoSuccess := filepath.Join(rootDir, "repo-success")
	_ = os.MkdirAll(repoSuccess, 0755)
	bSuccess := NewBuilder(okScript, "", "", filepath.Join(rootDir, "static"))
	resSuccess := bSuccess.BuildRepoWithMode(102, "org/repo-ok", repoSuccess, "", BuildModeFull)
	if !resSuccess.Success || resSuccess.Error != "" {
		t.Fatalf("expected full build to succeed cleanly, got success=%v err=%q", resSuccess.Success, resSuccess.Error)
	}
	if entries, err := os.ReadDir(tmpSiblingDir); err != nil || len(entries) != 0 {
		t.Fatalf("expected .tmp directory to be empty after successful build, got len=%d err=%v", len(entries), err)
	}

	// 场景 3: Step 0 (init) 失败时，必须保留 .tmp/build_* 目录，且 result.Log 中包含该排查路径
	repoInitFail := filepath.Join(rootDir, "repo-init-fail")
	_ = os.MkdirAll(repoInitFail, 0755)
	bInitFail := NewBuilder(failScript, "", "", filepath.Join(rootDir, "static"))
	resInitFail := bInitFail.BuildRepoWithMode(103, "org/repo-init-fail", repoInitFail, "", BuildModeFull)
	if resInitFail.Success {
		t.Fatalf("expected init failure to return Success=false")
	}
	entriesInitFail, err := os.ReadDir(tmpSiblingDir)
	if err != nil || len(entriesInitFail) != 1 {
		t.Fatalf("expected 1 preserved .tmp/build_* dir after init failure, got %d (err=%v)", len(entriesInitFail), err)
	}
	preservedInitPath := filepath.Join(tmpSiblingDir, entriesInitFail[0].Name())
	if !strings.Contains(resInitFail.Log, preservedInitPath) {
		t.Fatalf("expected result.Log to contain preserved path %q, got log:\n%s", preservedInitPath, resInitFail.Log)
	}
	if _, err := os.Stat(filepath.Join(preservedInitPath, ".env")); err != nil {
		t.Fatalf("expected .env to be copied into preserved task home: %v", err)
	}
	_ = os.RemoveAll(preservedInitPath)

	// 场景 4: Step 1 (update) 失败但触发离线发布降级时（result.Error != ""），同样必须保留 .tmp/build_* 目录
	repoUpdateFail := filepath.Join(rootDir, "repo-update-fail")
	_ = os.MkdirAll(filepath.Join(repoUpdateFail, "openwiki"), 0755) // 已有 openwiki 目录，跳过 init 直接进 update
	bUpdateFail := NewBuilder(failScript, "", "", filepath.Join(rootDir, "static"))
	resUpdateFail := bUpdateFail.BuildRepoWithMode(104, "org/repo-update-fail", repoUpdateFail, "", BuildModeFull)
	if resUpdateFail.Error == "" {
		t.Fatalf("expected update failure to set result.Error")
	}
	entriesUpdateFail, err := os.ReadDir(tmpSiblingDir)
	if err != nil || len(entriesUpdateFail) != 1 {
		t.Fatalf("expected 1 preserved .tmp/build_* dir after update failure, got %d (err=%v)", len(entriesUpdateFail), err)
	}
	preservedUpdatePath := filepath.Join(tmpSiblingDir, entriesUpdateFail[0].Name())
	if !strings.Contains(resUpdateFail.Log, preservedUpdatePath) {
		t.Fatalf("expected result.Log to contain preserved path %q on update failure, got log:\n%s", preservedUpdatePath, resUpdateFail.Log)
	}
}
```

- [x] **Step 2: 运行测试验证当前代码会因 3 个边界问题而失败**

Run:
```bash
cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run "TestBuilderTaskHomeLifecycle_SuccessDeletes_FailurePreserves"
```
Expected: FAIL（`KnowledgeOnly` 创建了目录、`init` 失败的 `result.Log` 不包含保留路径、`update` 失败误删了目录）。

- [x] **Step 3: 修改 `builder.go` 修复全部 3 个边界缺陷**

在 `deploy/unit1-orchestrator/internal/scheduler/builder.go` 的 `BuildRepoWithMode` 中，将 `taskHomeDir` 创建、`defer` 回收与返回日志逻辑更新为：

```go
	// Setup isolated temporary OPENWIKI_HOME under sibling .tmp directory (only when CLI execution is needed)
	var taskHomeDir string
	if mode != BuildModeKnowledgeOnly {
		tmpRootDir, baseOpenwiki := resolveSiblingTmpDir()
		_ = os.MkdirAll(tmpRootDir, 0755)

		safeRepoName := strings.ReplaceAll(strings.ReplaceAll(repoID, "/", "_"), "\\", "_")
		taskHomeDir = filepath.Join(tmpRootDir, fmt.Sprintf("build_%d_%s_%d", buildID, safeRepoName, time.Now().UnixNano()))
		if err := os.MkdirAll(taskHomeDir, 0700); err != nil {
			b.logStep(repoID, fmt.Sprintf("WARNING: failed to create task home %s: %v, falling back to base home", taskHomeDir, err), &logBuf, onProgress...)
			taskHomeDir = ""
		} else if baseOpenwiki != "" {
			// Inherit base .env if present (e.g. DeepSeek tokens or private configs)
			srcEnv := filepath.Join(baseOpenwiki, ".env")
			if envData, err := os.ReadFile(srcEnv); err == nil {
				_ = os.WriteFile(filepath.Join(taskHomeDir, ".env"), envData, 0600)
			}
		}
	}

	defer func() {
		// Clean up staging directory if it still exists (e.g. on build failure)
		if _, err := os.Stat(stagingDir); err == nil {
			os.RemoveAll(stagingDir)
		}

		// Clean up task temporary home ONLY if build succeeded without any CLI error.
		// If init or update failed (even if offline static export continued), preserve for troubleshooting.
		if taskHomeDir != "" {
			if result.Success && result.Error == "" {
				if _, err := os.Stat(taskHomeDir); err == nil {
					_ = os.RemoveAll(taskHomeDir)
				}
			} else {
				b.logStep(repoID, fmt.Sprintf("Build failed; preserving temporary home directory for troubleshooting: %s", taskHomeDir), &logBuf, onProgress...)
			}
		}
		// Always sync final logBuf snapshot so deferred troubleshooting messages are persisted in DB
		result.Log = logBuf.String()
	}()
```

- [x] **Step 4: 重新运行单元测试确认全部通过**

Run:
```bash
cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run "TestResolveSiblingTmpDir|TestBuilderTaskHomeIsolation|TestBuilderTaskHomeLifecycle_SuccessDeletes_FailurePreserves|TestBuilderEnvInjectionForOpenAICompatible"
```
Expected: PASS

---

### Task 2: 实现实时 Chat 与 MCP 问答（`qa/manager.go`）按仓库/项目 `.qa_homes` 隔离

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/qa/manager.go:165-259,487-576`
- Test: `deploy/unit1-orchestrator/internal/qa/manager_test.go`

- [x] **Step 1: 在 `manager_test.go` 中编写 `.qa_homes` 隔离与环境变量注入测试**

在 `deploy/unit1-orchestrator/internal/qa/manager_test.go` 末尾新增测试 `TestPrepareWorkerHomeDirAndEnvIsolation`：

```go
func TestPrepareWorkerHomeDirAndEnvIsolation(t *testing.T) {
	rootDir := t.TempDir()
	baseOpenwiki := filepath.Join(rootDir, ".openwiki")
	if err := os.MkdirAll(baseOpenwiki, 0700); err != nil {
		t.Fatal(err)
	}
	expectedEnvContent := "OPENAI_API_KEY=qa-secret-key\nOPENWIKI_MODEL_ID=glm-5\n"
	if err := os.WriteFile(filepath.Join(baseOpenwiki, ".env"), []byte(expectedEnvContent), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENWIKI_HOME", baseOpenwiki)

	// 1. 验证多段 RepoID 的专属 OPENWIKI_HOME 路径位于 .openwiki 同级的 .qa_homes/repos 下
	repoHome, err := prepareWorkerHomeDir("repos", "bxbank/CAC/source")
	if err != nil {
		t.Fatalf("prepareWorkerHomeDir for repo failed: %v", err)
	}
	expectedRepoHome := filepath.Join(rootDir, ".qa_homes", "repos", "bxbank", "CAC", "source")
	if repoHome != expectedRepoHome {
		t.Fatalf("expected repo worker home %q, got %q", expectedRepoHome, repoHome)
	}

	// 2. 验证全局 .env 已自动同步至该仓库专属目录
	syncedEnv, err := os.ReadFile(filepath.Join(repoHome, ".env"))
	if err != nil {
		t.Fatalf("expected .env synced to repo worker home: %v", err)
	}
	if string(syncedEnv) != expectedEnvContent {
		t.Fatalf("unexpected synced .env content: %q", string(syncedEnv))
	}

	// 3. 验证聚合项目的专属 OPENWIKI_HOME 位于 .qa_homes/projects 下，与单仓互不干扰
	projHome, err := prepareWorkerHomeDir("projects", "proj-deo")
	if err != nil {
		t.Fatalf("prepareWorkerHomeDir for project failed: %v", err)
	}
	expectedProjHome := filepath.Join(rootDir, ".qa_homes", "projects", "proj-deo")
	if projHome != expectedProjHome {
		t.Fatalf("expected project worker home %q, got %q", expectedProjHome, projHome)
	}

	// 4. 验证 buildWorkerEnv 正确覆盖注入 OPENWIKI_HOME 与 OPENWIKI_DIST_DIR
	env := buildWorkerEnv("/opt/openwiki/dist", repoHome)
	envMap := make(map[string]string)
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if envMap["OPENWIKI_HOME"] != repoHome {
		t.Fatalf("expected OPENWIKI_HOME=%q, got %q", repoHome, envMap["OPENWIKI_HOME"])
	}
	if envMap["OPENWIKI_DIST_DIR"] != "/opt/openwiki/dist" {
		t.Fatalf("expected OPENWIKI_DIST_DIR=/opt/openwiki/dist, got %q", envMap["OPENWIKI_DIST_DIR"])
	}
}
```

- [x] **Step 2: 运行测试确认失败**

Run:
```bash
cd deploy/unit1-orchestrator && go test -v ./internal/qa -run "TestPrepareWorkerHomeDirAndEnvIsolation"
```
Expected: FAIL with `undefined: prepareWorkerHomeDir` and `undefined: buildWorkerEnv`.

- [x] **Step 3: 在 `deploy/unit1-orchestrator/internal/qa/manager.go` 中实现 `.qa_homes` 隔离与环境变量构造**

在 `manager.go` 中新增辅助函数 `resolveQAHomesRootDir`、`prepareWorkerHomeDir`、`upsertWorkerEnv`、`buildWorkerEnv`，并在 `getOrStartWorker` 与 `getOrStartProjectWorker` 中调用（同时修复 `getOrStartProjectWorker` 原先漏写 `cmd.Env = env` 的问题）：

```go
// resolveQAHomesRootDir locates the .qa_homes directory sibling to the current .openwiki directory.
func resolveQAHomesRootDir() (string, string) {
	baseOpenwiki := os.Getenv("OPENWIKI_HOME")
	if baseOpenwiki == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			baseOpenwiki = filepath.Join(home, ".openwiki")
		}
	}
	if baseOpenwiki == "" {
		baseOpenwiki = ".openwiki"
	}
	baseOpenwiki = filepath.Clean(baseOpenwiki)

	var parentDir string
	if filepath.Base(baseOpenwiki) == ".openwiki" {
		parentDir = filepath.Dir(baseOpenwiki)
	} else {
		parentDir = baseOpenwiki
	}
	return filepath.Join(parentDir, ".qa_homes"), baseOpenwiki
}

// prepareWorkerHomeDir creates an isolated OPENWIKI_HOME directory for a QA worker
// under <sibling>/.qa_homes/<category>/<id> and syncs the base .env file if present.
func prepareWorkerHomeDir(category, id string) (string, error) {
	qaHomesRoot, baseOpenwiki := resolveQAHomesRootDir()
	cleanID := filepath.Clean(strings.TrimPrefix(id, "/"))
	if cleanID == "." || cleanID == "" || strings.HasPrefix(cleanID, "..") {
		cleanID = "default"
	}
	workerHome := filepath.Join(qaHomesRoot, category, cleanID)
	if err := os.MkdirAll(workerHome, 0o700); err != nil {
		return "", err
	}
	if baseOpenwiki != "" {
		srcEnv := filepath.Join(baseOpenwiki, ".env")
		if envData, err := os.ReadFile(srcEnv); err == nil {
			_ = os.WriteFile(filepath.Join(workerHome, ".env"), envData, 0o600)
		}
	}
	return workerHome, nil
}

func upsertWorkerEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func buildWorkerEnv(distDir, workerHome string) []string {
	env := os.Environ()
	hasPath := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=/opt/openwiki/node22/bin:" + strings.TrimPrefix(e, "PATH=") + ":/usr/local/bin:/opt/homebrew/bin"
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/opt/openwiki/node22/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin")
	}
	if distDir != "" {
		env = upsertWorkerEnv(env, "OPENWIKI_DIST_DIR", distDir)
	}
	if workerHome != "" {
		env = upsertWorkerEnv(env, "OPENWIKI_HOME", workerHome)
	}
	return env
}
```

并在 `getOrStartWorker` 中替换原有 `env` 构造逻辑：
```go
	workerHome, err := prepareWorkerHomeDir("repos", repo.ID)
	if err != nil {
		log.Printf("[QA-Manager] Warning: failed to prepare isolated QA home for repo %s: %v", repo.ID, err)
	}
	cmd.Env = buildWorkerEnv(m.distDir, workerHome)
```

在 `getOrStartProjectWorker` 中替换原有 `env` 构造逻辑：
```go
	workerHome, err := prepareWorkerHomeDir("projects", proj.ID)
	if err != nil {
		log.Printf("[QA-Manager] Warning: failed to prepare isolated QA home for project %s: %v", proj.ID, err)
	}
	cmd.Env = buildWorkerEnv(m.distDir, workerHome)
```

- [x] **Step 4: 运行单元测试确认通过**

Run:
```bash
cd deploy/unit1-orchestrator && go test -v ./internal/qa -run "TestPrepareWorkerHomeDirAndEnvIsolation"
```
Expected: PASS

---

### Task 3: SQLite `busy_timeout` 兜底与 `qa-daemon.js` 闲置防膨胀自愈

**Files:**
- Modify: `src/agent/sqlite-saver.ts:78-86`
- Modify: `dist/agent/sqlite-saver.js:64-73`
- Modify: `deploy/unit1-orchestrator/scripts/qa-daemon.js:211-225`

- [x] **Step 1: 在 `src/agent/sqlite-saver.ts` 和 `dist/agent/sqlite-saver.js` 中设置 30 秒 `busy_timeout`**

在 `src/agent/sqlite-saver.ts:78-86` 的 `setup()` 方法中：
```typescript
  setup(): void {
    if (this.isSetup) return;
    if (typeof this.db.pragma === "function") {
      this.db.pragma("journal_mode=WAL");
      this.db.pragma("busy_timeout=30000");
    } else {
      this.db.exec("PRAGMA journal_mode=WAL;");
      this.db.exec("PRAGMA busy_timeout=30000;");
    }
```
同步修改 `dist/agent/sqlite-saver.js:64-73` 中的 `setup()`：
```javascript
    setup() {
        if (this.isSetup)
            return;
        if (typeof this.db.pragma === "function") {
            this.db.pragma("journal_mode=WAL");
            this.db.pragma("busy_timeout=30000");
        }
        else {
            this.db.exec("PRAGMA journal_mode=WAL;");
            this.db.exec("PRAGMA busy_timeout=30000;");
        }
```

- [x] **Step 2: 在 `qa-daemon.js` 空闲退出时增加 SQLite 超限（>100MB）自动瘦身清理**

在 `deploy/unit1-orchestrator/scripts/qa-daemon.js` 的 `cleanupAndExit` 函数中增加闲置超限清理逻辑：

```javascript
const MAX_IDLE_SQLITE_BYTES = 100 * 1024 * 1024; // 100 MB

function pruneOversizedSqliteOnIdleExit() {
  try {
    const homeDir = process.env.OPENWIKI_HOME;
    if (!homeDir) return;
    const sqliteFile = path.join(homeDir, 'openwiki.sqlite');
    if (fs.existsSync(sqliteFile)) {
      const stat = fs.statSync(sqliteFile);
      if (stat.size > MAX_IDLE_SQLITE_BYTES) {
        console.log(`[QA-Daemon] openwiki.sqlite size (${stat.size} bytes) exceeds 100MB on idle exit; pruning...`);
        for (const suffix of ['', '-wal', '-shm']) {
          const target = sqliteFile + suffix;
          if (fs.existsSync(target)) {
            fs.unlinkSync(target);
          }
        }
      }
    }
  } catch (err) {
    console.error('[QA-Daemon] Warning: failed to prune oversized sqlite:', err);
  }
}

function cleanupAndExit(code = 0) {
  try {
    server.close(() => {
      try {
        if (socketPath && fs.existsSync(socketPath)) {
          fs.unlinkSync(socketPath);
        }
      } catch { }
      pruneOversizedSqliteOnIdleExit();
      process.exit(code);
    });
  } catch {
    pruneOversizedSqliteOnIdleExit();
    process.exit(code);
  }
}
```

---

### Task 4: 编译多平台生产二进制与全量回归验证

**Files:**
- Modify: `deploy/bin_orchestrator`
- Modify: `deploy/bin/x86/scripts/qa-daemon.js` & `deploy/bin/arm/scripts/qa-daemon.js`

- [x] **Step 1: 运行 Go 单元测试套件验证**

Run:
```bash
cd deploy/unit1-orchestrator && go test -v ./internal/scheduler -run "TestResolveSiblingTmpDir|TestBuilderTaskHomeIsolation|TestBuilderTaskHomeLifecycle_SuccessDeletes_FailurePreserves|TestBuilderEnvInjectionForOpenAICompatible" && go test -v ./internal/qa -run "TestPrepareWorkerHomeDirAndEnvIsolation"
```
Expected: 全部 `PASS`。

- [x] **Step 2: 编译更新本地及发布目录二进制，并同步 `qa-daemon.js`**

Run:
```bash
cd deploy/unit1-orchestrator && go build -o ../bin_orchestrator .
cp scripts/qa-daemon.js ../bin/x86/scripts/qa-daemon.js
cp scripts/qa-daemon.js ../bin/arm/scripts/qa-daemon.js
```
Expected: 编译退回码为 0，无任何警告或报错。

- [x] **Step 3: 生产服务器上线清理指引（一次性操作）**

在服务器 `/data/disk0/openwiki/.openwiki` 目录下清理历史遗留的 16.4 GB `openwiki.sqlite`：
```bash
cd /data/disk0/openwiki/.openwiki
rm -f openwiki.sqlite openwiki.sqlite-wal openwiki.sqlite-shm
```
随后重启 `orchestrator` 服务，后续：
- 构建任务自动在 `/data/disk0/openwiki/.tmp/build_*` 中隔离运行，成功即删，失败保留；
- 实时 Chat 与 MCP 问答自动在 `/data/disk0/openwiki/.qa_homes/repos/<repoID>` 与 `projects/<projID>` 中按仓隔离运行，彻底告别 `database is locked`。

