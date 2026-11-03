# OpenWiki 实施计划与架构演进归档 (Implementation Plans)

本目录维护 OpenWiki 的各项技术方案实施计划（Implementation Plans）及历史演进版本。

---

## 目录索引

### 1. 全行 633 仓基线批跑、多盘存储与组长权限体系（重点演进方案）

当前生效的最新实施计划：
👉 **[2026-10-29-openwiki-batch-scheduler.md](2026-10-29-openwiki-batch-scheduler.md)**（当前生效，V8 终审定稿版）

完整版本演进链条（共 8 个版本）：

| 版本 | 文件名 | 核心定位与设计重点 | 关键决策与变更说明 |
| :--- | :--- | :--- | :--- |
| **V1** | [2026-10-29-openwiki-batch-scheduler-v1.md](2026-10-29-openwiki-batch-scheduler-v1.md) | 初始草案 | 全行 500+ 代码库基线批量导入、Token 漏桶限流、夜间时间窗口控制与无变更跳过 |
| **V2** | [2026-10-29-openwiki-batch-scheduler-v2.md](2026-10-29-openwiki-batch-scheduler-v2.md) | 动态并发与 ETA 版 | 引入运行时并发动态热调整、实时进度与高精度 ETA 预估、飞书多维表格报表同步 |
| **V3** | [2026-10-29-openwiki-batch-scheduler-v3.md](2026-10-29-openwiki-batch-scheduler-v3.md) | 生产级加固版 | 统一单仓构建流水线 `ExecuteBuildPipeline`（消除 Cron/批跑并发冲突）、真实 Token 采集（方案 A）、基于条件变量 `sync.Cond` 的 `DynamicRateLimiter` 与 `SlotPool` |
| **V4** | [2026-11-03-openwiki-batch-scheduler-v4.md](2026-11-03-openwiki-batch-scheduler-v4.md) | 633 仓多盘硬挂载版 | 四级算力防打穿熔断体系、全行 633 仓存储量化测算（$\ge 1.2\text{TB} \sim 1.5\text{TB}$）、多磁盘硬挂载分片与跨盘虚拟 Monorepo 软链聚合、公有云主备虚拟机云盘自动漂移 HA 架构 |
| **V5** | [2026-11-03-openwiki-batch-scheduler-v5.md](2026-11-03-openwiki-batch-scheduler-v5.md) | 纯 deploy 零侵入版 | 确立“100% 仅改 `deploy/`，OpenWiki 源码零侵入”准则；回滚 `src/` 与 `test/`；引入独立 TPM 令牌桶限流；规划 633 仓实时静态页面落盘 `disk0/static/` 物理读写隔离 |
| **V6** | [2026-11-03-openwiki-batch-scheduler-v6.md](2026-11-03-openwiki-batch-scheduler-v6.md) | 独立日志盘版 | 将日志从 `disk0` 剥离，挂载专用独立日志盘 `disk_log`（100~200GB），彻底阻断日志打满冲垮 SQLite 核心库与 Web 页面的级联故障；配合 `deploy/logrotate.conf` 实现零风险清理与自动滚存 |
| **V7** | [2026-11-03-openwiki-batch-scheduler-v7.md](2026-11-03-openwiki-batch-scheduler-v7.md) | 私有云修正版 | 纠正公有云 API 漂移假设，改为私有云本地硬挂载方案；放弃令牌环/一致性哈希，确立“持久化映射 + 容量与负载感知加权最少负载放置算法”；设计单仓 7 态在线迁移状态机与逻辑路径软链间接层；设计 RBAC 组长业务域授权 |
| **V8** | [2026-11-03-openwiki-batch-scheduler-v8.md](2026-11-03-openwiki-batch-scheduler-v8.md) | 终审定稿版 | 确认私有云单机多盘硬挂载且无共享存储，本期去除双机 HA/DRBD 复杂度；确认行内私有化 OpenAI 协议，在后管提供 TPM/RPM 动态配置项与 429 自适应退避；组长按 `business_domain` 授权管理虚拟 Monorepo；SQLite `WAL + synchronous=FULL` 单机高可靠保障 |

---

### 2. 其它专项计划

- [2026-10-09-mcp-true-streaming.md](2026-10-09-mcp-true-streaming.md)：MCP 协议真正流式响应改造计划
- [2026-10-09-portal-qa-mermaid-links-admin-pagination.md](2026-10-09-portal-qa-mermaid-links-admin-pagination.md)：Portal 问答 Mermaid 流程图链接与后台分页实施计划
- [2026-10-16-virtual-project-workspace-qa.md](2026-10-16-virtual-project-workspace-qa.md)：虚拟多仓项目工作区与项目级 QA 问答实施计划
