# 自定义文档培训演讲 SPA (Single Page Application) 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个现代、高交互、双模式（全屏幻灯片演讲 + 交互式沙箱演练台）的单页应用 (SPA)，并以真实生产级 **ZooKeeper (ZK) 基础架构与业务协同** 作为完整演示代码库案例，向使用版块输出端到端的**具体实施 SOP** 与 **IDE/MCP 智能体协作全流程**。

**Architecture:** 单文件现代化纯前端 SPA（支持本地离线双击打开，亦可通过 Portal 静态服务访问）。内嵌 Tailwind CSS、Lucide 图标库、Marked.js Markdown 引擎、Mermaid 流程图解析器以及 Canvas 2D 力导向知识图谱渲染器。预装完整的 ZK 演示仓库样例数据（包含集群架构、分布式锁规范、高频故障 FAQ 及 Mermaid 时序图），支持一键切换 PPT 讲授模式、SOP 实施推演、及 GitOps+MCP 互动沙箱。

**Tech Stack:** HTML5, CSS3, Tailwind CSS (CDN), Marked.js, Canvas 2D Force-Directed Graph Engine, Vanilla JavaScript (ES6+), Mermaid.js。

---

## 产出文件与职责划分

| 文件路径 | 状态 | 职责说明 |
| :--- | :--- | :--- |
| `docs/superpowers/plans/2026-11-17-custom-docs-training-spa.md` | **已更新** | 包含 ZK 案例与 SOP 细化后的完整实施计划。 |
| `deploy/unit2-portal/public/training-custom-docs.html` | **新建** | 核心 SPA 单页应用，部署在 Portal 静态资源目录（可浏览器直开或通过 `http://<portal>/training-custom-docs.html` 访问）。 |
| `docs/training/custom-docs-training.html` | **新建** | 存放在 `docs/training/` 下的备份与文档归档。 |

---

## 核心设计与内容架构（融入 ZK 演示仓库与 SOP）

### 1. ZooKeeper (ZK) 专属演示代码库设计
在 SPA 沙箱与 PPT 中内置一个开箱即用的真实样例仓库 `infrastructure/zk-service-mesh`：
- **仓库根目录配置**：`.openwikirc`（演示显式配置 `knowledge_dir: "doc/knowledge"`）
- **核心文档 1：分布式协调与集群规划 (`doc/knowledge/zk-cluster-deployment.md`)**
  - 角色划分（Leader / Follower / Observer）、奇数节点容灾、Mermaid 选主流程图。
- **核心文档 2：分布式锁落地与防惊群规范 (`doc/knowledge/zk-distributed-lock.md`)**
  - 基于 Ephemeral Sequential 临时顺序节点机制、Watch 监听前序节点的 Mermaid 时序图、超时与重试规范。
- **核心文档 3：生产环境高频踩坑与排错手册 (`doc/knowledge/zk-troubleshooting-faq.md`)**
  - JVM GC 停顿引发 Session Expired、脑裂（Split-Brain）预防、快照清理脚本与告警阈值。
- **静态图片资产演示**：
  - `doc/knowledge/_assets/zk-architecture.svg`（矢量架构图，验证流水线同步不丢图）。

### 2. 业务版块端到端实施 SOP (Standard Operating Procedure)
SPA 中特别提供一份**业务团队标准作业程序 (SOP) 步骤看板**（支持交互勾选与命令一键复制）：
1. **SOP-01 规范初始化**：在业务代码仓库根目录下创建 `doc/knowledge/` 目录（或配置 `.openwikirc`）。
2. **SOP-02 编写与元数据补全**：编写 Markdown 文档，首行声明一级标题 `# 文档名`，系统自动补齐 Frontmatter（`type: Knowledge`），流程图使用 `mermaid` 语法，图片放入 `_assets/`。
3. **SOP-03 本地静态与语法自检**：检查文档相对路径引用、代码块与链接规范。
4. **SOP-04 GitOps 提交与触发同步**：通过 Git PR 合并或推送代码；日间走极速通道（`mode: knowledge-only`），跳过 LLM 全量扫描，秒级生效。
5. **SOP-05 三端成果验收**：
   - 验证 Portal 静态阅读器目录树与富文本；
   - 验证 3D 知识图谱中 ZK 知识节点与代码服务节点的连线并网；
   - 验证 Cursor / IDE 中通过 MCP 提问获取精准回答。

---

## 任务拆解与执行步骤

### 任务 1：搭建单页应用骨架与现代化沉浸式 UI
- [ ] **步骤 1**：创建 `deploy/unit2-portal/public/training-custom-docs.html`，配置 HTML5 结构、Tailwind CSS、Google 字体及图标。
- [ ] **步骤 2**：设计顶部控制栏，提供【幻灯片演讲】、【SOP 实施指南】、【ZK 交互演练沙箱】三大视图 Tab 快速切换，支持一键全屏模式 (`F` 键快捷响应)。

### 任务 2：实现模式一：全屏互动幻灯片系统 (Slide Deck - 包含 ZK 案例)
- [ ] **步骤 1**：编写 6 幕沉浸式大屏演讲幻灯片：
  - Slide 1: 业务文档痛点与 OpenWiki“代码+业务双大脑”定位；
  - Slide 2: GitOps 零侵入设计理念（`src/` 纯净 vs `doc/knowledge/` 同源）；
  - Slide 3: 目录发现优先级与 `.openwikirc`；
  - Slide 4: ZK 实战案例剖析（分布式锁时序图、集群规范、图片引用）；
  - Slide 5: 白天快速同步 (`knowledge-only`) 与夜间增量构建 (`full`)；
  - Slide 6: IDE 智能体（Cursor / Windsurf）通过 MCP 消费 ZK 知识。
- [ ] **步骤 2**：实现幻灯片控制器逻辑（键盘 `←` `→` `Space` 翻页、全屏、进度条提示）。

### 任务 3：实现模式二：业务版块实施 SOP 标准看板
- [ ] **步骤 1**：构建交互式 SOP 操作指引，涵盖初始化、编写规范、资产管理、GitOps 触发及三端验收 5 大步骤。
- [ ] **步骤 2**：提供可交互的命令一键复制器（`mkdir -p doc/knowledge/_assets`、curl 触发快速同步等）。

### 任务 4：实现模式三：ZK 真实案例交互实战沙箱 (Interactive Playground)
- [ ] **步骤 1**：左侧构建 ZK 仓库文件树（`zk-cluster-deployment.md`、`zk-distributed-lock.md`、`zk-troubleshooting-faq.md`、`.openwikirc`），支持点击切换查看与在线编辑。
- [ ] **步骤 2**：中间构建 GitOps 模拟控制台，点击 "Commit & Sync" 按钮，实时终端高亮打印 `knowledge.go` 与 `builder.go` 的标准构建日志。
- [ ] **步骤 3**：右侧三端联动呈现：
  - **Wiki 视图**：实时 Markdown 渲染 + Mermaid 流程图/时序图动态解析。
  - **知识图谱视图**：Canvas 力导向图实时绘制，呈现 `zk-service` 代码拓扑与新注入的 ZK 业务文档节点聚类并网。
  - **AI / MCP 模拟器**：模拟在 Cursor 中提问：“ZK 发生 Session Expired 怎么处理？”，动态演示调用 `read_repository_doc` 工具抓取文档并流式打字输出专业解答。

### 任务 5：备份、集成测试与验证
- [ ] **步骤 1**：同步创建 `docs/training/custom-docs-training.html`。
- [ ] **步骤 2**：在本地浏览器中全面测试全屏翻页、SOP 流程、ZK 沙箱编辑、Mermaid 渲染、图谱动力学动画及 MCP 问答模拟。
