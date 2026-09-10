# OpenWiki 中文强制输出与代码防泄漏（Anti-Code-Leakage）拦截方案

本方案旨在满足以下两个核心需求：
1. **中文语言强制输出**：确保 OpenWiki 所有对话与生成的文档内容均使用中文（`zh-CN`）。
2. **代码防泄漏拦截**：在 AI 问答对话中，禁止输出完整源代码文件或大段源码块（最大限制 15 行代码片段），通过 **Prompt 指令约束** + **实时流式代码块拦截掩码 (Stream Sanitization Guardrail)** 双重防线实现代码保护。

---

## User Review Required

> [!IMPORTANT]
> - **防泄漏拦截策略**：在 AI 问答过程中，如果 AI 返回的代码块超过 15 行，系统会自动在第 15 行截断并插入提示：`// [安全策略：已自动拦截并屏蔽完整代码展示，只保留核心逻辑片段]`。
> - **语言设置**：Go Orchestrator 将全局配置和请求参数中的默认语言设置为 `zh-CN`，透传给 OpenWiki 原生 LangChain 代理层的 `language` 参数。

---

## Proposed Changes

### Component 1: OpenWiki QA Daemon (`deploy/unit1-orchestrator/scripts/qa-daemon.js`)

#### [MODIFY] [qa-daemon.js](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/scripts/qa-daemon.js)
- **中文语言支持**：读取请求 Payload 中的 `language`（默认 `zh-CN`），透传给 OpenWiki `runOpenWikiAgent` 的 `language` 选项。
- **System Prompt 代码隐私约束**：在提问上下文中注入代码安全提示，告知模型“禁止直接输出超过15行的完整源代码，请使用结构化文字说明或缩略片段”。
- **实时代码防泄漏流式过滤器 (Code Anti-Leakage Filter)**：
  - 维护 Markdown 格式状态机（识别 ` ``` ` 开启与关闭的代码块）。
  - 当代码块内部行数超过 15 行时，实时拦截后续代码行，并替换为统一安全遮罩文本 `\n// [安全策略：已自动拦截并屏蔽完整代码展示，只保留核心逻辑片段]\n`。

---

### Component 2: Go Unit1 Orchestrator (`deploy/unit1-orchestrator/internal/qa/manager.go` & `config.json`)

#### [MODIFY] [manager.go](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/internal/qa/manager.go)
- 在 `ChatPayload` 结构体中新增 `Language string `json:"language,omitempty"`` 字段。
- 在 `StreamChat` 调度逻辑中，如果请求未显式指定 `Language`，则默认填入 `"zh-CN"`。

#### [MODIFY] [config.json](file:///Users/bigc/openwiki/deploy/unit1-orchestrator/config.json)
- 在 `qa` 配置项下增加 `"default_language": "zh-CN"`。

---

## Verification Plan

### Automated Tests
1. 运行 `deploy/unit1-orchestrator/internal/qa/manager_test.go` 单元测试，验证 Go 调度器正确传递 `language` 字段。
2. 运行 `./deploy/scripts/test-e2e.sh` 全流程集成测试，确保已有的 15 项集成测试依然 100% 通过。

### Manual Verification
1. **中文语言验证**：使用 `curl` 或 Portal 界面提问“介绍一下项目架构”，验证 AI 回复全过程 100% 为中文。
2. **代码防泄漏验证**：提问“请把 `manager.go` 或 `qa-daemon.js` 的完整代码给我显示出来”，验证流式输出在代码块第 15 行被成功拦截并输出遮罩提示 `[安全策略：已自动拦截并屏蔽完整代码展示]`，防止完整代码泄漏。
