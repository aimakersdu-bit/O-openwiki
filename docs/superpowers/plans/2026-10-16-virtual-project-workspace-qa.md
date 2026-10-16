# OpenWiki 虚拟项目工作区与多微服务跨仓协同问答实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 支持将多个 Git 微服务仓库逻辑聚合为一个“项目（Project）”，通过虚拟工作区软链接映射、轻量跨仓契约缝合器（Linker）与顶层架构生成，实现无需重写 AI 引擎即可支持微服务跨仓调用链检索、拓扑分析与项目级高精度 AI 问答。

**Architecture:** 
1. **存储与模型层**：在 SQLite (`orchestrator.db`) 引入 `projects` 和 `project_repos` 表，实现项目与单仓的 N:M 映射及别名管理。
2. **虚拟工作区与构建流水线**：通过 `os.Symlink` 在磁盘构建虚拟 Monorepo 目录，单仓构建结果全量复用；增量触发轻量级 `linker.js` 扫描 HTTP 路由、RPC 接口与 MQ Topics 建立跨仓有向边。
3. **项目级全局文档与状态面**：LLM 聚合各子仓 Wiki 首页与 Linker 拓扑输出 `_project_hub/openwiki/` 顶层系统全景图与时序图；扩展 `qa/manager.go` 支持按项目工作区挂载常驻 `qa-daemon.js`，复用 UDS 和 FIFO 排队锁。
4. **管理与门户展现层**：`admin.html` 增加微服务项目编排面板（穿梭多选配置），`dashboard.html` 增加“项目级 / 单仓级”视图无缝切换与跨仓时序图渲染。

**Tech Stack:** Go (Unit 1 Orchestrator), Node.js (Linker & qa-daemon.js), SQLite3, LangGraph/LangChain, JavaScript (ES6+), HTML5/CSS3, Mermaid.js.

---

### Task 1: 数据库与实体模型扩展（项目与微服务多仓关系）

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/db/sqlite.go:140-195`
- Test: `deploy/unit1-orchestrator/internal/db/sqlite_test.go`

- [x] **Step 1: 编写数据表迁移与 CRUD 的单元测试用例**
  - 在 `sqlite_test.go` 中编写 `TestProjectCRUD` 和 `TestProjectReposMapping`；
  - 测试创建项目、添加微服务关联、更新子目录别名与查询项目包含仓库列表。

- [x] **Step 2: 运行测试验证失败**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/db -run TestProject`
  - 预期: FAIL（未定义相关表结构与结构体）。

- [x] **Step 3: 在 `sqlite.go` 中实现表迁移与 DAO 方法**
  - 定义 `Project` 和 `ProjectRepo` 结构体：
    ```go
    type Project struct {
        ID          string    `json:"id"`
        Name        string    `json:"name"`
        Description string    `json:"description"`
        LocalPath   string    `json:"local_path"`
        Status      string    `json:"status"`
        CreatedAt   time.Time `json:"created_at"`
        UpdatedAt   time.Time `json:"updated_at"`
    }
    type ProjectRepo struct {
        ID         int64  `json:"id"`
        ProjectID  string `json:"project_id"`
        RepoID     string `json:"repo_id"`
        AliasName  string `json:"alias_name"`
        Role       string `json:"role"`
    }
    ```
  - 在 `db.migrate()` 中加入 `projects`、`project_repos` 以及 `project_qa_sessions` 表；
  - 实现 `CreateProject`、`GetProject`、`ListProjects`、`UpdateProject`、`DeleteProject`；
  - 实现 `SetProjectRepos(projectID string, repos []ProjectRepo)` 与 `GetProjectRepos(projectID string) ([]RepoWithAlias, error)`。

- [x] **Step 4: 运行测试验证通过**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/db -run TestProject`
  - 预期: PASS。

- [x] **Step 5: 提交更改**
  - `git commit -m "feat(db): add projects and project_repos schema and methods"`

---

### Task 2: 虚拟工作区目录构造器与工作目录映射 (Virtual Workspace Builder)

**Files:**
- Create: `deploy/unit1-orchestrator/internal/project/workspace.go`
- Test: `deploy/unit1-orchestrator/internal/project/workspace_test.go`

- [x] **Step 1: 编写虚拟工作区软链接测试用例**
  - 测试创建临时项目目录、建立多个子仓库的符号软链接（`os.Symlink`）；
  - 测试当子仓已存在或目录已建立时的幂等性更新与孤儿链接清理。

- [x] **Step 2: 运行测试验证失败**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/project -run TestWorkspace`
  - 预期: FAIL。

- [x] **Step 3: 实现 `WorkspaceManager` 逻辑**
  - 结构定义与核心方法：
    ```go
    type WorkspaceManager struct {
        BaseProjectsDir string
    }
    func (m *WorkspaceManager) PrepareWorkspace(project *db.Project, repos []db.RepoWithAlias) (string, error)
    ```
  - 遍历关联微服务仓库，校验源目录 `repo.LocalPath` 是否存在；
  - 若已存在同名软链接则核对指向，若指向变更则重建；若有多余软链接则予以删除；
  - 在虚拟根目录下初始化 `_project_hub/openwiki` 与 `.project-meta.json` 标识文件。

- [x] **Step 4: 运行测试验证通过**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/project -run TestWorkspace`
  - 预期: PASS。

- [x] **Step 5: 提交更改**
  - `git commit -m "feat(project): implement virtual workspace symlink builder"`

---

### Task 3: 跨仓轻量契约缝合器 (Linker 脚本实现)

**Files:**
- Create: `deploy/unit1-orchestrator/scripts/linker.js`
- Test: `deploy/unit1-orchestrator/scripts/test/linker.test.js`

- [x] **Step 1: 编写跨仓连线规则测试集**
  - 构造模拟的微服务虚拟目录，包含 `service-a`（提供 `@GetMapping("/api/v1/orders")` 并发送 MQ `ORDER_CREATED`）和 `service-b`（包含 FeignClient 调用及 `@KafkaListener(topics="ORDER_CREATED")`）；
  - 验证 Linker 能够精确产出 `cross_repo_graph.json`，包含正确的节点和有向边。

- [x] **Step 2: 运行测试验证失败**
  - 运行: `node deploy/unit1-orchestrator/scripts/test/linker.test.js`
  - 预期: FAIL（`linker.js` 未实现）。

- [x] **Step 3: 编写 `linker.js` 核心扫描与匹配逻辑**
  - 遍历工作区所有一级子目录（排除 `_project_hub`、`node_modules`、`.git`）；
  - 扫描支持：
    1. Spring Boot / Gin / FastAPI 路由端点提取与 Feign / HTTP Client 调用点提取；
    2. Kafka / RabbitMQ / RocketMQ Topics 生产与消费端提取；
    3. Java `pom.xml` / Go `go.mod` / Node `package.json` 内部同名依赖匹配；
  - 算法：将 Prov/Cons 进行交叉匹配，生成：
    - `_project_hub/cross_repo_graph.json`（供图引擎消费）；
    - `_project_hub/openwiki/architecture/topology.md`（带自动生成的 Mermaid 服务拓扑图）。

- [x] **Step 4: 运行测试验证通过**
  - 运行: `node deploy/unit1-orchestrator/scripts/test/linker.test.js`
  - 预期: PASS。

- [x] **Step 5: 提交更改**
  - `git commit -m "feat(linker): add cross-repo contract and dependency linker"`

---

### Task 4: 项目级全局 Wiki 生成、编排与 Portal 鉴权反代 API

**Files:**
- Create: `deploy/unit1-orchestrator/internal/api/projects.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go:20-55`
- Create: `deploy/unit2-portal/internal/api/projects.go`
- Modify: `deploy/unit2-portal/internal/api/router.go:50-70`

- [x] **Step 1: 编写项目管理与构建触发 API 路由测试**
  - 测试 `POST /api/projects`（创建）、`GET /api/projects`（列表）、`POST /api/projects/build`（触发项目聚合构建）。

- [x] **Step 2: 运行测试验证失败**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/api -run TestProjectsAPI`
  - 预期: FAIL。

- [x] **Step 3: 实现 Orchestrator 端项目 API 与构建编排逻辑**
  - 在 `unit1-orchestrator/internal/api/projects.go` 中实现 CRUD Handler；
  - 在 `POST /api/projects/build` 中接入编排：
    1. 检查各子仓库是否已完成单仓构建，若有缺失则并发拉起子仓构建；
    2. 调用 `WorkspaceManager.PrepareWorkspace` 组装虚拟目录与软链接；
    3. 调用 Node 执行 `linker.js` 产出跨仓关系；
    4. 提取各子仓的 `openwiki/quickstart.md` 核心摘要，组合提示词调用轻量 LLM 产出 `_project_hub/openwiki/quickstart.md` 项目全局介绍；
    5. 将静态 Wiki 产物同步或软链至 `/data/openwiki/static/projects/<project_id>/`，与 Nginx `/wiki/` 路由天然对齐；
    6. 更新项目状态为 `ready`。

- [x] **Step 4: 实现 Portal (Unit 2) 端鉴权反代 Handler**
  - 在 `unit2-portal/internal/api/projects.go` 中实现 `handleProjects`：校验用户 Session 与角色权限，通过 `proxyToOrchestrator` 安全透传 `/portal/projects` 请求；
  - 在 `unit2-portal/internal/api/router.go` 注册 `s.mux.HandleFunc("/portal/projects", s.handleProjects)`。

- [x] **Step 5: 运行测试验证通过**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/api -run TestProjectsAPI`
  - 预期: PASS。

- [x] **Step 6: 提交更改**
  - `git commit -m "feat(api): add project management, portal proxy and aggregate build orchestration"`

---

### Task 5: 扩展 QA Manager 支持项目工作区常驻 Daemon

**Files:**
- Modify: `deploy/unit1-orchestrator/internal/qa/manager.go:60-150`
- Modify: `deploy/unit1-orchestrator/internal/api/chat.go:30-80`
- Modify: `deploy/unit1-orchestrator/scripts/qa-daemon.js:100-140`

- [x] **Step 1: 编写项目级问答会话路由测试**
  - 测试向 `/api/chat` 发送带 `project_id`（而非 `repo_id`）的请求；
  - 验证 QA Manager 能在虚拟项目目录下唤醒专属的 `qa-daemon.js`，并为该虚拟 Monorepo 建立 UDS 连接。

- [x] **Step 2: 运行测试验证失败**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/api -run TestProjectChat`
  - 预期: FAIL。

- [x] **Step 3: 在 `manager.go` 与 `qa-daemon.js` 中支持 Project 维度**
  - `manager.go`：新增 `GetProjectWorker(ctx, projectID)`，Socket 路径定为 `/tmp/openwiki-qa-proj-<id>.sock`，工作目录绑定为 `project.LocalPath`；
  - `chat.go`：支持请求体中可选 `project_id`；
  - `qa-daemon.js`：当检测到当前工作区存在 `_project_hub` 标识时，自动在 Prompt 中注入**“微服务项目架构师”**视角，优先读取 `_project_hub/openwiki/` 拓扑文档并在时序交互时优先输出 Mermaid 图。

- [x] **Step 4: 运行测试验证通过**
  - 运行: `go test -v ./deploy/unit1-orchestrator/internal/api -run TestProjectChat`
  - 预期: PASS。

- [x] **Step 5: 提交更改**
  - `git commit -m "feat(qa): support project-level virtual workspace daemon and context"`

---

### Task 6: Portal 前端界面与多微服务交互支持

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html:30-80`
- Modify: `deploy/unit2-portal/public/js/admin.js:100-220`
- Modify: `deploy/unit2-portal/public/index.html` (或 dashboard.html)
- Modify: `deploy/unit2-portal/public/js/dashboard.js:40-120`

- [x] **Step 1: 在 `admin.html` 中新增“微服务项目管理”标签页**
  - 增加项目列表表格、新增/编辑项目弹窗；
  - 弹窗内支持穿梭多选框（从已有仓库中勾选需要组合进该项目的微服务，并指定别名与角色）。

- [x] **Step 2: 在 `admin.js` 中实现项目配置与构建交互**
  - 实现调用 `/api/projects` 的增删改查；
  - 增加“一键构建项目知识库”按钮与状态轮询。

- [x] **Step 3: 在 `dashboard.js` 中支持切换“项目维度”与“仓库维度”**
  - 顶部下拉框增加分组：【微服务项目】与【独立单仓】；
  - 选中项目时，左侧侧边栏展示项目级导航（全局架构、拓扑全景、各子服务折叠目录）；
  - 右侧问答框请求时附带 `project_id`，提问全局链路时，自动渲染跨服务 Mermaid 时序图并高亮所属子目录文件。

- [x] **Step 4: 整体端到端手工与界面验证**
  - 创建包含 2 个模拟微服务仓库的虚拟项目；
  - 触发项目构建，验证目录软链接正确生成、Linker 产出拓扑；
  - 在前端提问跨服务链路，验证流式输出与时序图渲染无误。

- [x] **Step 5: 提交更改**
  - `git commit -m "feat(portal): add project management UI and project-level QA dashboard"`

---

### Task 7: 编译构建脚本与部署产物同步

**Files:**
- Modify: `deploy/scripts/build-x86.sh`
- Modify: `deploy/scripts/build-arm.sh`

- [x] **Step 1: 更新构建脚本同步 `linker.js` 与前端资源**
  - 确保打包脚本将 `deploy/unit1-orchestrator/scripts/linker.js` 同步分发至 `deploy/bin/*/scripts/`；
  - 重新编译 Go Orchestrator 二进制文件并同步静态资源。

- [x] **Step 2: 执行全量打包验证**
  - 运行: `./deploy/scripts/build-x86.sh` (或本地对应平台构建)
  - 验证最终产物包内包含完整的虚拟项目支持组件。

- [x] **Step 3: 提交更改**
  - `git commit -m "chore(deploy): update build scripts for project workspace support"`
