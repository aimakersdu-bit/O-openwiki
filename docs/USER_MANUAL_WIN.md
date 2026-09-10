# OpenWiki Windows 绿色单文件版用户使用手册

本文档为 **OpenWiki Windows 纯净绿色单文件版 (`openwiki.exe`)** 的完整使用与部署指南。

该版本采用独立的 C/C++ & Native 机器码交叉编译（Windows x64），**无需在 Windows 目标机器上安装 Node.js、Python、Git 依赖环境或 Docker 容器**。解压/拷贝后即可在完全隔离的私有无外网环境中直接独立运行。

---

## 1. 软件信息与产物路径

- **Windows 可执行文件路径**：`dist-bin/openwiki.exe`
- **文件体积**：约 119 MB (包含整体 Runtime、React Ink 渲染引擎、OKF 校验模块及内置 SQLite 数据库)
- **适用系统**：Windows 10 / Windows 11 / Windows Server 2016+ (64位)

---

## 2. 安装与环境变量配置

### 步骤一：放置与运行位置
建议在 Windows 系统中将 `openwiki.exe` 放置在专用目录（例如 `C:\OpenWiki\`），或者直接拷贝到您的**工程代码仓库根目录**下运行。

> [!NOTE]
> **本地持久化说明**：所有配置文件（`.env`）、SQLite 数据库（`openwiki.sqlite`）、预置技能与对话历史均会自动保存在**当前运行命令的代码仓库根目录**下（`.\.openwiki\`），隔离不同工程的数据，不依赖用户主目录或全局路径。

### 步骤二：添加系统 PATH 环境变量（可选，建议配置）
添加 PATH 后可以在任意项目代码仓库的 CMD 或 PowerShell 终端中直接敲 `openwiki` 命令：

1. 按 `Win + R` 输入 `sysdm.cpl` 打开“系统属性”。
2. 切换到“高级”标签页，点击“环境变量”。
3. 在“系统变量”列表中找到 `Path`，点击“编辑”。
4. 新增一条记录：`C:\OpenWiki\`
5. 保存退出，并重新打开新的 CMD 或 PowerShell 窗口。

---

## 3. 内部私有网络 (Air-Gapped) 环境配置

在完全隔离无外网的内网环境中部署时，OpenWiki 支持对接企业自建的大模型网关（如 OneAPI / vLLM / FastGPT / Ollama / DeepSeek 私有部署）。

### 环境变量配置说明

在 Windows 终端中设置以下环境变量：

| 环境变量 | 推荐值示例 | 说明 |
| :--- | :--- | :--- |
| `OPENWIKI_PROVIDER` | `openai-compatible` | 指定使用兼容 OpenAI 协议的私有网关 |
| `OPENAI_COMPATIBLE_BASE_URL` | `http://192.168.1.100:8000/v1` | 内网大模型 Gateway 服务的 API 地址 |
| `OPENAI_COMPATIBLE_API_KEY` | `sk-custom-private-key` | 私有网关认证密钥（无认证可填任意非空字符串） |
| `OPENWIKI_MODEL_ID` | `deepseek-r1` / `qwen2.5-coder` | 目标大模型的 ID |
| `OPENWIKI_TELEMETRY_DISABLED`| `1` | 彻底切断与关闭外部 PostHog 遥测上报 |
| `LANGSMITH_TRACING_DISABLED` | `1` | 强制关闭 LangSmith 链路追踪 (离线/无外网环境推荐) |
| `OPENWIKI_OFFLINE` | `1` | 开启纯净离线防护模式 (自动跳过遥测与 LangSmith 追踪) |

### 环境变量配置方法

#### 方法 A：PowerShell 终端临时设置
```powershell
$env:OPENWIKI_PROVIDER="openai-compatible"
$env:OPENAI_COMPATIBLE_BASE_URL="http://10.0.0.88:8000/v1"
$env:OPENAI_COMPATIBLE_API_KEY="sk-private-token"
$env:OPENWIKI_MODEL_ID="deepseek-r1"
$env:OPENWIKI_TELEMETRY_DISABLED="1"
$env:OPENWIKI_OFFLINE="1"
```

#### 方法 B：PowerShell 脚本一键启动模板 (`start-openwiki.ps1`)
您可以创建 `start-openwiki.ps1` 脚本置于项目目录下：
```powershell
# set-env.ps1
$env:OPENWIKI_PROVIDER="openai-compatible"
$env:OPENAI_COMPATIBLE_BASE_URL="http://10.0.0.88:8000/v1"
$env:OPENAI_COMPATIBLE_API_KEY="sk-private-token"
$env:OPENWIKI_MODEL_ID="deepseek-r1"
$env:OPENWIKI_TELEMETRY_DISABLED="1"
$env:OPENWIKI_OFFLINE="1"

# 启动 OpenWiki 交互终端
openwiki.exe
```

#### 方法 C：批处理一键启动模板 (`start-openwiki.bat`)
```bat
@echo off
set OPENWIKI_PROVIDER=openai-compatible
set OPENAI_COMPATIBLE_BASE_URL=http://10.0.0.88:8000/v1
set OPENAI_COMPATIBLE_API_KEY=sk-private-token
set OPENWIKI_MODEL_ID=deepseek-r1
set OPENWIKI_TELEMETRY_DISABLED=1
set OPENWIKI_OFFLINE=1

openwiki.exe %*
```

---

## 4. 常用命令与使用场景

在任意代码工程根目录下运行以下命令：

### 1. 全量初始化生成文档 (`--init`)
```powershell
openwiki.exe --init
```
分析当前代码仓库，自动在工程根目录下生成标准的 `openwiki/` 结构化知识库文档。

### 2. 增量更新文档 (`--update`)
```powershell
openwiki.exe --update
```
结合 Git 提交历史与最新代码变更，自动更新增量修改的文档部分。

### 3. 终端 TUI 交互对话模式
```powershell
openwiki.exe
```
打开 React Ink 交互式终端聊天界面，可以随时提问代码库结构或要求调整文档。

### 4. 命令行单次提问 (`-p` / `--print`)
```powershell
openwiki.exe -p "请概述本仓库的核心入口模块与主要功能"
```

### 5. 启动 3D 本地离线知识图谱 (`visualize`)
```powershell
openwiki.exe visualize
```
启动本地 Web 服务器，自动打开浏览器访问 `http://127.0.0.1:4321`，展示 3D 节点拓扑关系与 Markdown 渲染。
*(所有 JS 资源均内置于本地服务 `/vendor/*` 中，无需公网 CDN 支持)*。

---

## 5. 常见问题排查 (FAQ)

### Q1：Windows 终端乱码或显示字符异常？
**解答**：建议使用 Windows Terminal，或在 PowerShell / CMD 中先将代码页设置为 UTF-8：
```cmd
chcp 65001
```

### Q2：内网网关使用了自签名 SSL 证书 (HTTPS) 导致报错？
**解答**：可以设置环境变量指定自签名 CA 证书路径：
```powershell
$env:NODE_EXTRA_CA_CERTS="C:\Certs\internal-ca.crt"
```

### Q3：`visualize` 图谱服务启动提示防火墙弹窗？
**解答**：`openwiki.exe visualize` 仅绑定在本地回环地址 `127.0.0.1`（Loopback Only），绝不会暴露到公网，提示防火墙时勾选“允许专用网络”即可。

---

## 6. 构建与维护指令（针对开发维护人员）

如需在 MacOS / Linux 上重新交叉编译 Windows 版本的 `openwiki.exe`，执行以下指令：

```bash
# 重新编译 Windows x64 二进制文件
pnpm run build:win

# 编译结果文件
# dist-bin/openwiki.exe
```
