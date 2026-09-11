# 百信 RepoWiki AI 问答多轮会话管理与全功能界面设计 实施计划

根据对 OpenWiki 原生 TUI 机制与企业门户 UI/UX 的比对分析，本计划旨在为 **百信 RepoWiki** 打造媲美 ChatGPT / Claude 体验的**企业级多轮 AI 问答会话管理系统**。

---

## 一、 核心功能设计

### 1. 多轮对话与上下文延续 (Multi-turn Context Continuation)
- **底层串联**：前端问答抽屉为每次新对话分配 `session_id`（格式为 `sess_<timestamp>_<random>`）。
- **LangChain / DeepAgents 绑定**：向 `/api/chat` 发送问题时传入 `session_id`，`qa-daemon.js` 将其作为 `thread_id` 传递给 OpenWiki Agent 引擎，自动加载并更新 Checkpointer 内存上下文，实现追问与上下文继承。

### 2. 用户专属历史会话列表 (User Session History)
- **按用户隔离**：每个登录用户在当前仓库下只能查看、检索与管理属于自己的历史会话。
- **会话卡片信息**：展示会话标题（首个提问前30字）、最后更新时间、消息对话轮数及“🗑️ 删除会话”快捷操作。

### 3. 会话自由切换与新建 (Session Switching & New Chat)
- **一键新建 (`➕ 新建会话`)**：清空当前聊天气泡，分配全新 `session_id`，准备开始新维度的代码探索。
- **历史载入 (`📜 会话历史`)**：点击历史列表中的任意会话，自动加载该会话下的所有历史问答记录，并将当前对话上下文锚定到该 `session_id` 继续追问。

### 4. 模型与 Provider 动态切换 (Model / Provider Selector)
- 抽屉顶部支持动态选框或快捷指令，允许用户在已配置的模型（如 DeepSeek、OpenAI、Gemini）间自由切换。

---

## 二、 界面 UI/UX 设计方案

```mermaid
graph TD
    subgraph AI 问答侧边抽屉 (#chatDrawer - 支持左右拖拽调整宽度)
        Header[顶栏工具条: 仓库名 | ➕ 新建会话 | 📜 历史列表 | ⚙️ 模型切换 | ✖️ 关闭]
        
        subgraph 折叠/滑动面板 (#sessionHistoryPanel)
            S1[会话 1: "这个项目的核心架构..." - 3轮 - 13:40]
            S2[会话 2: "Redis 缓存配置在哪..." - 5轮 - 10:20]
        end
        
        subgraph 聊天内容滚动区 (#chatMessages)
            UserMsg[用户提问气泡: 右侧天蓝色]
            BotMsg[AI 回答气泡: 左侧深色 + 流式 Markdown 渲染 + 代码高亮]
            StatusMsg[状态提示: 🔍 正在检索/调用工具...]
        end
        
        Footer[底栏输入区: 多行 Textarea + Ctrl+Enter 发送按钮]
    end
```

---

## 三、 拟修改文件与模块划分

---

### [MODIFY] [sqlite.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/db/sqlite.go)

1. **数据库结构迁移**：
   - 为 `qa_sessions` 表添加 `session_id TEXT` 字段（保留向后兼容）。
   - 添加索引 `CREATE INDEX IF NOT EXISTS idx_qa_sessions_user_repo ON qa_sessions(repo_id, user_id);` 与 `CREATE INDEX IF NOT EXISTS idx_qa_sessions_session ON qa_sessions(session_id);`。
2. **扩展 DB 方法**：
   - `CreateQAMessage(sessionID, repoID, userID, question, answer string) error`
   - `ListUserQASessions(repoID, userID string, limit int) ([]SessionSummary, error)`：按 `session_id` 聚合返回首个问题标题、消息数及最后更新时间。
   - `GetQASessionMessages(sessionID, userID string) ([]QASession, error)`：按时间升序返回特定 session_id 的全部历史对话。
   - `DeleteQASession(sessionID, userID string) error`：删除特定会话。

---

### [MODIFY] [chat.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/api/chat.go) & [server.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/api/server.go)

1. 更新 `handleChat` 持久化逻辑：在 SSE 流式推送完成（`done`）后，将 `session_id` 与提问回答同步写入 DB。
2. 新增 REST API：
   - `GET /api/qa/sessions`：查询当前用户在该仓库下的会话摘要列表。
   - `GET /api/qa/messages`：查询特定 `session_id` 的历史消息列表。
   - `DELETE /api/qa/sessions`：删除指定的会话记录。

---

### [MODIFY] [repos.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/repos.go) & [server.go](file:///Users/bigc/openwiki/deploy/unit2-portal/internal/api/server.go)

在 Unit 2 Portal 服务中增加代理路由，安全校验 Session 中的 Cookie UserID 并透传：
- `GET /portal/qa/sessions` -> 代理到 Orchestrator `GET /api/qa/sessions`
- `GET /portal/qa/messages` -> 代理到 Orchestrator `GET /api/qa/messages`
- `DELETE /portal/qa/sessions` -> 代理到 Orchestrator `DELETE /api/qa/sessions`

---

### [MODIFY] [index.html](file:///Users/bigc/openwiki/deploy/unit2-portal/public/index.html)

重构 `#chatDrawer` 模板结构：
- 在 `.chat-header` 增加操作按钮栏：`➕ 新建会话`、`📜 历史会话`。
- 增加滑出式会话列表容器 `#sessionHistoryPanel`。

---

### [MODIFY] [style.css](file:///Users/bigc/openwiki/deploy/unit2-portal/public/css/style.css)

增加会话列表面板、历史会话条目卡片、删除按钮及新建按钮的 CSS 样式规范（适配现有的暗黑科技风）。

---

### [MODIFY] [api.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/js/api.js)

添加前端 API 封装：
- `API.getUserQASessions(repoId)`
- `API.getQASessionMessages(sessionId)`
- `API.deleteQASession(sessionId)`

---

### [MODIFY] [dashboard.js](file:///Users/bigc/openwiki/deploy/unit2-portal/public/js/dashboard.js)

1. **抽屉初始化**：点击仓库“💬 AI 问答”打开抽屉时，自动拉取当前用户在该仓库下的历史会话列表。
2. **新建会话**：点击 `➕ 新建会话` 时，生成新 UUID `session_id`，清空消息列表，恢复默认 Welcome 提示。
3. **会话切换**：点击历史会话卡片时，调用 `getQASessionMessages` 拉取历史问答，用 `API.renderMarkdown` 批量渲染到聊天区，并设置 `currentSessionId = selectedSessionId`。
4. **流式续问**：后续发送任何新提问，继续使用选中的 `currentSessionId`，回答完成后自动刷新历史会话列表。

---

## 四、 验证计划

### 1. 单元与接口测试
在 `deploy/unit1-orchestrator` 和 `deploy/unit2-portal` 中运行 Go 单元测试：
```bash
go test -v ./internal/db/...
go test -v ./internal/api/...
```

### 2. 交互功能验证
1. **多轮追问测试**：提问“描述此项目” -> 追问“它的入口文件在哪里？” -> 验证 AI 回答能结合上一轮上下文。
2. **新建与切换测试**：点击“新建会话”，发送新维度问题；点击“历史会话”切换回上一个会话，验证对话历史恢复且可接着追问。
3. **隔离与删除测试**：使用不同用户登录，验证会话列表隔离；测试删除会话功能。
