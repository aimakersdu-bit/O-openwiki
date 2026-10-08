# OpenWiki 服务测试问题修复方案 Implementation Plan (v6 约束增强版)

本文档汇总了 OpenWiki 测试中反馈的 8 个缺陷的系统级修复方案。**本方案严格遵循硬性约束：拉取独立 Git 分支进行修复，且 100% 不改动 `openwiki` 引擎源码（所有改动严格收敛于 `deploy/` 部署单元内）。**

---

## 0. 硬性约束与原则 (Mandatory Constraints)

1. **分支隔离开发**：所有修复工作必须在新的 Git 分支（如 `fix/service-issues`）上进行，禁止直接在主分支修改。
2. **源码零侵入原则**：**严格禁止修改 `openwiki` 项目 `src/` 或 `openwiki/` 目录下的任何 TypeScript/Node 源码**。所有前端适配、图谱更新、版本隔离、权限审计与交互优化，必须 100% 在 `deploy/` 独立部署单元（Go Orchestrator、Go Portal、Portal 网页组件及 Nginx 配置）内完成。

---

## 1. 8 大测试问题与核心架构缺陷根因分析与方案

### 问题 1 & 问题 6：Wiki 仪表盘内容丢失（隔天不显示 / 点击立即构建后不显示）
- **根因分析**：
  `deploy/unit2-portal/public/js/dashboard.js` 在过滤展示仓库时，硬编码了 `builds[0].status === 'success'`（即仅检查最新一条构建记录）。
  - 当隔天触发 Cron 自动构建（问题 1），或管理员在后台点击“立即构建”（问题 6）时，数据库中新增了一条状态为 `pending` 或 `running` 的构建记录。
  - 此时 `builds[0]` 变为最新一条未完成的记录，导致 `dashboard.js` 直接过滤剔除了该仓库，使得仪表盘页面上的 Wiki 按钮和问答入口瞬间消失。
- **修复方案**：
  1. 重构 `dashboard.js` 的仓库筛选与状态展示逻辑。只要仓库历史记录中存在成功构建（`builds.some(b => b.status === 'success')`），或本地已存在成功部署的 Wiki 目录，即保持在仪表盘展示。
  2. 若当前有新构建正在进行，在卡片上展示 `⚡ 增量构建中 (Build #xx)` 状态标签，不遮挡已有 Wiki 的访问与问答入口。

---

### 问题 2：问答框异常显示问题（窗口拖拽拉大拉小后无法关闭、弹性溢出）
- **根因分析**：
  1. `deploy/unit2-portal/public/css/style.css` 中 `.chat-drawer` 关闭状态硬编码了 `right: -900px`。当用户将问答抽屉向左拖拽拉大（例如宽度拉到 `1000px`）后，点击关闭时即使移除了 `.open` 类，由于 `-900px` 仍小于 `1000px` 宽度，屏幕右侧依然会残留 `100px` 宽度的抽屉画面，导致看似“无法关闭”。
  2. 抽屉在不同分辨率或历史列表展开时，未设置 `flex: 1` 和最大高度，导致底部输入框和发送按钮被挤出可视区域。
  3. 发送按键仅绑定 `Ctrl+Enter`，缺乏多行自适应与单 `Enter` 发送（`Shift+Enter` 换行）的良好体验。
- **修复方案**：
  1. **彻底重构抽屉隐藏机制**：在 `style.css` 中将 `.chat-drawer` 的关闭隐藏逻辑从 `right: -900px` 改为 **`transform: translateX(100%)`**。无论用户将抽屉拖拽调整为多大，关闭时均能 100% 完美平滑滑出屏幕外！
  2. **关闭按钮层级与布局保障**：为 `#closeChatBtn` 显式指定 `z-index: 30`、`flex-shrink: 0` 与明确的点击手势；优化 `.chat-body` 的 Flex 弹性高度与 Markdown 代码块横向滚动限制（防止拉变形）。
  3. **输入框优化**：支持单 `Enter` 发送（`Shift+Enter` 换行）与文本框高度自动拓展。

---

### 问题 3：静态资源拷贝（copy）问题与路径不匹配
- **根因分析**：
  1. `deploy/unit1-orchestrator/assets/index.html` 生成的 HTML 模版中含有 Leading Slash 路径（如 `<script src="/vendor/force-graph.min.js">` 和 `<script src="/client.js">`）。当 Nginx 将 Wiki 挂载于 `/wiki/<repo_id>/` 子路径下时，浏览器会直接向根域名请求 `http://host/vendor/...`（返回 404），导致跑批后静态页面崩溃。
  2. `builder.go` 在复制 `index.html` 与 `client.js` 时存在多源路径不统一的问题，导致覆盖后 HTML 引用的 JS 变量/函数与导出的 `client.js` 版本不匹配。
- **修复方案**：
  1. 彻底统一 `deploy/unit1-orchestrator/assets/index.html` 中的所有资源路径为**相对路径**（`vendor/force-graph.min.js`、`client.js`、`client-lib.js`），彻底消除 `/wiki/<repo_id>/` 子路径托管时的 404 隐患。
  2. 规范 `builder.go` 的静态资源提取逻辑，确保 `index.html`、`client.js`、`client-lib.js` 和 `vendor/` 库强一致拷贝。

---

### 问题 4：新增知识文档在查看 Wiki 页面不显示问题（及 AI 问答生成新 Wiki 的图谱热更新与版本控制）
- **根因分析**：
  1. OpenWiki 在问答或 Agent 交互中会在 `openwiki/` 目录中生成/更新 Markdown 知识文档，但静态 Visualizer 页面依赖 `/data/openwiki/static/<repo_id>/api/graph`（包含 `nodes`、`edges` 和 `backlinks`）渲染图谱与文档树。
  2. 原先 `api/graph` 仅在跑批/手动构建时导出一次。问答实时产生新 Wiki 后，`api/graph` 未被同步更新，导致前端图谱与文档列表中缺少新节点索引。
  3. Portal 中的 `/api/graph` 接口未显式设置 HTTP `Cache-Control` Header，导致浏览器缓存旧图谱。
- **修复方案与 OpenWiki 原生 SSE 架构对齐**：
  1. **图数据实时重导出机制**：当 AI 问答或 Agent 任务向 `openwiki/` 写入新 Markdown 文档后，Orchestrator 自动触发 `builder.exportGraph()`，重新扫描 `openwiki/` 目录并更新当前版本的 `api/graph` JSON 文件（连同 Frontmatter 标题、`type`、`tags` 及 Markdown 内链关系）。
  2. **对接 OpenWiki 原生 SSE 长链接（EventSource /events）**：Nginx 与 Portal 放行 `/wiki/<repo_id>/events` 代理至 Orchestrator。当 `api/graph` 更新后，Orchestrator 向长链接客户端广播 `event: reload`，前端 `client.js` 自动重新 fetch `/api/graph`，完成新生成 Wiki 的**实时节点关联与 UI 提示（"Wiki updated"）**。
  3. **HTTP 缓存控制**：为 Portal `/api/graph` 接口增加 `Cache-Control: no-cache, no-store, must-revalidate` 响应头。

---

### 问题 5：登录失败报错一长串，不友好
- **根因分析**：`deploy/unit2-portal/internal/api/login.go` 在认证失败时，直接将后端的原始 LDAP / AD 报错字符串（如 `Authentication failed: LDAP Result Code 49 "Invalid Credentials": LDAP Error 49 ...`）写回给前端，前端 `login.js` 原样显示。
- **修复方案**：在 `login.go` 与 `ldap.go` 中拦截技术报错，转换为友好提示（例如：“用户名或密码错误，请重新输入” 或 “AD 域服务连接超时，请联系管理员”），同时记录详细日志到服务端。

---

### 构建架构强化：构建版本隔离与原子无缝切换（Zero-Downtime Atomic Swapping）
- **根因分析**：原 Orchestrator 构建时直接在正被外部访问的 `/data/openwiki/static/<repo_id>/` 中原置覆盖写入文件。在耗时较长的跑批编译中，用户访问会出现文件缺失或 404；且若构建中途失败，已有的成功 Wiki 会被损坏。
- **修复方案**：
  1. **构建版本隔离**：每次触发构建（Cron 或手动），Orchestrator 在临时目录 `/data/openwiki/static/<repo_id>_tmp_<build_id>/` 中独立进行全套编译、`api/graph` 图数据导出与静态资源组装。当前线上版本 `/data/openwiki/static/<repo_id>/api/graph` 保持 100% 独立与可读。
  2. **原子无缝切换**：仅在全套流程**完全成功**后，通过原子重命名（Atomic Dir Swap）将新产物替换至 `/data/openwiki/static/<repo_id>/`。
  3. **零不可用窗口**：在新版本编译的整个过程中，线上用户可以顺畅浏览旧版 Wiki 并正常进行 AI 问答。即便跑批编译失败，生产目录依然安全保留上一个成功版本，绝不产生“不可访问窗口”。

---

### 问题 7：数据权限问题（管理员在问答审计中看不到普通用户的问答信息）
- **根因分析**：
  1. `deploy/unit2-portal/internal/api/sessions.go` 在 `handleQASessions` 接口中，无条件向 Orchestrator 传递 `user_id = session.UserID`（即即使是管理员，也传入了管理员自身的 user_id）。
  2. `deploy/unit1-orchestrator/internal/api/chat.go` (`handleUserQASessions`) 和 `internal/db/sqlite.go` (`ListUserQASessions` / `ListQASessions`) 严格按 `WHERE repo_id = ? AND user_id = ?` 过滤。
- **修复方案**：
  1. **审计权限解绑**：修改 `sessions.go`，当登录用户角色为 `admin` 时，向 Orchestrator 发起查询时不携带 `user_id` 约束（查询全量用户数据）；普通用户保持仅查询自身的 `user_id`。
  2. **后端全量 SQL 扩展**：修改 `sqlite.go` 中的 `ListUserQASessions` 和 `ListQASessions`，当 `user_id == ""` 时，使用 `WHERE repo_id = ?` 语句，返回该仓库下所有用户的提问与回答历史。
  3. **界面展示**：审计页面 `history.html` 明确展示提问用户账号标识，便于管理员进行全量问答审计与合规检查。

---

### 问题 8：登录页面账号 sAMAccountName / User Principal 建议去掉
- **根因分析**：`deploy/unit2-portal/public/login.html` 第 25 行标签写有 `<label for="username">账号 (sAMAccountName / User Principal)</label>`，表述过于晦涩专业。
- **修复方案**：将其简化修改为 `<label for="username">AD 域账号</label>`。

---

## 2. 拟修改文件全量列表（严格限于 `deploy/` 目录）

### Unit 1 - Orchestrator (构建、版本隔离与 SSE 广播)

#### [MODIFY] [builder.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/scheduler/builder.go)
- 实现基于临时目录 `/data/openwiki/static/<repo_id>_tmp_<build_id>/` 的隔离构建与 **Atomic Dir Swap**。
- 修正 `index.html` 中的资源引用路径为相对路径。
- 提供 `ExportGraph(repoID)` 供问答增量更新图谱。

#### [MODIFY] [router.go (Orchestrator)](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/api/router.go)
- 增加 SSE `/api/repos/:id/events` 长链接订阅路由与广播 Handlers。

#### [MODIFY] [chat.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/api/chat.go)
- 问答生成新 Markdown 后，触发 `ExportGraph` 并向 SSE 通道广播 `event: reload`。
- `handleUserQASessions` 当 `user_id` 为空时，返回全量用户的问答历史。

#### [MODIFY] [sqlite.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/db/sqlite.go)
- 支持管理员全量问答数据检索。

#### [MODIFY] [index.html (assets)](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/assets/index.html)
- 修正资源加载路径为相对路径，确保与 `client.js` 强匹配。

---

### Unit 2 - Portal & Frontend (前端、代理与样式)

#### [MODIFY] [nginx.conf](file:///Users/bigc/openwiki/deploy/nginx.conf)
- 增加 `/wiki/<repo_id>/events` 到 Orchestrator SSE 的反向代理（关闭 buffering）。

#### [MODIFY] [style.css](file:///Users/bigc/openwiki/deploy/unit2-portal/public/css/style.css)
- 将 `.chat-drawer` 的关闭重构为 `transform: translateX(100%)`，彻底解决拖拽拉大后无法关闭问题。
- 优化 `.chat-body` Flex 弹性与防溢出样式。

#### [MODIFY] [dashboard.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/js/dashboard.js)
- 保持成功历史版本的展示与问答访问，构建中显示 `⚡ 增量构建中...` 徽章。
- 优化拖拽调整宽度逻辑与 `Enter` / `Shift+Enter` 按键交互。

#### [MODIFY] [sessions.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/sessions.go)
- 管理员角色透传全量审计请求。

#### [MODIFY] [login.html](file:///Users/bigc/openwiki/deploy/unit2-portal/public/login.html)
- 替换 Label 为 `AD 域账号`。

#### [MODIFY] [login.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/js/login.js)
- 拦截并格式化 LDAP 报错，输出友好中文提示。

#### [MODIFY] [login.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/login.go)
- 拦截原始 LDAP Error，写回标准友好错误。

#### [MODIFY] [router.go (Portal)](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/router.go)
- 为 `/api/graph` 增加 HTTP 无缓存 Header。

#### [MODIFY] [history.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/js/history.js)
- 审计页面展示提问用户账号列。

---

## 3. 验证计划 (Verification Plan)

### 自动化测试 (Automated Tests)
- 在 Git 新分支上执行 `go test ./...` 校验后端逻辑。

### 手动验证 (Manual Verification)
1. **分支检查**：验证改动提交完全运行在 `fix/service-issues` 分支上，且 `git status` 确认没有任何 `src/` 或 `openwiki/` 目录下的修改。
2. **抽屉拖拽关闭验证**：打开 AI 问答抽屉，将其向左拖拽拉大至 `1000px+`，点击右上角 `✕` 按钮，确认抽屉 100% 平滑收起消失。
3. **SSE 长链接与图谱热更新验证**：在浏览器打开静态 Wiki 页面，通过 AI 问答触发生成新 Wiki，观察 SSE 接收到 `reload` 事件，页面弹窗提示 `Wiki updated` 并且 Graph 中实时长出新节点。
4. **构建期图谱版本隔离验证**：在后台触发构建，确认构建期间静态访问依然读取老版本的 `api/graph` 和静态资源；构建成功后无缝切换至新版 graph。
5. **管理员全局审计验证**：管理员进入“问答审计”，确认可无障碍查看全员问答日志。
6. **登录 UI 与报错验证**：验证输入框 Label 为“AD 域账号”，输入错误密码测试友好中文提示。
