# OpenWiki 内网打包、Docker 镜像构建与部署运维手册

本手册专为**内网打包人员及运维人员**编写，详细说明 OpenWiki 发布包的目录结构、内网 Docker 镜像构建（支持 ARM64 与 x86_64）以及容器化部署运行的操作流程。

---

## 一、发布包整体说明与文件清单

部署包采用 **All-in-One 架构**，集成了 Go 语言编写的后台调度服务（Orchestrator）、Web 门户服务（Portal）、Nginx 反向代理服务以及日志轮转与健康检查机制。

解压打包文件 `openwiki-deploy-package.tar.gz` 后，目录结构如下：

```
deploy/
├── Dockerfile                  # 生产级 All-in-One Dockerfile 镜像构建脚本
├── docker-compose.yml          # Docker Compose 快速单机部署配置
├── nginx.conf                  # Nginx 80 端口反向代理配置文件
├── logrotate.conf              # 日志切割轮转配置文件
├── entrypoint.sh               # 容器主进程启动控制脚本 (支持自动启动 Nginx, Orchestrator, Portal, Cron)
├── healthcheck.sh              # 容器健康与就绪检查探针脚本
├── MANUAL.md                   # 本操作手册与内网 Build 指南
└── bin/                        # 二进制与依赖资源目录
    ├── arm/                    # ARM64 (aarch64) 架构发布包资源
    │   ├── orchestrator        # Orchestrator 后端 Go 二进制服务 (ARM64)
    │   ├── portal              # Portal Web 前端 Go 二进制服务 (ARM64)
    │   ├── config.json         # Orchestrator 服务配置文件
    │   ├── config.yaml         # Portal 服务配置文件
    │   ├── scripts/            # 辅助后台脚本 (qa-daemon.js 等)
    │   ├── public/             # Portal 网页静态资源与 UI 组件
    │   └── assets/             # 离线静态 Vendor 依赖库
    └── x86/                    # x86_64 (amd64) 架构发布包资源
        ├── orchestrator        # Orchestrator 后端 Go 二进制服务 (x86_64)
        ├── portal              # Portal Web 前端 Go 二进制服务 (x86_64)
        ├── config.json         # Orchestrator 服务配置文件
        ├── config.yaml         # Portal 服务配置文件
        ├── scripts/            # 辅助后台脚本
        ├── public/             # Portal 网页静态资源
        └── assets/             # 离线静态 Vendor 依赖库
```

---

## 二、内网 Docker 镜像构建指南 (Dockerfile Build 手册)

### 1. 构建前准备与内网适配说明

在无外网连接的局域网/隔离内网环境中，需根据实际内网基础设施调整 `Dockerfile` 中的两处网络依赖：

#### 适配点 A：基础镜像 (Base Image)
原 `Dockerfile` 第 7 行：
```dockerfile
FROM node:22-bookworm-slim
```
* **内网替换方案**：请替换为内网私有镜像仓库（如 Harbor）中的 Node.js 基础镜像地址，例如：
```dockerfile
FROM your-internal-harbor.company.com/base/node:22-bookworm-slim
```

#### 适配点 B：NPM 包安装 (npm install -g openwiki)
原 `Dockerfile` 第 25 行：
```dockerfile
RUN npm install -g openwiki
```
* **内网私有源方案**：若内网有私有 npm 镜像源（如 Nexus / Verdaccio），加入 `--registry` 参数：
```dockerfile
RUN npm install -g openwiki --registry=http://your-internal-npm.company.com/repository/npm-group/
```
* **离线 `.tgz` 包方案**：若完全无 npm 源，可将预先下载好的 `openwiki-0.3.1.tgz` 放入打包目录，并在 Dockerfile 中调整为：
```dockerfile
COPY openwiki-0.3.1.tgz /app/openwiki-0.3.1.tgz
RUN npm install -g /app/openwiki-0.3.1.tgz && rm -f /app/openwiki-0.3.1.tgz
```

#### 适配点 C：操作系统 Apt 软件源 (如果内网无法直连 debian 官方源)
若构建节点无法连接外网更新 apt，可切换为内网 Apt 镜像源，或使用已打好 `nginx`, `git`, `sqlite3`, `logrotate`, `procps`, `cron` 依赖的基础镜像。

---

### 2. 执行 Docker 镜像构建命令

进入打包根目录（包含 `Dockerfile` 的层级）：

#### A. 构建 ARM64 架构镜像（默认）
```bash
docker build -t openwiki:arm64 -f Dockerfile .
```
或显式指定架构参数：
```bash
docker build --build-arg TARGETARCH=arm -t openwiki:arm64 -f Dockerfile .
```

#### B. 构建 x86_64 (amd64) 架构镜像
```bash
docker build --build-arg TARGETARCH=x86 -t openwiki:x86_64 -f Dockerfile .
```

#### C. 打包导出镜像 (供内网分发)
```bash
docker save openwiki:arm64 | gzip > openwiki-image-arm64.tar.gz
```

---

## 三、部署与运维操作手册 (Operation Manual)

### 1. 环境依赖要求
- **容器运行时**：Docker Engine 20.10+ / Containerd / Podman
- **编排工具**：Docker Compose v2+ (可选)
- **操作系统**：Linux ARM64 (如 飞腾/鲲鹏/Kylin/UOS) 或 Linux x86_64 (CentOS/Ubuntu/RHEL)

---

### 2. 容器部署与运行方式

#### 方式一：使用 Docker Direct 命令运行

创建宿主机持久化目录：
```bash
mkdir -p /data/openwiki/{repos,static,db,logs}
```

启动容器：
```bash
docker run -d \
  --name openwiki \
  --restart always \
  -p 80:80 \
  -p 8080:8080 \
  -p 3000:3000 \
  -v /data/openwiki:/data/openwiki \
  openwiki:arm64
```

---

#### 方式二：使用 Docker Compose 快速编排部署

打包目录中已包含 `docker-compose.yml` 示例配置：

```yaml
version: '3.8'

services:
  openwiki:
    image: openwiki:arm64
    container_name: openwiki
    restart: always
    ports:
      - "80:80"        # Nginx 网关入口 (推荐主访问入口)
      - "8080:8080"    # Portal Web 门户服务
      - "3000:3000"    # Orchestrator 调度服务
    volumes:
      - /data/openwiki:/data/openwiki  # 持久化数据与日志挂载点
    healthcheck:
      test: ["CMD", "/app/healthcheck.sh"]
      interval: 15s
      timeout: 5s
      retries: 3
      start_period: 10s
```

运行启动命令：
```bash
docker compose up -d
```

---

### 3. Git SSH 方式下载与秘钥挂载配置

OpenWiki 后端调度器已原生支持 SSH 协议的 Git 仓库下载（如 `git@github.com:org/repo.git` 或 `git@gitlab.company.com:group/repo.git`）。服务内部已配置非交互式免密交互探针 `GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new`。

#### 挂载私钥方式（推荐）：
只需在启动容器时将宿主机包含 Git 权限的 SSH 秘钥目录挂载到容器的 `/root/.ssh`（推荐只读 `:ro`）：

- **Docker Direct 运行**：
  ```bash
  docker run -d \
    --name openwiki \
    --restart always \
    -p 80:80 -p 8080:8080 -p 3000:3000 \
    -v /data/openwiki:/data/openwiki \
    -v ~/.ssh:/root/.ssh:ro \
    openwiki:arm64
  ```

- **Docker Compose 运行**：
  在 `docker-compose.yml` 的 `volumes` 节点下配置：
  ```yaml
      volumes:
        - /data/openwiki:/data/openwiki
        - ~/.ssh:/root/.ssh:ro
  ```

---

### 4. Git HTTP 方式账号密码 / Token 统一配置

对于使用 HTTP/HTTPS 协议的私有仓库（如 `https://gitlab.company.com/group/project.git`），可通过以下三种方式配置鉴权凭据：

#### 方式 A：单仓库 URL 内嵌凭据（最简便、按仓库配置）
在 Web 门户注册仓库时，直接在 Git URL 中带上账号密码或个人访问令牌 (Personal Access Token)：
- **密码方式**：`https://<username>:<password>@gitlab.company.com/group/project.git`
- **Token 方式**：`https://oauth2:<your_token>@gitlab.company.com/group/project.git`
- **GitHub Token**：`https://token:<your_token>@github.com/org/repo.git`

#### 方式 B：统一挂载全局 Git Credentials 凭据文件（企业级统一配置）
若希望全局免去在每一个仓库 URL 中暴露明文密码，可以在宿主机准备全局 Git 凭据文件并挂载到容器中：

1. **在宿主机（或部署节点）创建 `git-credentials` 文件**（例如位于 `/data/openwiki/.git-credentials`）：
   ```text
   https://your_username:your_password_or_token@gitlab.company.com
   https://your_username:your_password_or_token@github.com
   ```

2. **在宿主机创建匹配的 `.gitconfig` 文件**（例如位于 `/data/openwiki/.gitconfig`）：
   ```ini
   [credential]
       helper = store --file=/root/.git-credentials
   ```

3. **在容器启动时将凭据挂载到容器**：
   - **Docker Direct 运行**：
     ```bash
     docker run -d \
       --name openwiki \
       -v /data/openwiki:/data/openwiki \
       -v /data/openwiki/.git-credentials:/root/.git-credentials:ro \
       -v /data/openwiki/.gitconfig:/root/.gitconfig:ro \
       openwiki:arm64
     ```
   - **Docker Compose 运行**：
     ```yaml
         volumes:
           - ./data:/data/openwiki
           - ./data/.git-credentials:/root/.git-credentials:ro
           - ./data/.gitconfig:/root/.gitconfig:ro
     ```

---

### 3. 服务端口与数据持久化说明

#### 对外服务端口映射
| 端口号 | 服务名称 | 描述 | 访问方式 |
| :--- | :--- | :--- | :--- |
| **80** | Nginx 网关 | **推荐入口**，反向代理 Portal 及静态文档资源 | `http://<服务器IP>/` |
| **8080** | Portal 服务 | OpenWiki 独立 Web 门户界面 | `http://<服务器IP>:8080/` |
| **3000** | Orchestrator | OpenWiki 后台任务与 QA 调度服务 API | `http://<服务器IP>:3000/` |

#### 数据持久化挂载点 (`/data/openwiki`)
容器内部数据与日志均统一挂载在宿主机的 `/data/openwiki` 目录下：
- `/data/openwiki/db/`：SQLite 数据库文件（`orchestrator.db`, `portal.db`）
- `/data/openwiki/repos/`：Git 仓库代码缓存与工作空间
- `/data/openwiki/static/`：生成的 Wiki 静态 HTML / Markdown 产物目录
- `/data/openwiki/logs/`：系统服务运行日志（`nginx/`, `orchestrator/`, `portal/`）

---

### 4. 维护、日志查看与健康状态检查

#### 检查容器运行状态与健康探针
```bash
docker ps -f name=openwiki
```
若状态显示为 `(healthy)`，说明反向代理、Orchestrator 以及 Portal 三大组件均运行正常。

#### 查看实时运行日志
```bash
# 查看全局容器日志
docker logs -f openwiki

# 查看独立服务日志（宿主机持久化目录）
tail -f /data/openwiki/logs/orchestrator/orchestrator.log
tail -f /data/openwiki/logs/portal/portal.log
tail -f /data/openwiki/logs/nginx/error.log
```

#### 手动执行探针测试
```bash
docker exec -it openwiki /app/healthcheck.sh
```

---

## 四、常见问题与故障排查 (Troubleshooting)

1. **80 / 8080 端口冲突**：
   - 若宿主机已占用 80 端口，可在 `docker run` 时修改宿主机端口映射，如 `-p 8000:80`。
2. **数据库文件读写权限**：
   - 确保宿主机的 `/data/openwiki` 挂载目录具备可读写权限（默认容器运行在 root 权限下）。
3. **内网镜像构建时 apt 更新卡住**：
   - 请在 Dockerfile 中注释 `apt-get update` 或配置内网 Apt 源。
