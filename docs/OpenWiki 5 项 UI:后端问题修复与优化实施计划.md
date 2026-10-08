# OpenWiki 5 项 UI/后端问题修复与优化实施计划

本计划旨在解决用户在 OpenWiki 静态页面查看、QA 问答审计、会话历史列表、对话流控制以及 Mermaid 图表渲染中遇到的 5 个核心问题。

---

## 问题诊断与修复方案

### 1. 静态页面在部分电脑/浏览器报错 (SyntaxError & ReferenceError)
- **原因分析**：
  - 通过截图分析，报错为 `Uncaught SyntaxError: Unexpected token '{'` 以及 `Uncaught ReferenceError: mermaid is not defined at client.js:554`。
  - 根因：`download-vendor.sh` 抓取的是 `unpkg.com` 的最新版本 `mermaid.min.js` (v10+ / v11+)。新版使用了 ES2022 的类静态初始化块语法 (`static { ... }`)，部分较旧版本的 Chrome/Edge 浏览器或壳浏览器无法解析导致 `SyntaxError`，使得 `window.mermaid` 加载失败。
  - `client.js` 无防御性判断，在 bootstrap 阶段直接执行 `mermaid.initialize(...)`，引发 `ReferenceError` 并导致整个页面 JS 执行中断。
- **修复方案**：
  1. 锁定并更新 `download-vendor.sh` 中的 `mermaid.min.js` 依赖地址为兼容性更好的 UMD 构建版本（如 `mermaid@10.6.1` 或 `9.4.3` 标准打包），去除未编译的 ES2022 语法。
  2. 在 `client.js` 中增加全局变量检测与 `try-catch` 防御包装，确保即使 `mermaid` 脚本因网络/插件拦截加载失败，主视效与 Markdown 阅读器依然能正常工作。

### 2. 问答审计记录查不到其他人的记录
- **原因分析**：
  - `deploy/unit2-portal/internal/api/sessions.go` 中的 `handleSessions` (针对 `/portal/sessions`，即审计页面 `history.html` 使用) 强制以 `user_id = session.UserID` 请求 Orchestrator `/api/qa/history`，未判断 `session.Role == "admin"`。
  - 同理，`handleQAMessages` (针对 `/portal/qa/messages`) 无论是否为管理员均传递 `user_id = session.UserID`，导致管理员在审计页点击他人会话查看详情时被拦截或返回空记录。
- **修复方案**：
  1. 在 `handleSessions` 中增加管理员身份判断：若 `session.Role == "admin"`，则向 Orchestrator 传递 `user_id=""`（查询所有用户的问答记录）。
  2. 在 `handleQAMessages` 中增加管理员身份判断：若 `session.Role == "admin"`，传递 `user_id=""`，允许管理员调阅任意会话的完整问答明细。

### 3. 历史对话名称显示乱码/无意义字符串 (`sess_17266...`)
- **原因分析**：
  - `deploy/unit2-portal/public/js/dashboard.js` 第 249 行使用 `sess.first_query || ('会话 ' + sess.session_id)`。
  - 但后端 `ListUserQASessions` 返回的 JSON 字段名是 `title`（已自动截取首个问题的 30 字符），不存在 `first_query` 字段。
  - 导致 `sess.first_query` 恒为 `undefined`，降级回退显示为乱码字符串 `会话 sess_172665...` 或 `会话 legacy_123`。
- **修复方案**：
  - 修改 `dashboard.js` 中的会话标题取值逻辑，优先使用 `sess.title`，其次使用 `sess.question` 或 `新对话`。

### 4. 对话处理中增加“停止”按钮，停止后才能再次对话
- **原因分析**：
  - 目前 `dashboard.js` 的 `sendQuestion` 没有接入 `AbortController`，对话生成过程中发送按钮未切换，用户无法中断当前正在流式输出或卡顿的回答，也可能导致重复触发提问。
- **修复方案**：
  1. 在 `dashboard.js` 中引入全局/会话级 `AbortController`。
  2. 在对话生成中（`status` / `delta` 阶段）：
     - 禁用输入框 `chatInput.disabled = true`。
     - 将“发送”按钮文案更新为“⏹️ 停止”，样式切换为停止态。
  3. 用户点击“停止”按钮时：
     - 调用 `abortController.abort()` 中断 Fetch SSE HTTP 连接。
     - 在消息区域末尾追加提示 `[对话已手动停止]`。
     - 恢复输入框与发送按钮状态。

### 5. AI 对话返回的 Mermaid 图表无法渲染出图片
- **原因分析**：
  - `unit2-portal/public/index.html` 页面未引入 `mermaid.min.js`，`public/vendor/` 目录下缺失该依赖。
  - `dashboard.js` 使用 `API.renderMarkdown(fullMarkdown)` 将 fenced code (````mermaid ... ````) 转为 `<pre><code class="language-mermaid">` 后，未调用 `mermaid.run()` 进行 SVG 节点渲染。
- **修复方案**：
  1. 将兼容版 `mermaid.min.js` 复制到 `unit2-portal/public/vendor/` 并在 `index.html` 中引入。
  2. 在 `dashboard.js` 中实现 `renderMermaid()` 函数，在流式回答完成（`done` 事件）或每次防抖更新时，对 `.language-mermaid` 节点执行 `mermaid.run()` 渲染出可视化图表。

---

## 拟修改文件列表

### [Component] Unit 1 - Orchestrator (静态资源与底层库)
- #### [MODIFY] [download-vendor.sh](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/scripts/download-vendor.sh)
  - 调整 `mermaid.min.js` 下载源为广泛兼容的 UMD 版本。
- #### [MODIFY] [client.js](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/assets/client.js)
  - 增加对 `mermaid` 对象的防崩溃防御性校验。

---

### [Component] Unit 2 - Portal (后端 API 与 权限控制)
- #### [MODIFY] [sessions.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/sessions.go)
  - 在 `handleSessions` 与 `handleQAMessages` 中允许 `admin` 角色查询所有用户的审计记录与会话详情。

---

### [Component] Unit 2 - Portal (前端 UI 与 交互 logic)
- #### [MODIFY] [index.html](file:///Users/bigc/openwiki/deploy/unit2-portal/public/index.html)
  - 引入 `vendor/mermaid.min.js` 依赖。
- #### [NEW] [mermaid.min.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/vendor/mermaid.min.js)
  - 补充静态依赖。
- #### [MODIFY] [dashboard.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/js/dashboard.js)
  - 修复历史会话标题取值 `sess.title`。
  - 增加 `AbortController`，“停止生成”按钮流式控制逻辑。
  - 增加 `renderMermaid()` 对对话内 Mermaid 代码块渲染的支持。

---

## 验证计划

### 自动化/命令行验证
1. 运行 `download-vendor.sh` 确保离线静态包正常下载。
2. 运行 `go test ./...` 校验 `unit2-portal` 与 `unit1-orchestrator` Go 后端包无编译与逻辑错误。

### 手动验证
1. **静态页面兼容性**：在低版本 Chrome 或模拟环境打开 `/wiki/<repo>/` 静态页面，确认无 SyntaxError 控制台报错，图表正常渲染。
2. **审计记录查询**：使用管理员账号登录 Portal 访问 `history.html`，确认能检索并查看所有用户的历史问答与会话明细。
3. **历史对话名称**：在 AI 问答抽屉点开“📜 历史”，确认显示的会话标题为用户提问的简短问题（如“这个项目的核心模块...”），而非无意义 ID。
4. **停止生成交互**：在 AI 问答中发送耗时提问，提问中点击“⏹️ 停止”按钮，确认流传输立即中断，且输入框恢复可再提问状态。
5. **Mermaid 图表渲染**：提问要求 AI 返回架构图（如“用 mermaid 画出这个项目的架构图”），确认回答渲染出直观的 Mermaid SVG 流程图。
