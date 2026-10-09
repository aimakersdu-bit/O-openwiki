# OpenWiki Portal 体验优化、仓库运维分页查询与默认分支调整实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 解决 Portal 问答 Mermaid 语法错误、超链接 404、Nginx 根路径跳转 404，在“仓库运维与构建”页面实现关键字搜索与分页控制，并将“添加仓库”默认分支调整为 `master`。

**Architecture:** 
1. 后端守卫层 (`qa-daemon.js`) 排除图表语言防泄漏截断并增强 Prompt；前端渲染层 (`dashboard.js` & `api.js`) 采用逐图 try-catch 容错渲染与自动纠错。
2. 问答链接层统一拦截 Wiki 相对路径，转换为当前仓库 `/wiki/<repo_id>/#<node>`，外链新窗口打开。
3. Nginx 层补齐 `location /portal/` 反向代理并将根路径重定向改为 302 `/portal/`。
4. 运维前端 (`admin.html` + `admin.js`) 引入统一分页组件与实时搜索过滤，并将“添加仓库”表单默认分支更新为 `master`，后端缺省分支同步对齐。

**Tech Stack:** JavaScript (ES6+), HTML5, CSS3, Go (Unit 1 & Unit 2), Nginx, Mermaid.js, Marked.js.

---

### Task 1: 修复 Mermaid 渲染与语法错误（后端豁免 + 前端逐图容错渲染）

**Files:**
- Modify: `deploy/unit1-orchestrator/scripts/qa-daemon.js:140-168`
- Modify: `deploy/bin/x86/scripts/qa-daemon.js:140-168`
- Modify: `deploy/bin/arm/scripts/qa-daemon.js:140-168`
- Modify: `deploy/unit2-portal/public/js/dashboard.js:505-535`
- Modify: `deploy/bin/x86/public/js/dashboard.js:505-535`
- Modify: `deploy/bin/arm/public/js/dashboard.js:505-535`

- [ ] **Step 1: 修改 `qa-daemon.js` 中的 `CodeAntiLeakFilter`，对图表语言（mermaid, plantuml 等）豁免截断**
  - 在 `CodeAntiLeakFilter` 中记录当前代码块语言：如果为 `mermaid`、`plantuml` 等图表声明，不进行 15 行截断，保持结构完整；
  - 在 `promptPrefix` 系统提示词中，增加关于 mermaid 语法的中文指导：“若绘制 mermaid 图，请确保语法严谨合法，节点名称含空格或特殊符号请使用双引号包裹”。

- [ ] **Step 2: 升级前端 `dashboard.js` 的 `renderMermaidInElement` 为单图隔离容错渲染**
  - 避免使用批处理 `mermaid.run({ nodes })`，改为对每个 `.mermaid` 节点逐个独立处理；
  - 自动预清洗：去除可能混入的安全占位注释，修正中文字符及常见语法符号；
  - 若渲染失败，优雅回退显示格式化代码块与复制按钮，并在底部提示“图表语法有瑕疵，已回退为代码展示”，绝不破坏整个聊天界面。

- [ ] **Step 3: 验证 Mermaid 容错与渲染**
  - 构造包含常规 mermaid 以及超长 mermaid 的测试用例，验证不再出现红字语法错误崩溃。

---

### Task 2: 修复问答界面超链接 404 问题

**Files:**
- Modify: `deploy/unit2-portal/public/js/api.js:130-150`
- Modify: `deploy/unit2-portal/public/js/dashboard.js:760-825`
- Modify: `deploy/data/static/test/client.js:475-485` (以及 Unit 1 assets client.js)
- Modify: `deploy/bin/x86/public/js/...` & `deploy/bin/arm/public/js/...`

- [ ] **Step 1: 扩展 `api.js` 中 `renderMarkdown` 的自定义 Link 渲染逻辑**
  - 使用 marked 自定义 renderer 重写链接：
    - `http://` / `https://` 开头的外链：自动增加 `target="_blank" rel="noopener noreferrer"`；
    - 相对路径或以 `.md`、`/openwiki/` 开头的内部链接：解析出目标文档名称 `docId`，根据当前上下文生成 `data-wiki-doc="<docId>"`；
  - 若未配置 marked 自定义 renderer，在 `dashboard.js` 中对 `contentDiv` 进行事件委托代理。

- [ ] **Step 2: 在 `dashboard.js` 中增加 Wiki 链接点击委托**
  - 当用户在聊天气泡中点击 Wiki 链接时：
    - 阻止默认相对路径跳转（避免跳向 `/portal/openwiki/xxx` 报 404）；
    - 若当前存在 `activeRepo`，自动重定向或在新标签页打开 `/wiki/${activeRepo.id}/#${cleanDocId}`；
    - 若外部链接，则正常 `window.open(href, '_blank')`。

- [ ] **Step 3: 静态 Wiki `client.js` 支持根据 URL Hash 自动定位高亮文档**
  - 在静态页面加载完成后检查 `window.location.hash`，若存在 `#docId`，自动触发 `selectNode(docId)` 并渲染详情面板。

---

### Task 3: 修复 Nginx 根目录跳转与 `/portal/` 反向代理 404

**Files:**
- Modify: `deploy/nginx.conf:56-104`
- Modify: `deploy/deploy/nginx.conf:56-104` (如有)

- [ ] **Step 1: 在 `deploy/nginx.conf` 中增加 `/portal/` 代理路由**
  ```nginx
  # 0. Portal 控制台与前端资产代理 (/portal/)
  location /portal/ {
      proxy_pass http://127.0.0.1:8080/portal/;
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
  }
  ```

- [ ] **Step 2: 修改根路径重定向**
  ```nginx
  # 根目录默认重定向至 /portal/
  location = / {
      return 302 /portal/;
  }
  ```

- [ ] **Step 3: 验证 Nginx 配置**
  - 检查配置文件语法并验证从 `/` 302 跳转到 `/portal/`，以及直接访问 `/portal/` 返回 200。

---

### Task 4: 仓库运维与构建页面添加查询搜索与分页控制，且添加仓库默认分支改为 `master`

**Files:**
- Modify: `deploy/unit2-portal/public/admin.html:50-110`
- Modify: `deploy/unit2-portal/public/js/admin.js:50-240`
- Modify: `deploy/unit1-orchestrator/internal/api/repos.go:70-80`
- Modify: `deploy/bin/x86/public/...` & `deploy/bin/arm/public/...`

- [ ] **Step 1: 将添加仓库界面的默认分支更新为 `master`**
  - 修改 `admin.html` 中分支输入框的默认值：`<input type="text" id="branch" value="master" required>`；
  - 修改 `admin.js` 中表单重置（`registerForm.reset()`）后的默认分支回填为 `master`；
  - 修改后端 `unit1-orchestrator/internal/api/repos.go` 中空分支缺省值：若 `req.Branch == ""` 则 `req.Branch = "master"`。

- [ ] **Step 2: 在 `admin.html` 仓库表格卡片中添加搜索栏与分页控制容器**
  - 表格上方添加搜索栏：
    - 输入框：`adminSearchInput`（支持按仓库 ID、名称、Git 地址、分支模糊搜索）；
    - 下拉框：`adminPageSizeSelect`（每页展示条数：10、20、50、全部）；
  - 表格下方添加分页工具条：
    - 容器：`adminPaginationBar`（展示总记录数、当前页/总页数、上一页/下一页及数字按钮）。

- [ ] **Step 3: 在 `admin.js` 中实现过滤与分页状态机**
  - 定义状态变量：`searchKeyword = ''`, `currentPage = 1`, `pageSize = 10`；
  - 实现 `filterRepos()`：根据关键字过滤 `cachedRepos`；
  - 实现 `renderAdminPage()`：对过滤后的列表进行分页切片（slice），渲染当前页的表格行；
  - 实现 `renderAdminPagination(totalItems)`：生成分页按钮并绑定点击事件；
  - 搜索输入框绑定 `input` 防抖事件，输入时重置为第 1 页并更新视图；
  - 刷新或构建状态轮询时，保持用户的搜索输入和当前页码不丢失。

- [ ] **Step 4: 编译 Orchestrator 并验证默认分支与分页逻辑**
  - 运行单元测试并重新编译 `bin_orchestrator`；
  - 验证添加仓库时默认分支为 `master`，验证分页与查询生效。

---

### Task 5: 全面端到端回归测试与静态资产同步

**Files:**
- Sync: 同步修改至 `deploy/bin/x86/` 与 `deploy/bin/arm/`
- Run: 针对 Portal 和 Orchestrator 进行全面联调

- [ ] **Step 1: 同步脚本与公共前端代码至二进制发行目录**
  - 同步 `admin.html`, `admin.js`, `dashboard.js`, `api.js`, `qa-daemon.js`, `nginx.conf`, `repos.go` (重新编译)。
- [ ] **Step 2: 端到端功能点验证**
  - 1. Mermaid 语法容错验证；
  - 2. 问答气泡超链接点击行为验证；
  - 3. Nginx `/` 重定向与 `/portal/` 访问验证；
  - 4. 仓库运维表格分页与实时搜索验证；
  - 5. 添加仓库界面分支默认值为 `master` 验证。
