# 基于 GitOps 的自定义知识接入与 MCP 访问实施计划

> **智能体执行规范：** 必须使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 技能逐项执行此计划。步骤使用复选框（`- [ ]`）标记进度。

**目标：** 支持代码仓库提交自定义 Wiki/知识文档（存放在 `doc/knowledge`、`docs` 或指定配置目录中），在服务端拉取代码后自动触发 OpenWiki 构建流水线，将这些自定义文档一并处理并融合至 3D 知识图谱、静态阅读器以及远程 MCP 服务中，且**完全不修改 `src/` 核心代码**。

**架构思路：**
1. **流水线接入与知识规范化层 (`deploy/unit1-orchestrator/internal/scheduler/knowledge.go`)**：增强知识目录自动发现机制，支持多候选目录（`doc/knowledge`、`docs`、`wiki`）及 `.openwikirc` 配置；自动补齐 Markdown 的 Frontmatter（如 `type: Knowledge`）和图片等静态资产；在 OpenWiki 导出图谱与打包前，通过可靠的物理同步机制汇入 `openwiki/knowledge/`。
2. **知识图谱联合提取与静态化呈现**：充分利用 OpenWiki 原生图谱抽取引擎（`dist/visualize/graph.js`）递归遍历 `openwiki/` 目录的机制，直接将规范化后的 Markdown 作为一等公民参与图谱节点与连线计算，自动呈现在 3D 节点网络与静态 SPA 阅读器侧边栏。
3. **MCP 访问增强层 (`deploy/unit1-orchestrator/internal/mcp/`)**：在 Go 编写的 MCP 服务中新增 `list_repository_docs` 与 `read_repository_doc` 工具，使 AI 编程工具（Cursor、Claude Desktop、OpenCode）不仅能在 `ask_repository` 问答时自动命中这些业务总结，还能按需直接查阅特定专题文档。

**技术栈：** Go 1.22+、Node.js 22 运行时、Model Context Protocol (MCP 2025 / SSE)、SQLite/MySQL。

---

## 涉及文件与职责划分

| 文件路径 | 状态 | 职责说明 |
| :--- | :--- | :--- |
| `deploy/unit1-orchestrator/internal/scheduler/knowledge.go` | **新建** | 独立封装自定义知识目录发现、`.openwikirc` 配置解析、Markdown 元信息规范化（补全 title 和 type）及图片资产镜像同步逻辑。 |
| `deploy/unit1-orchestrator/internal/scheduler/knowledge_test.go` | **新建** | 单元测试：覆盖候选目录优先级、配置覆盖、文件复制幂等性、静态图片同步及 Frontmatter 自动补齐。 |
| `deploy/unit1-orchestrator/internal/scheduler/builder.go` | **修改** | 将原有的软链接接入升级为调用 `KnowledgeIngester`；在静态化打包阶段，确保用户文档及关联图片完整发布到 `stagingDir`。 |
| `deploy/unit1-orchestrator/internal/scheduler/builder_test.go` | **修改** | 验证构建流水线全流程回归测试，确保新知识接入不影响现有构建生命周期。 |
| `deploy/unit1-orchestrator/internal/mcp/types.go` | **修改** | 注册 `list_repository_docs` 和 `read_repository_doc` 的 JSON-Schema 入参与出参定义。 |
| `deploy/unit1-orchestrator/internal/mcp/server.go` | **修改** | 实现新增 MCP 工具的调用处理逻辑，内置沙箱安全检查（严防跨目录路径穿越漏洞）。 |
| `deploy/unit1-orchestrator/internal/mcp/server_test.go` | **修改** | 编写针对两个新工具的端到端 MCP 协议调用测试用例。 |

---

## 任务拆解与执行步骤

### 任务 1：实现独立的自定义知识发现与规范化模块

**涉及文件：**
- 新建：`deploy/unit1-orchestrator/internal/scheduler/knowledge.go`
- 测试：`deploy/unit1-orchestrator/internal/scheduler/knowledge_test.go`

- [x] **步骤 1：编写失败的单元测试**

在 `knowledge_test.go` 中编写测试用例，覆盖：
1. `FindKnowledgeSourceDir`：按 `doc/knowledge` -> `docs` -> `wiki` 顺序探测候选目录；支持 `.openwikirc` 配置覆盖探测结果。
2. `IngestKnowledgeDocs`：验证将 Markdown 物理拷贝至目标目录，对缺少 Frontmatter 的文件自动提取首个 `# 标题` 生成 `type: Knowledge`；同时将 `.png`、`.svg` 等图片同步至 `_assets` 子目录。
3. 安全路径校验：严禁包含 `../` 越界路径。

- [x] **步骤 2：运行测试并确认失败**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./internal/scheduler -run TestFindKnowledgeSourceDir -v
```
预期结果：编译失败（函数尚未定义）。

- [x] **步骤 3：编写 `knowledge.go` 最小实现**

实现以下核心能力：
- `KnowledgeConfig`：解析仓库根目录的 `.openwikirc`（支持 JSON 或简易 YAML）。
- `FindKnowledgeSourceDir(repoPath string) string`：顺序探测候选目录。
- `IngestKnowledgeDocs(repoPath, dstKnowledgeDir string) (int, error)`：完成文件复制、Frontmatter 规范化补全及图片静态资源同步。

- [x] **步骤 4：运行测试并确认全部通过**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./internal/scheduler -run "TestFindKnowledge|TestNormalizeAndCopy" -v
```
预期结果：PASS。

- [x] **步骤 5：提交代码**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/knowledge*
git commit -m "feat(orchestrator): 实现自定义知识发现与文档规范化模块"
```

---

### 任务 2：将知识同步接入 Builder 构建流水线

**涉及文件：**
- 修改：`deploy/unit1-orchestrator/internal/scheduler/builder.go`
- 测试：`deploy/unit1-orchestrator/internal/scheduler/builder_test.go`

- [x] **步骤 1：编写构建流水线的失败测试**

在 `builder_test.go` 中添加：
- `TestIngestKnowledge_MultiDirectorySupport`：验证当仓库使用 `docs/` 或 `doc/knowledge/` 时，能成功将文档同步进 `openwiki/knowledge`。
- `TestIngestKnowledge_CopiesAssetsAndNormalizes`：验证生成的是真实文件（非外部失效软链），且图片资源一并同步。

- [x] **步骤 2：运行测试并确认失败**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./internal/scheduler -run TestIngestKnowledge_MultiDirectorySupport -v
```
预期结果：FAIL。

- [x] **步骤 3：完善 `builder.go` 的流水线调用**

在 `builder.go` 中：
1. 重构 `ingestKnowledge()` 函数，内部调用 `IngestKnowledgeDocs`。
2. 在 `copyVisualizerAssets` 步骤中，若存在 `openwiki/knowledge/_assets`，一并复制到 `finalStaticDir/knowledge/_assets`，确保静态网页打开时图片能正常加载。
3. 增加清晰的流水线日志输出（例如 `[build][%s] 成功导入 %d 篇自定义知识文档，来源目录: %s`）。

- [x] **步骤 4：运行调度器全量测试**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./internal/scheduler -v
```
预期结果：全部 PASS。

- [x] **步骤 5：提交代码**

```bash
git add deploy/unit1-orchestrator/internal/scheduler/builder*
git commit -m "feat(builder): 构建流水线集成多目录知识同步与静态资源导出"
```

---

### 任务 3：在 MCP 服务中暴露知识检索与文档直读工具

**涉及文件：**
- 修改：`deploy/unit1-orchestrator/internal/mcp/types.go`
- 修改：`deploy/unit1-orchestrator/internal/mcp/server.go`
- 测试：`deploy/unit1-orchestrator/internal/mcp/server_test.go`

- [x] **步骤 1：编写 MCP 新工具的失败测试**

在 `server_test.go` 中编写用例：
- `TestMCP_ListRepositoryDocs`：调用 `list_repository_docs`，验证能同时列出自动生成的代码 Wiki 与人工定制文档（包含标题、类型、相对路径）。
- `TestMCP_ReadRepositoryDoc`：调用 `read_repository_doc`，读取指定 Markdown 文件；测试路径穿越（如 `../../etc/passwd`）能够被拦截并返回安全错误。

- [x] **步骤 2：运行测试并确认失败**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./internal/mcp -run "TestMCP_ListRepositoryDocs|TestMCP_ReadRepositoryDoc" -v
```
预期结果：FAIL（工具尚未在 GetTools 中注册）。

- [x] **步骤 3：实现 MCP 工具处理器**

1. 在 `types.go` 中增加两个工具的 JSON-Schema 声明与输入参数校验。
2. 在 `server.go` 中实现：
   - `handleListRepositoryDocs`：定位目标仓库，扫描 `openwiki/` 目录树，返回结构化文档列表。
   - `handleReadRepositoryDoc`：校验入参路径必须严格位于目标仓库的 `openwiki/` 目录内，读取文件内容并以标准 MCP 格式返回。

- [x] **步骤 4：运行 MCP 模块全量测试**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./internal/mcp -v
```
预期结果：全部 PASS。

- [x] **步骤 5：提交代码**

```bash
git add deploy/unit1-orchestrator/internal/mcp/
git commit -m "feat(mcp): 新增 list_repository_docs 与 read_repository_doc 工具"
```

---

### 任务 4：端到端验证与边界纯净度检查

**涉及文件：**
- 检查验证产物：`deploy/unit1-orchestrator/bin_orchestrator`
- 检查目录边界：`src/` 必须保持 0 变动。

- [x] **步骤 1：运行 Orchestrator 全量单元测试**

运行：
```bash
cd deploy/unit1-orchestrator && go test ./...
```
预期结果：全部通过。

- [x] **步骤 2：编译二进制并检查 `src/` 干净度**

运行：
```bash
cd deploy/unit1-orchestrator && go build -o bin_orchestrator main.go
git status --porcelain src/
```
预期结果：
1. `bin_orchestrator` 成功编译生成。
2. `git status --porcelain src/` 输出为空，确保 `src/` 核心代码保持 100% 干净。

- [x] **步骤 3：提交检查记录**

```bash
git commit --allow-empty -m "chore: 验证自定义知识流水线与 src 纯净边界"
```

---

## 阶段检查点与评审说明

此时计划已完整制定，存储在 `docs/superpowers/plans/2026-11-16-custom-knowledge-gitops-pipeline.md`。请用户审核确认后，即可启动第一步的代码落地。
