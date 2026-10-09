# OpenWiki Portal 体验优化与仓库运维分页查询实施计划

> 本计划针对用户提出的 4 项核心诉求制定：
> 1. 问答界面经常有 mermaid 显示不出来，报 mermaid 语法错误
> 2. 问答界面中的超链接点不开是 404
> 3. 默认 nginx 根跳转到 portal 失败且 404
> 4. 仓库运维与构建页面添加分页与查询

---

## 一、 根本原因诊断与方案设计

| 需求 | 根本原因定位 | 解决方案 |
| :--- | :--- | :--- |
| **1. 问答界面 Mermaid 报错** | ① `qa-daemon.js` 中的 `CodeAntiLeakFilter` 防代码泄漏过滤器无差别在第 15 行截断所有代码块（包括 mermaid 图），并插入中文提示，导致 mermaid 结构被破坏报 `Syntax error`<br>② 前端 `dashboard.js` 采用全局 `mermaid.run({ nodes })` 批量渲染，一个图的小瑕疵会导致整个页面红字报错。 | ① `CodeAntiLeakFilter` 识别 `mermaid` / 图表语言并**豁免截断**；提示词明确 mermaid 规范；<br>② 前端改造为**逐图独立容错渲染**，做语法自动清洗；渲染异常时优雅降级为代码块展示，绝不破坏页面。 |
| **2. 问答中的超链接 404** | AI 输出知识库文档链接（如 `[quickstart.md](openwiki/quickstart.md)`），marked 默认生成相对链接 `<a href="...">`，在 `/portal/` 页面点击会直接请求 `/portal/openwiki/xxx` 报 404。 | 在 `api.js` 和 `dashboard.js` 统一拦截超链接：内部文档相对路径自动重定向至当前所选仓库的静态 Wiki 路由 `/wiki/<repo_id>/#<node>`；外部链接自动以 `target="_blank"` 打开。静态 `client.js` 补齐 hash 自动定位。 |
| **3. Nginx 根跳转 404** | `deploy/nginx.conf` 中**未配置 `location /portal/` 代理**，且根目录重定向目标被硬编码成了 `/wiki/openwiki/`，在没有该 repo 时产生 404。 | 在 `deploy/nginx.conf` 增加 `location /portal/` 反向代理至 Portal 服务 (8080 端口)，并将根路径 `/` 302 重定向到 `/portal/`。 |
| **4. 仓库运维与构建分页与查询** | `admin.html` 与 `admin.js` 当前直接将所有仓库全量渲染到 `<tbody>`，缺少搜索查询输入框与分页器组件。 | 参考 `dashboard.js` 的成熟分页体系，在 `admin.html` 表格上方加入**实时模糊搜索框**与**每页条数选择器**，下方加入**分页控制器**（上一页/下一页/页码/总数），并在 `admin.js` 中实现切片分页与状态保持。 |

---

## 二、 任务分解清单

### Task 1: 修复 Mermaid 语法错误与渲染体验
- **后端守护进程豁免**：修改 `deploy/unit1-orchestrator/scripts/qa-daemon.js` 中的 `CodeAntiLeakFilter`，对以 ````mermaid` 或 ````plantuml` 开头的块不做 15 行截断；
- **Prompt 引导**：系统提示词增加对 mermaid 语法的约束（节点名含特殊符号时加双引号）；
- **前端逐图容错渲染**：修改 `deploy/unit2-portal/public/js/dashboard.js` 中的 `renderMermaidInElement`，对每个 mermaid 节点独立 try-catch，渲染失败时安全降级为高亮代码块。

### Task 2: 修复问答超链接 404
- **Markdown 链接解析增强**：在 `deploy/unit2-portal/public/js/api.js` 中规范化链接属性，外链增加 `target="_blank"`；
- **Wiki 路径跳转映射**：在 `deploy/unit2-portal/public/js/dashboard.js` 中增加链接点击事件委托，若点击的是知识库相对链接，自动跳转到 `/wiki/${activeRepo.id}/#${cleanDocId}`；
- **静态 Wiki Hash 定位**：修改 `deploy/data/static/test/client.js` 及资产模板，支持页面根据 URL Hash 自动展开并选中对应的文档节点。

### Task 3: 修复 Nginx 根路径跳转与 `/portal/` 代理
- **补齐 Portal 反向代理**：在 `deploy/nginx.conf` 中添加 `location /portal/` 代理规则，转发至 `http://127.0.0.1:8080/portal/`；
- **修正根目录重定向**：将 `location = /` 改为 `return 302 /portal/;`；
- **多架构配置同步**：同步更新 `deploy/deploy/nginx.conf`。

### Task 4: 仓库运维与构建页面添加搜索与分页
- **页面结构扩展 (`admin.html`)**：
  - 在卡片表格上方增加搜索输入框与每页条数下拉选择框；
  - 在卡片表格下方增加分页导航栏容器 `adminPaginationBar`；
- **状态与逻辑实现 (`admin.js`)**：
  - 维护 `searchKeyword`, `currentPage`, `pageSize` 响应式状态；
  - 实现按仓库 ID、名称、Git 地址、分支的模糊搜索与防抖输入；
  - 实现当前页数据切片（slice）渲染与分页组件渲染；
  - 触发构建及状态轮询时，保持当前搜索条件与页码。

### Task 5: 资产同步与全功能端到端回归验证
- 同步相关 JS/HTML/conf 文件至 `deploy/bin/x86/` 与 `deploy/bin/arm/`；
- 现场进行四大功能点的回归验证与验证日志记录。
