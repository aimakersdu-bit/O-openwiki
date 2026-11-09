# OpenWiki ARM64 Docker 镜像新机安装与数据迁移验证手册

本文档指导运维人员如何将已构建好的 **OpenWiki ARM64 Docker 镜像** 部署到一台新的 ARM64（aarch64）服务器上进行功能验证，并完成从现有 ECS 服务器到新机器的**完整数据迁移**。

---

## 目录
- [一、准备工作与资产获取](#一准备工作与资产获取)
- [二、新机器：镜像安装与纯净环境验证](#二新机器镜像安装与纯净环境验证)
- [三、数据迁移：从旧服务器导出并导入新机器](#三数据迁移从旧服务器导出并导入新机器)
- [四、配置微调（大模型、Git 凭据与端口）](#四配置微调大模型git-凭据与端口)
- [五、验证清单与常见问题排查](#五验证清单与常见问题排查)

---

## 一、准备工作与资产获取

### 1. 核心资产清单
在当前 ECS 服务器（`i-0jle27a6ly22jmi67ybd`）上已就绪的文件：

| 资产类型 | 服务器源路径 | 大小 | 说明 |
| :--- | :--- | :--- | :--- |
| **ARM64 Docker 镜像** | `/tmp/openwiki-arm64-image.tar.gz` | 223 MB | 预编译 All-in-One 镜像（Node 22 + Nginx + OpenWiki） |
| **HTTP 直链下载** | `http://<ECS_IP>:8891/portal/openwiki-arm64-image.tar.gz` | 223 MB | 局域网/内网可直接通过浏览器或 `curl` 下载 |

### 2. 下载镜像包至新机器
在新机器的终端执行：
```bash
# 方式 1：若能直连当前 ECS IP
curl -o /tmp/openwiki-arm64-image.tar.gz http://<ECS_IP>:8891/portal/openwiki-arm64-image.tar.gz

# 方式 2：中转下载后通过 scp 传输
scp user@<ECS_IP>:/tmp/openwiki-arm64-image.tar.gz /tmp/
```

---

## 二、新机器：镜像安装与纯净环境验证

### 1. 加载 Docker 镜像
登录新机器（必须为 Linux ARM64 系统，且已安装 Docker）：
```bash
docker load -i /tmp/openwiki-arm64-image.tar.gz
```
加载完成后检查镜像：
```bash
docker images | grep openwiki
# 预期输出：
# openwiki   arm64     a29a8d62b50a   ...   789MB
# openwiki   latest    a29a8d62b50a   ...   789MB
```

### 2. 创建宿主机持久化目录
```bash
mkdir -p /data/openwiki/repos \
         /data/openwiki/static \
         /data/openwiki/db \
         /data/openwiki/logs
```

### 3. 一键启动容器
```bash
docker run -d \
  --name openwiki \
  --restart always \
  -p 80:80 \
  -v /data/openwiki:/data/openwiki \
  openwiki:arm64
```
> [!NOTE]
> 若新机器的 80 端口已被其他服务占用，可映射为自定义端口（例如 `-p 8891:80`）。

### 4. 纯净环境验证（验证镜像本身）
1. **检查容器状态：**
   ```bash
   docker ps -f name=openwiki
   ```
   等待 10~15 秒，STATUS 字段应显示为 **`Up ... (healthy)`**。
2. **在宿主机手动执行健康探针：**
   ```bash
   docker exec -it openwiki /app/healthcheck.sh
   # 应返回 exit code 0，无任何报错
   ```
3. **浏览器访问 Portal 控制台：**
   打开浏览器访问：`http://<新机器IP>/portal/`（若改了端口则为 `http://<新机器IP>:8891/portal/`）
   - 应能正常看到登录界面或 Portal 仪表盘。
   - 默认初始管理员账号：`admin` / `admin123`（初次启动由系统自动初始化）。

---

## 三、数据迁移：从旧服务器导出并导入新机器

在确认新机器上镜像能正常拉起后，接下来将旧环境的**用户、权限、代码仓库与已生成的 Wiki 站点**完整迁移过去。

### 步骤 1：在旧服务器（ECS）上导出数据

> [!IMPORTANT]
> 为避免 SQLite WAL 日志处于写入状态导致的数据不一致，建议先停止旧服务，或者使用内置的 SQLite 检查点安全打包。

在旧服务器上执行：
```bash
# 1. 临时停止服务（确保数据库文件落盘）
systemctl stop openwiki-orchestrator openwiki-portal 2>/dev/null || pkill -f orchestrator; pkill -f portal || true

# 2. 打包持久化数据目录（包含数据库、仓库及已编译静态 Wiki）
cd /data
tar -czvf /tmp/openwiki-data-backup.tar.gz \
  --exclude="*.log" \
  --exclude="openwiki/logs" \
  --exclude="openwiki/qa/*.sock" \
  openwiki/

# 3. 检查备份包大小与完整性
ls -lh /tmp/openwiki-data-backup.tar.gz
```

### 步骤 2：将备份包传输至新机器
```bash
# 在新机器上拉取备份包：
scp root@<ECS_IP>:/tmp/openwiki-data-backup.tar.gz /tmp/
```

### 步骤 3：在新机器上停止容器并恢复数据
```bash
# 1. 停止新机器上正在运行的测试容器
docker stop openwiki

# 2. 解压覆盖到宿主机数据目录
cd /data
tar -xzvf /tmp/openwiki-data-backup.tar.gz -C /data/

# 3. 统一文件归属与权限
chown -R root:root /data/openwiki
chmod -R 755 /data/openwiki

# 4. 重新启动 OpenWiki 容器
docker start openwiki
```

### 步骤 4：迁移后数据校验
1. 刷新浏览器访问 `http://<新机器IP>/portal/`：
   - 登录凭据应与旧服务器完全一致。
   - 检查已绑定的代码仓库列表是否存在。
2. 访问静态文档页面：
   - 访问 `http://<新机器IP>/wiki/<repo_id>/`，图谱与 Markdown 文档应完整展示。
3. 问答对话功能（QA）：
   - 进入某个仓库的 Wiki 页面，发送一条问答测试，验证后台 QA-Daemon 能正常流式生成答案。

---

## 四、配置微调（大模型、Git 凭据与端口）

### 1. 配置大模型上游地址与 Key
如果需要调整上游大模型 API（如从内网大模型网关转发）：
在宿主机挂载的配置或直接通过容器环境变量传参：
```bash
# 可在宿主机直接编辑 Orchestrator 配置文件：
# 容器内部使用的配置文件若已持久化，可直接修改 /data/openwiki/config.json
```
若使用纯环境变量启动：
```bash
docker run -d \
  --name openwiki \
  --restart always \
  -p 80:80 \
  -v /data/openwiki:/data/openwiki \
  -e OPENAI_UPSTREAM_URL="http://your-llm-gateway:8889/v1" \
  -e OPENWIKI_API_KEY="sk-xxxx" \
  -e OPENWIKI_MODEL="glm-5.2" \
  openwiki:arm64
```

### 2. 配置全局 Git HTTP 统一认证（克隆私有 GitLab 仓库）
若代码仓位于内网私有 GitLab，需配置凭据自动拉取代码：
```bash
docker run -d \
  --name openwiki \
  --restart always \
  -p 80:80 \
  -v /data/openwiki:/data/openwiki \
  -e GIT_HTTP_HOST="gitlab.company.com" \
  -e GIT_HTTP_USERNAME="oauth2" \
  -e GIT_HTTP_TOKEN="glpat-xxxxxxxxxxxxxxxx" \
  openwiki:arm64
```
容器启动脚本 `entrypoint.sh` 会自动将该 Token 注入到容器的 `~/.git-credentials` 中。

---

## 五、验证清单与常见问题排查

### 1. 验证清单 (Checklist)
- [ ] `docker ps` 输出中容器状态为 `Up ... (healthy)`。
- [ ] 浏览器访问 `http://<IP>/portal/` 能正常加载管理台。
- [ ] 旧仓库列表、分支信息与构建日志正常可见。
- [ ] 静态文档与知识图谱（Force-Graph / Mermaid）正常加载渲染。
- [ ] 触发一次代码构建（Build），任务能正常进入队列并执行完成。
- [ ] 触发一次知识问答（QA），能正常流式返回回答。

### 2. 常见问题排查

| 现象 | 排查命令与解决办法 |
| :--- | :--- |
| **容器状态显示 `(unhealthy)`** | 运行 `docker exec openwiki /app/healthcheck.sh` 查看是哪一项失败。分别检查 `/data/openwiki/logs/orchestrator/app.log` 和 `portal/app.log`。 |
| **提示 SQLite 数据库锁定 (database is locked)** | 迁移时若未停止旧服务可能造成 WAL 锁。在新机器宿主机执行：`sqlite3 /data/openwiki/db/orchestrator.db "PRAGMA wal_checkpoint(TRUNCATE);"` |
| **代码克隆报认证失败 (Authentication failed)** | 检查环境变量 `GIT_HTTP_TOKEN` 是否过期，或在宿主机 `/data/openwiki/repos/` 查看对应仓库的 `.git/config` 地址。 |
| **QA 问答超时无响应** | 检查 Orchestrator 日志中对上游 LLM 的网络连通性：`docker exec openwiki curl -I http://your-llm-gateway/v1/models`。 |

