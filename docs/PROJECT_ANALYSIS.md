# OpenWiki 项目全景深度分析与模型使用指南

本文档全面梳理了 **OpenWiki** 仓库的项目定位、系统架构、关键源码模块、大模型 Agent 工作流、Connector 连接器体系、可视化图谱子系统以及内部私有（无外网）环境改造要点。旨在为后续接手或维护本仓库的大语言模型（LLM / AI Agent）提供权威、精准的上下文支撑。

---

## 1. 项目概述 (Project Overview)

**OpenWiki** 是一个基于 TypeScript 和 [DeepAgents](https://github.com/langchain-ai/deepagents) 框架开发的智能文档生成与维护 CLI 工具。

- **核心目标**：通过大模型 Agent 自动分析代码库结构与变更历史，在目标仓库根目录下生成并持续增量维护一套结构化的 Open Knowledge Format (`openwiki/`) 文档。
- **命令行入口**：`bin/openwiki` -> `dist/cli/cli.js` (`src/cli/cli.tsx`)。
- **主要能力**：
  1. **单次文档构建**：通过 `--init`（全量初始化）与 `--update`（结合 Git 提交历史增量更新）自动生成文档。
  2. **终端交互模式**：基于 React Ink 打造的终端 TUI 聊天界面，支持与 OpenWiki Agent 对话问答。
  3. **3D 可视图谱**：内置 `openwiki visualize` 命令，启动本地 Web 服务器展现 3D 节点关系图谱并支持 SSE 实时热刷新。
  4. **多模型接入**：支持 OpenAI、Anthropic、Gemini、Gemini Enterprise (Vertex AI)、GitHub Copilot、OpenRouter、AWS Bedrock、NVIDIA NIM、vLLM / Ollama (OpenAI-Compatible) 等 13+ 种 LLM Provider。
  5. **持续集成支持**：提供 GitHub Actions、GitLab CI、Bitbucket Pipelines 等自动化 CI 调度配置。

---

## 2. 核心架构与模块地图 (Architecture & Module Map)

仓库源码集中在 `src/` 目录下，整体采用了 **层次化结构 + 插件化扩展** 的设计模式：

```
src/
├── cli/                 # 命令行 UI 与生命周期控制 (Ink / React / meow 命令行解析)
│   ├── cli.tsx          # CLI 主入口，控制 TUI 渲染与流程控制
│   └── commands.ts      # 命令解析逻辑与帮助文本
├── config/              # 全局常量与 Provider 配置中心
│   └── constants.ts     # 模型 Provider 字典、环境变量映射、模型匹配与解析规则
├── agent/               # DeepAgents Agent 核心引擎与中间件
│   ├── index.ts         # Agent 运行时实例化、createModel、工具与 Mount 后端搭建
│   ├── prompt.ts        # Agent Prompt 组合器 (Prompt Builder)
│   ├── prompts/         # Code / Personal 系统的 Prompt 模板
│   ├── docs-only-backend.ts # OpenWikiLocalShellBackend (虚拟文件系统只读/写保护)
│   ├── okf-middleware.ts# OKF Frontmatter 校验与目录 index.md 自动同步中间件
│   ├── translation-middleware.ts # 多语言 Wiki 翻译中间件 (--language)
│   ├── wiki-link-validator.ts   # Wiki 内引用链接与锚点有效性校验器
│   ├── crash-guard.ts   # 进程级异常捕获保护 (Crash Guard)
│   └── utils.ts         # Git 上下文获取与 SHA-256 文档 Snapshot 比较逻辑
├── auth/                # Connector 认证与第三方 OAuth 模块 (Slack, Gmail, Notion)
├── connectors/          # 多源数据采集连接器 (Git, Web, Slack, Gmail, X, MCP)
├── visualize/           # 3D 节点关系图谱 Web 服务器与前端单页应用
│   ├── server.ts        # 离线 HTTP Web Server (内置 CSP 与 SSE 热更新)
│   ├── page.ts          # 单页应用 HTML / CSS / 脚本模板
│   ├── graph.ts         # 解析 openwiki/ 目录建立节点与关系边图谱
│   └── client.ts        # 前端 Force Graph 渲染与 SSE 客户端逻辑
├── telemetry/           # PostHog 匿名遥测与错误指纹分析 (包含 build_channel 标记)
├── okf/                 # Open Knowledge Format 标准解析、Frontmatter 与 Index 标号
└── mermaid/             # Mermaid 流程图提取、校验与容错修复
```

---

## 3. 大模型 Agent 执行引擎 (Agent Workflow & Runtime)

OpenWiki 的核心竞争力在于其深度定制的 Agent 执行逻辑：

### 3.1 限制性文件后端 (`OpenWikiLocalShellBackend`)
- Agent 通过 `LocalShellBackend` 操作本地文件，基于虚拟路径 (如 `/README.md`) 映射。
- **写边界保护**：Agent 被严格限制为**仅允许写入 `openwiki/` 目录**（除了通过专门注册的虚拟挂载点操作外），防止误修改源代码库。
- **隔离挂载**：挂载 `/skills/`（技能扩展）与 `/conversation_history/`（会话历史分流），避免触发写边界拦截。

### 3.2 增量更新与 Snapshot 检视
- 当执行 `openwiki --update` 时，Agent 会自动执行 `git log <lastHead>..HEAD` 提取最近更改的文件。
- 结束时在 `src/agent/utils.ts` 中计算 `openwiki/` 目录下所有 Markdown 的 SHA-256 哈希总和。**若无内容变更，则不更新 `.last-update.json`**，有效避免无意义的 CI/CD Git 提交循环。

### 3.3 OKF 规范与修剪中间件 (`okf-middleware.ts`)
- **Frontmatter 自动补全**：每篇文档均包含 `type`, `title`, `description`, `tags` 等元数据。
- **Mermaid 自动修复**：运行后遍历提取全库 Mermaid 流程图，若格式解析失败则自动转化为平铺文本块并附带调试注释，避免破坏渲染。
- **死链扫描**：`wiki-link-validator.ts` 会全库扫描内部链接与锚点，失效链接使用 `<!-- openwiki: broken link -->` 内联标记以便后续修复。

---

## 4. 多模型 Provider 适配体系 (Model Providers)

`src/config/constants.ts` 集中管理所有支持的 Model Provider，主要划分为 4 类认证模式：

1. **`api-key` 模式**：OpenAI, Anthropic, OpenRouter, Gemini, Baseten, Fireworks, Nebius, NVIDIA NIM, OpenAI-Compatible。支持自定义 Base URL (例如 `OPENAI_COMPATIBLE_BASE_URL`)。
2. **`oauth` 模式**：OpenAI-ChatGPT (浏览器登录并自动刷新 Token)。
3. **`aws-sdk` 模式**：AWS Bedrock (使用 AWS SDK 凭据链、IAM Role 或 AccessKey/SecretKey)。
4. **`external-cli` 模式**：GitHub Copilot (利用 `gh auth token` 动态获取临时 API Key)。
5. **Keyless 模式**：Gemini Enterprise (Vertex AI，通过 Google Application Default Credentials / ADC 认证)。

---

## 5. 内部私有无外网环境 (Air-Gapped Private Network) 改造要点

在隔离的企业内网部署 OpenWiki 时，需特别注意以下 4 个层面的改造：

### 5.1 大模型网关重定向 (Private LLM Gateway)
- **配置方式**：设置 `OPENWIKI_PROVIDER=openai-compatible`，配合 `OPENAI_COMPATIBLE_BASE_URL=http://your-internal-gateway/v1` 及 `OPENAI_COMPATIBLE_API_KEY`。
- **模型匹配**：通过 `OPENWIKI_MODEL_ID` 指定私有网关上的模型（如 `deepseek-r1`, `deepseek-v3`, `qwen2.5-coder-32b`）。
- **CA 证书支持**：若企业内部网关采用自签名 SSL 证书，运行环境需指定 `NODE_EXTRA_CA_CERTS=/path/to/internal-ca.crt`。

### 5.2 遥测彻底关闭 (Disable Telemetry)
- 在无外网环境下，PostHog 遥测请求 (`us.i.posthog.com`) 会超时。
- 环境变量必须设置 `OPENWIKI_TELEMETRY_DISABLED=1` 或 `DO_NOT_TRACK=1`，促使 `src/telemetry/gates.ts` 直接跳过上报。

### 5.3 可视化图谱 CDN 本地化 (Visualizer Local Assets)
- `src/visualize/page.ts` 原本依赖 `cdn.jsdelivr.net` 加载 `force-graph`, `marked`, `dompurify`, `mermaid`。
- 离线改造需在 `src/visualize/server.ts` 增加 `/vendor/*` 静态资源响应路由，并将 4 个 JS 依赖库部署在本地服务器上，同时去掉 Google Fonts 外链。

### 5.4 离线单文件二进制打包 (Standalone Binary Executable)
- 放弃 Docker 与 Node.js 宿主依赖，采用交叉编译将 OpenWiki CLI 及其全量依赖（包含 React/Ink 终端渲染引擎、OKF 校验中间件、SQLite 数据库引擎）整合成独立单一二进制可执行文件：
  - Linux / macOS 架构：`dist-bin/openwiki-bin`
  - Windows x64 架构：`dist-bin/openwiki.exe` (使用说明参考 [USER_MANUAL_WIN.md](file:///Users/bigc/openwiki/docs/USER_MANUAL_WIN.md))
- 部署至隔离的目标私有服务器时，无需安装 Node.js、npm 或 Docker 容器环境，赋予执行权限后直接裸机一键运行。

---

## 6. 开发者与 AI 模型操作规范 (Guidelines for AI Assistants)

未来 AI 模型或 Agent 在本仓库进行开发修补时，必须严格遵守以下规则：

1. **先读规范文档**：修改前首选阅读 `openwiki/quickstart.md` 与 `openwiki/architecture/overview.md`。
2. **严禁修改生成文档**：`openwiki/` 目录下的文档是 OpenWiki 自身运行产生的成果物，除非用户明确要求修改生成规则，否则不得手动更改生成的 Markdown。
3. **模型 Provider 修改强一致**：添加或修改 Model Provider 时，必须同步修改：
   - `src/config/constants.ts` (`PROVIDER_CONFIGS`, `OpenWikiProvider` 类型, `SELECTABLE_OPENWIKI_PROVIDERS`)
   - `src/agent/index.ts` 中的 `createModel` 分支
   - `src/env.ts` 中的 `managedEnvKeys`
4. **编译与验证指令**：
   - 类型检查：`pnpm run typecheck`
   - 工程构建：`pnpm run build`
   - 测试套件：`pnpm run test` / `pnpm run coverage`
