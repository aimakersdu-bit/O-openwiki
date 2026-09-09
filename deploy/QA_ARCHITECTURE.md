# OpenWiki AI 问答架构与会话状态设计文档

本文档详细记录 OpenWiki 高性能 AI 问答架构、Go 封装层与 LangChain/LangGraph 核心层的交互原理、会话 ID 映射机制以及多轮上下文管理方案，供系统维护与后续迭代查询。

---

## 1. 总体架构与职责边界

系统采用 **"Go 负责管控层 (Control Plane)，Node 负责运行时数据面 (Data Plane)"** 的双层架构：

```
+-----------------------------------------------------------------------------------------+
|                                    Unit 2 - Portal (前端与网关)                          |
+--------------------------------------------+--------------------------------------------+
                                             | HTTP SSE / REST (/api/chat)
                                             v
+-----------------------------------------------------------------------------------------+
|                                 Unit 1 - Orchestrator (Go API 服务)                     |
|  - 路由分发与鉴权                                                                        |
|  - 仓库沙箱管理: manager.go                                                              |
|  - 轻量流水库: orchestrator.db (表 qa_sessions: 仅记录 user, repo, session, Q&A)        |
+--------------------------------------------+--------------------------------------------+
                                             |
                   Unix Domain Socket (UDS, 如 /tmp/openwiki-qa-<repo_id>.sock)
                                             |
            +--------------------------------+--------------------------------+
            | (2小时无访问自动销毁)              |                                |
            v                                v                                v
+-----------------------+        +-----------------------+        +-----------------------+
|  Repo Worker Daemon   |        |  Repo Worker Daemon   |        |  Repo Worker Daemon   |
|   (Repo: repo-A)      |        |   (Repo: repo-B)      |        |   (Repo: repo-C)      |
| Cwd: /var/repo-A      |        | Cwd: /var/repo-B      |        | Cwd: /var/repo-C      |
| FIFO Mutex 排队锁      |        | FIFO Mutex 排队锁      |        | FIFO Mutex 排队锁      |
+-----------+-----------+        +-----------+-----------+        +-----------+-----------+
            |                                |                                |
            v                                v                                v
+-----------------------+        +-----------------------+        +-----------------------+
| LangGraph + SQLite    |        | LangGraph + SQLite    |        | LangGraph + SQLite    |
| (.openwiki/sqlite)    |        | (.openwiki/sqlite)    |        | (.openwiki/sqlite)    |
| 深度多轮状态、Token裁剪 |        | 深度多轮状态、Token裁剪 |        | 深度多轮状态、Token裁剪 |
+-----------------------+        +-----------------------+        +-----------------------+
```

### 职责划分表：

| 模块 / 组件 | 语言 | 核心职责 |
| :--- | :--- | :--- |
| **Go Orchestrator** (`manager.go`) | Go | **控制面**：接收客户端问答请求，为每个仓库按需拉起并管理常驻 Worker 守护进程；提供 2 小时空闲自愈重试；流式透传 SSE 数据并记录历史。 |
| **qa-daemon.js** | Node.js | **数据面胶水层**：常驻运行在对应仓库目录（`cwd: repo.LocalPath`），监听 UDS，通过内存闭包直接调用 OpenWiki 原生 `runOpenWikiAgent`，零启动开销。内置 FIFO 锁规避并发竞争。 |
| **OpenWiki Agent** | TypeScript | **AI 认知核心**：AST 分析、代码搜索工具、Prompt 工程、LangGraph 状态图与大模型多轮交互。 |

---

## 2. Go 业务会话 (`session_id`) 与 LangGraph 状态机 (`thread_id`) 的映射逻辑

### 2.1 概念分层
* **`session_id`（业务会话层）**：
  - 属于前端用户交互维度。
  - 例如用户在 Portal 页面中打开某个仓库发起的一组连续对话，前端生成 `sess_1725800000_abc12`。
  - Go 仅用此 ID 关联前端对话窗口和 `orchestrator.db` 中的人类可读历史。
* **`thread_id`（智能体状态机层）**：
  - 属于 LangGraph 检查点持久化维度（`SqliteSaver`）。
  - LangGraph 依靠 `thread_id` 作为主键在 `<repo>/.openwiki/openwiki.sqlite` 的 `checkpoints` 表中查询历史快照，用于自动还原上一轮对话中的 messages 数组与 Agent 思考上下文。

### 2.2 命名空间映射规范
为了防止不同仓库、不同用户之间的会话发生 ID 碰撞，Go 层在向 Worker 派发请求时应用**带仓库前缀的命名空间映射公式**：

```go
// deploy/unit1-orchestrator/internal/qa/manager.go
threadID := sessionID
if threadID != "" {
    // 映射公式: openwiki-{repo_id}-{session_id}
    threadID = fmt.Sprintf("openwiki-%s-%s", repo.ID, sessionID)
}

payloadBytes, _ := json.Marshal(ChatPayload{
    Question:  question,
    UserID:    userID,
    SessionID: sessionID,
    ThreadID:  threadID,
})
```

### 2.3 源码级调用链流程
1. **请求进入 Worker**：`qa-daemon.js` 接收到 JSON 载荷中的 `thread_id`。
2. **传参 OpenWiki**：作为 `options.threadId` 传递给 `runOpenWikiAgent('chat', repoDir, options)`。
3. **LangGraph 状态恢复**（`src/agent/index.ts` 第 505 行）：
   ```ts
   const stream = await agent.stream(input, {
     configurable: {
       thread_id: threadId,
     },
     streamMode: ["messages", "tools"],
     subgraphs: true,
   });
   ```
4. **数据库操作**：
   - LangGraph 在 `checkpoints` 表执行：
     ```sql
     SELECT * FROM checkpoints WHERE thread_id = ? ORDER BY checkpoint_id DESC LIMIT 1;
     ```
   - 若存在，自动恢复上一轮的 `messages` 历史；
   - 本次 Agent 推理结束后，自动追加新生成的状态并写入；
   - 自动执行 `pruneCheckpointHistory(checkpointer, threadId)` 裁剪陈旧步骤，防止 SQLite 无限制膨胀。

---

## 3. 双层数据存储架构

```
                +--------------------------------------------------------+
                |                    一次多轮对话                         |
                +---------------------------+----------------------------+
                                            |
                    +-----------------------+-----------------------+
                    |                                               |
                    v                                               v
    +-------------------------------+               +-------------------------------+
    | Go 层 (orchestrator.db)       |               | LangChain 层 (openwiki.sqlite)|
    +-------------------------------+               +-------------------------------+
    | 表: qa_sessions               |               | 表: checkpoints & writes      |
    | - session_id: "sess_abc123"   |               | - thread_id: "openwiki-r-sess"|
    | - question: "这段代码什么含义?" |               | - messages: [User, AI, Tool]  |
    | - answer: "它负责..."          |               | - graph_state: 完整执行图快照  |
    |                               |               |                               |
    | 用途: 前端展示历史问答流        |               | 用途: 大模型上下文拼接与记忆恢复 |
    +-------------------------------+               +-------------------------------+
```

* **Go 数据库 (`orchestrator.db`)**：
  - **定位**：轻量级流水表。
  - **特点**：毫秒级响应前端历史列表拉取，无需解析任何复杂的 AI 结构体。
* **LangChain 数据库 (`.openwiki/openwiki.sqlite`)**：
  - **定位**：深度智能体图快照。
  - **特点**：完全利用 OpenWiki 原生 LangGraph 的序列化与反序列化机制，Go 层**零代码拼接 Prompt 历史**，上下文还原 100% 精确。

---

## 4. 为什么必须保留轻量 `qa-daemon.js`？

1. **不可替代的 TypeScript 运行时**：
   OpenWiki 的 AST 语法树解析、LangGraph 循环图、60,000+ 行智能体逻辑全部依赖 Node.js 原生生态。用 Go 纯重写 AI 引擎成本极高且容易失去后续上游同步能力。
2. **消灭冷启动开销**：
   旧方案每次问答 `exec.Command("openwiki", ...)` 需要重新加载 ESM 和 LangGraph，耗时 **1.5 ~ 2.5 秒**；常驻 `qa-daemon.js` 热进程首 Token 响应压缩至 **< 50 毫秒**。
3. **FIFO 排队锁保障数据安全**：
   OpenWiki 原生在运行时会临时覆盖 `globalThis.fetch`，且 SQLite 未配置 `busy_timeout`。`qa-daemon.js` 内部实现的 Promise FIFO 排队锁，确保同一个仓库内的多个问答串行执行，彻底根除了并发竞争崩溃。

---

## 5. 进程生命周期与资源控制 (4C8G 规格)

* **按需冷启动**：某个仓库首次收到提问时，Orchestrator 在 1 秒内拉起专属 Worker。
* **极速热响应**：后续该仓库的所有会话均享受零等待热进程极速回复。
* **2 小时空闲优雅自毁 (TTL)**：若连续 2 小时没有任何新问题，Worker 自行关闭 SQLite 连接、解除 Unix Socket 文件并调用 `process.exit(0)`，将内存 100% 归还操作系统。
* **断线自愈**：在新请求到达的毫秒级瞬间若 Worker 恰好自毁退出，Orchestrator 的 `manager.go` 会捕获连接异常，自动重新拉起并重试一次，客户端完全无感知。
