# 版本校验机制 + Beijing ECS Docker 部署 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 OpenWiki 二进制与 Docker 镜像注入可查询的版本信息（git commit + build time），使部署时能核验"运行的代码就是最新代码"；同时在 wulanchabu 构建 x86 Docker 镜像并部署到 beijing，数据库指向 wulanchabu 的 MySQL，nginx 暴露 17388 端口。

**Architecture:** 通过 Go `ldflags` 将 git commit short hash 与构建时间注入到 orchestrator/portal 二进制的包级变量中，两个服务各自新增 `/api/version` 端点返回 JSON；`build-x86.sh` 脚本自动提取 git 信息并传入 ldflags；Dockerfile 增加 `LABEL` 和 build-arg 传递版本；`docker inspect` 与 `curl /api/version` 双通道校验。beijing 部署通过 `docker run` 环境变量 `DB_DSN` 指向 wulanchabu MySQL（需开放 3306 公网访问 + 安全组），端口映射 `17388:80`。

**Tech Stack:** Go 1.24 (ldflags -X), Docker (LABEL + build-arg), MySQL 8.4, nginx, Workbench CLI (文件传输 + 命令执行)

**Spec:** 本文档即 spec。

## Global Constraints

- Go 模块路径：orchestrator = `github.com/openwiki/orchestrator`，portal = `github.com/openwiki/portal`
- 构建脚本：`deploy/scripts/build-x86.sh`，输出目录 `deploy/bin/x86/`
- Dockerfile 位于 `deploy/Dockerfile`，构建上下文为 `deploy/` 目录
- Dockerfile 当前默认 `TARGETARCH` fallback 为 `arm`，x86 构建需传 `--build-arg TARGETARCH=x86`
- 两个服务现有 health 端点：orchestrator `/api/health`，portal `/portal/health`
- MySQL（wulanchabu 容器 `sub2api-goldendb`）用户 `openwiki` 密码 `openwiki_2026`，已授权 `openwiki@%`，库 `openwiki`
- wulanchabu MySQL 当前绑定 `127.0.0.1:3306`（仅本地），beijing 跨公网访问需改为 `0.0.0.0:3306` + 安全组放行 beijing 公网 IP `59.110.33.10`... 不对，beijing 访问 wulanchabu 需用 wulanchabu 公网 IP `8.160.163.69`
- beijing ECS：`i-2ze6dswz4p720jq1bulq`，cn-beijing，未安装 Docker
- wulanchabu ECS：`i-0jle27a6ly22jmi67ybd`，cn-wulanchabu，已有 Docker

## ECS 访问方式

两台 ECS 均通过阿里云 Workbench CLI 访问（公网 22 仅放行 Workbench 内网段 `100.104.0.0/16`，无法直接 SSH）。Workbench CLI 已安装（v1.0.4），配置了两个 profile：

### 机器 A — wulanchabu（默认·常用）

- Profile：`default`（当前激活）
- 实例 ID：`i-0jle27a6ly22jmi67ybd`，地域 `cn-wulanchabu`
- 公网 `8.160.163.69` / 私网 `172.20.91.72`

```bash
# 交互式登录
workbench connect -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu
# 执行单条命令（默认 profile，可省略 -P default）
workbench exec -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu --command "df -h"
# 上传文件
workbench upload ./local-file /remote/path/ -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu -f
# 下载文件
workbench download /remote/file ./local-dir/ -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu
```

### 机器 B — beijing（不常用）

- Profile：`beijing`（需显式指定 `-P beijing`）
- 实例 ID：`i-2ze6dswz4p720jq1bulq`，地域 `cn-beijing`
- 公网 `59.110.33.10` / 私网 `172.21.180.74`

```bash
# 交互式登录
workbench connect -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing
# 执行单条命令
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command "free -h"
# 上传文件
workbench upload ./local-file /remote/path/ -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing -f
# 下载文件
workbench download /remote/file ./local-dir/ -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing
```

### Profile 切换

```bash
workbench config list              # 查看所有 profile（* 标记当前激活）
workbench config switch -P default  # 切回默认（wulanchabu）
workbench config switch -P beijing  # 切到 beijing
```

> 注意：日常操作保持 `default` 激活，连 beijing 时显式带 `-P beijing`，避免影响 wulanchabu 访问。两台机器均已写入本地 ed25519 公钥，待安全组放行公网 22 后可直接 `ssh root@<IP>`。


## Review Focus

- 二进制未注入版本时（默认值）version 端点返回 `unknown` 而非空字符串或崩溃
- `build-x86.sh` 在非 git 目录或 detached HEAD 时降级为 `unknown` + 时间戳，不阻塞构建
- Docker 镜像 `LABEL` 与二进制内版本一致（同一 git commit 贯穿构建链）
- beijing 容器 DB_DSN 连接 wulanchabu MySQL 失败时，entrypoint 日志有明确报错而非静默重启循环
- wulanchabu MySQL 改为 `0.0.0.0:3306` 后，原有本地服务（orchestrator/portal/sub2api）不受影响

---

### Task 1: Orchestrator 版本注入与 `/api/version` 端点

**Files:**
- Create: `deploy/unit1-orchestrator/internal/version/version.go`
- Modify: `deploy/unit1-orchestrator/internal/api/router.go:266-270`（health 端点后新增 version 端点）
- Test: `deploy/unit1-orchestrator/internal/version/version_test.go`

**Interfaces:**
- Produces: `version.Info` struct (`Version`, `GitCommit`, `BuildTime` string fields)，`version.Get()` 返回 `Info`
- Consumes: `api/router.go` 调用 `version.Get()` 序列化为 JSON

- [ ] **Step 1: 编写 version 包测试**

```go
// version_test.go
package version

import (
	"strings"
	"testing"
)

func TestGetReturnsDefaults(t *testing.T) {
	// 保存原值，测试后恢复
	origVersion, origCommit, origBuild := Version, GitCommit, BuildTime
	defer func() { Version, GitCommit, BuildTime = origVersion, origCommit, origBuild }()

	Version, GitCommit, BuildTime = "unknown", "unknown", "unknown"
	info := Get()
	if info.Version != "unknown" {
		t.Fatalf("expected unknown, got %s", info.Version)
	}
	if info.GitCommit != "unknown" {
		t.Fatalf("expected unknown, got %s", info.GitCommit)
	}
}

func TestGetReflectsInjectedValues(t *testing.T) {
	origVersion, origCommit, origBuild := Version, GitCommit, BuildTime
	defer func() { Version, GitCommit, BuildTime = origVersion, origCommit, origBuild }()

	Version, GitCommit, BuildTime = "0.5.4", "abc1234", "2026-11-18T22:00:00Z"
	info := Get()
	if info.Version != "0.5.4" || info.GitCommit != "abc1234" || info.BuildTime != "2026-11-18T22:00:00Z" {
		t.Fatalf("unexpected: %+v", info)
	}
	if !strings.Contains(info.String(), "0.5.4") || !strings.Contains(info.String(), "abc1234") {
		t.Fatalf("String() should contain version and commit: %s", info.String())
	}
}
```

- [ ] **Step 2: 运行测试确认失败（包不存在）**

Run: `cd deploy/unit1-orchestrator && go test ./internal/version/ -run TestGet -v -count=1`
Expected: FAIL（package not found）

- [ ] **Step 3: 实现 version 包**

```go
// version.go
package version

import "fmt"

// 以下变量通过 ldflags -X 在编译时注入；默认值为 unknown
var (
	Version   = "unknown"
	GitCommit = "unknown"
	BuildTime = "unknown"
)

// Info 持有构建版本信息
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
}

// Get 返回当前版本信息快照
func Get() Info {
	return Info{Version: Version, GitCommit: GitCommit, BuildTime: BuildTime}
}

// String 返回单行可读摘要
func (i Info) String() string {
	return fmt.Sprintf("openwiki-orchestrator %s (commit: %s, built: %s)", i.Version, i.GitCommit, i.BuildTime)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd deploy/unit1-orchestrator && go test ./internal/version/ -run TestGet -v -count=1`
Expected: PASS

- [ ] **Step 5: 在 router.go 注册 `/api/version` 端点**

在 `router.go` 的 `routes()` 方法中，`/api/health` 注册之后添加：

```go
s.mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(version.Get())
})
```

并在 import 块添加 `"github.com/openwiki/orchestrator/internal/version"`。

- [ ] **Step 6: 编译验证 orchestrator**

Run: `cd deploy/unit1-orchestrator && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X github.com/openwiki/orchestrator/internal/version.Version=0.5.4 -X github.com/openwiki/orchestrator/internal/version.GitCommit=test123 -X github.com/openwiki/orchestrator/internal/version.BuildTime=2026-11-18T22:00:00Z" -o /tmp/orch-test .`
Expected: 编译成功，无错误

- [ ] **Step 7: 提交**

```bash
cd /Users/bigc/openwiki
git add deploy/unit1-orchestrator/internal/version/ deploy/unit1-orchestrator/internal/api/router.go
git commit -m "feat(orchestrator): 注入版本信息并暴露 /api/version 端点"
```

---

### Task 2: Portal 版本注入与 `/api/version` 端点

**Files:**
- Create: `deploy/unit2-portal/internal/version/version.go`
- Create: `deploy/unit2-portal/internal/version/version_test.go`
- Modify: `deploy/unit2-portal/internal/api/router.go:84`（health 端点后新增 version 端点）

**Interfaces:**
- Produces: `version.Info` struct (`Version`, `GitCommit`, `BuildTime`)，`version.Get()` 返回 `Info`
- 模块路径：`github.com/openwiki/portal/internal/version`

- [ ] **Step 1: 复制 version 包并调整模块路径**

将 Task 1 的 `version.go` 复制到 `deploy/unit2-portal/internal/version/`，package 声明不变（`package version`），`String()` 中 `openwiki-orchestrator` 改为 `openwiki-portal`。复制测试文件并调整 import。

- [ ] **Step 2: 运行测试确认通过**

Run: `cd deploy/unit2-portal && go test ./internal/version/ -run TestGet -v -count=1`
Expected: PASS

- [ ] **Step 3: 在 portal router.go 注册 `/api/version` 端点**

在 `deploy/unit2-portal/internal/api/router.go` 的 `routes()` 中 `/portal/health` 之后添加：

```go
s.mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(version.Get())
})
```

import 添加 `"github.com/openwiki/portal/internal/version"` 和 `"encoding/json"`。

- [ ] **Step 4: 编译验证 portal**

Run: `cd deploy/unit2-portal && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X github.com/openwiki/portal/internal/version.Version=0.5.4 -X github.com/openwiki/portal/internal/version.GitCommit=test456 -X github.com/openwiki/portal/internal/version.BuildTime=2026-11-18T22:00:00Z" -o /tmp/portal-test .`
Expected: 编译成功

- [ ] **Step 5: 提交**

```bash
cd /Users/bigc/openwiki
git add deploy/unit2-portal/internal/version/ deploy/unit2-portal/internal/api/router.go
git commit -m "feat(portal): 注入版本信息并暴露 /api/version 端点"
```

---

### Task 3: 更新 build-x86.sh 自动注入 git 版本信息

**Files:**
- Modify: `deploy/scripts/build-x86.sh:39-47`（go build 行）

**Interfaces:**
- Consumes: git 命令提取 `describe --tags`、`rev-parse --short HEAD`、`date -u`
- Produces: 编译产物自带版本信息，`/api/version` 可查

- [ ] **Step 1: 在 build-x86.sh 编译前提取版本变量**

在第 2 步"配置 Go 编译环境"之后、第 3 步"编译 Go 后端二进制"之前插入：

```bash
# 提取版本信息 (git describe + commit + 构建时间)
GIT_VERSION=$(cd "$ROOT_DIR" && git describe --tags --always --long 2>/dev/null | sed 's/-[0-9]*-g/-/g' || echo "unknown")
GIT_COMMIT=$(cd "$ROOT_DIR" && git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-s -w -X github.com/openwiki/orchestrator/internal/version.Version=${GIT_VERSION} -X github.com/openwiki/orchestrator/internal/version.GitCommit=${GIT_COMMIT} -X github.com/openwiki/orchestrator/internal/version.BuildTime=${BUILD_TIME} -X github.com/openwiki/portal/internal/version.Version=${GIT_VERSION} -X github.com/openwiki/portal/internal/version.GitCommit=${GIT_COMMIT} -X github.com/openwiki/portal/internal/version.BuildTime=${BUILD_TIME}"
echo "--> 版本: ${GIT_VERSION} (commit: ${GIT_COMMIT}, built: ${BUILD_TIME})"
```

- [ ] **Step 2: 将三处 go build 的 ldflags 替换为 $LDFLAGS**

把 `-ldflags="-s -w"` 改为 `-ldflags="${LDFLAGS}"`，覆盖 orchestrator、portal、openwiki-migrate 三处。

- [ ] **Step 3: 运行 build-x86.sh 验证**

Run: `cd /Users/bigc/openwiki && bash deploy/scripts/build-x86.sh`
Expected: 编译成功，输出包含版本行 `--> 版本: v0.3.1-112-gfff0ea7 (commit: fff0ea7, ...)`

- [ ] **Step 4: 提交**

```bash
cd /Users/bigc/openwiki
git add deploy/scripts/build-x86.sh
git commit -m "build: build-x86.sh 自动注入 git 版本信息到 ldflags"
```

---

### Task 4: Dockerfile 增加 VERSION build-arg 与 LABEL

**Files:**
- Modify: `deploy/Dockerfile:1-10`（头部增加 ARG + LABEL）

- [ ] **Step 1: 在 Dockerfile FROM 之后增加 ARG 和 LABEL**

在 `FROM node:22-bookworm-slim` 之后插入：

```dockerfile
ARG APP_VERSION=unknown
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
LABEL org.openwiki.version="${APP_VERSION}" \
      org.openwiki.git-commit="${GIT_COMMIT}" \
      org.openwiki.build-time="${BUILD_TIME}"
ENV OPENWIKI_VERSION=${APP_VERSION} \
    OPENWIKI_GIT_COMMIT=${GIT_COMMIT} \
    OPENWIKI_BUILD_TIME=${BUILD_TIME}
```

- [ ] **Step 2: 提交**

```bash
cd /Users/bigc/openwiki
git add deploy/Dockerfile
git commit -m "build: Dockerfile 增加 VERSION build-arg 与 LABEL 标签"
```

---

### Task 5: 重新编译最新 x86 二进制（带版本注入）

**Files:**
- 无新文件；使用更新后的 `build-x86.sh`

- [ ] **Step 1: 运行 build-x86.sh**

Run: `cd /Users/bigc/openwiki && bash deploy/scripts/build-x86.sh`
Expected: 编译成功，输出版本行

- [ ] **Step 2: 同步资源到 bin/x86**

```bash
cd /Users/bigc/openwiki/deploy
rm -rf bin/x86/scripts bin/x86/assets bin/x86/public
mkdir -p bin/x86/scripts bin/x86/assets bin/x86/public
cp -r unit1-orchestrator/scripts/* bin/x86/scripts/
cp -r unit1-orchestrator/assets/* bin/x86/assets/
cp -r unit2-portal/public/* bin/x86/public/
chmod +x bin/x86/scripts/*.sh 2>/dev/null || true
```

- [ ] **Step 3: 记录当前版本号供后续校验**

Run: `cd /Users/bigc/openwiki && echo "$(git describe --tags --always --long) $(git rev-parse --short HEAD) $(date -u +%FT%TZ)" | tee /tmp/openwiki-build-version.txt`
Expected: 输出类似 `v0.3.1-112-gfff0ea7 fff0ea7 2026-11-18T23:00:00Z`

---

### Task 6: 部署最新二进制到 wulanchabu 并验证版本

**Files:**
- 无文件修改；远程操作

- [ ] **Step 1: 打包 bin/x86 并上传到 wulanchabu**

```bash
cd /Users/bigc/openwiki/deploy
tar czf /tmp/openwiki-x86-bin.tar.gz -C bin/x86 orchestrator portal openwiki-migrate scripts assets public config.json config.yaml
workbench upload /tmp/openwiki-x86-bin.tar.gz /tmp/ -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu -f
```

- [ ] **Step 2: 替换二进制并重启服务**

```bash
workbench exec -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu --command '
set -e
cd /opt/openwiki/bin
TS=$(date +%Y%m%d%H%M%S)
cp orchestrator orchestrator.bak-$TS; cp portal portal.bak-$TS
cd /tmp && tar xzf openwiki-x86-bin.tar.gz
cp -f orchestrator portal openwiki-migrate /opt/openwiki/bin/
chmod +x /opt/openwiki/bin/orchestrator /opt/openwiki/bin/portal /opt/openwiki/bin/openwiki-migrate
cp -rf scripts/* /opt/openwiki/bin/scripts/ 2>/dev/null || true
cp -rf assets/* /opt/openwiki/assets/ 2>/dev/null || true
cp -rf public/* /opt/openwiki/public/ 2>/dev/null || true
mkdir -p /data/openwiki/logs/orchestrator /data/openwiki/logs/portal
pkill -f /opt/openwiki/bin/orchestrator; pkill -f /opt/openwiki/bin/portal; sleep 2
cd /opt/openwiki
nohup /opt/openwiki/bin/orchestrator -config /opt/openwiki/config.json > /data/openwiki/logs/orchestrator/app.log 2>&1 &
nohup /opt/openwiki/bin/portal -config /opt/openwiki/config.yaml -public /opt/openwiki/public > /data/openwiki/logs/portal/app.log 2>&1 &
sleep 4
curl -s http://127.0.0.1:3009/api/version
echo
curl -s http://127.0.0.1:8099/api/version
'
```
Expected: 两个 version 端点返回包含 `v0.3.1-112-gfff0ea7` 的 JSON

- [ ] **Step 3: 校验 wulanchabu 版本与本地 git HEAD 一致**

对比 `/api/version` 返回的 `git_commit` 与本地 `git rev-parse --short HEAD`。一致则通过。

---

### Task 7: 在 wulanchabu 构建 x86 Docker 镜像

**Files:**
- 无文件修改；远程构建

- [ ] **Step 1: 打包完整 deploy 构建上下文并上传**

```bash
cd /Users/bigc/openwiki/deploy
tar czf /tmp/openwiki-build-context.tar.gz \
  Dockerfile openwiki-0.5.4.tgz nginx.conf logrotate.conf schema.sql \
  entrypoint.sh healthcheck.sh bin/x86
workbench upload /tmp/openwiki-build-context.tar.gz /tmp/ -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu -f
```

- [ ] **Step 2: 在 wulanchabu 上解包并 docker build**

```bash
workbench exec -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu --command '
set -e
rm -rf /tmp/openwiki-build && mkdir -p /tmp/openwiki-build
cd /tmp/openwiki-build && tar xzf /tmp/openwiki-build-context.tar.gz
GIT_VERSION=$(echo "v0.3.1-112-gfff0ea7")  # 与本地一致
GIT_COMMIT=$(echo "fff0ea7")
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
docker build \
  --build-arg TARGETARCH=x86 \
  --build-arg APP_VERSION="$GIT_VERSION" \
  --build-arg GIT_COMMIT="$GIT_COMMIT" \
  --build-arg BUILD_TIME="$BUILD_TIME" \
  -t openwiki:latest \
  -t "openwiki:${GIT_COMMIT}" \
  .
'
```
Expected: 镜像构建成功

- [ ] **Step 3: 校验镜像 LABEL 版本**

```bash
workbench exec -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu --command 'docker inspect openwiki:latest --format "{{json .Config.Labels}}"'
```
Expected: JSON 包含 `org.openwiki.git-commit=fff0ea7`

- [ ] **Step 4: 导出镜像为 tar**

```bash
workbench exec -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu --command 'docker save openwiki:latest | gzip > /tmp/openwiki-image.tar.gz && ls -lh /tmp/openwiki-image.tar.gz'
```

---

### Task 8: wulanchabu MySQL 开放 beijing 公网访问

**Files:**
- 无文件修改；远程配置

- [ ] **Step 1: 修改 MySQL 容器端口绑定从 127.0.0.1 改为 0.0.0.0**

```bash
workbench exec -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu --command '
# 找到 sub2api-goldendb 的 compose 文件
docker inspect sub2api-goldendb --format "{{index .Config.Labels \"com.docker.compose.project.config_files\"}}"
'
```

找到 compose 文件后，将 `ports: - "127.0.0.1:3306:3306"` 改为 `- "0.0.0.0:3306:3306"` 或 `- "3306:3306"`，然后 `docker compose up -d` 重建容器。

- [ ] **Step 2: wulanchabu 安全组放行 beijing 公网 IP 的 3306**

需要用户在阿里云控制台操作，或在 wulanchabu 上通过 firewalld/iptables 放行 `59.110.33.10`（beijing 公网 IP）。实际上 beijing 容器访问 wulanchabu MySQL 走的是 beijing 公网出口 IP。

- [ ] **Step 3: 验证从 beijing 能连通 wulanchabu MySQL**

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command '
yum install -y mysql 2>/dev/null || apt-get install -y default-mysql-client 2>/dev/null || true
nc -z -w 5 8.160.163.69 3306 && echo "PORT_OPEN" || echo "PORT_BLOCKED"
'
```

---

### Task 9: beijing 安装 Docker 并加载镜像

**Files:**
- 无文件修改；远程操作

- [ ] **Step 1: beijing 安装 Docker**

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command '
dnf install -y dnf-plugins-core
dnf config-manager --add-repo https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo
dnf install -y docker-ce docker-ce-cli containerd.io
systemctl enable --now docker
docker --version
'
```

- [ ] **Step 2: 传输镜像 tar 到 beijing**

```bash
workbench download /tmp/openwiki-image.tar.gz /tmp/ -i i-0jle27a6ly22jmi67ybd -r cn-wulanchabu  # 下到本地
workbench upload /tmp/openwiki-image.tar.gz /tmp/ -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing -f  # 传到 beijing
```

- [ ] **Step 3: beijing docker load**

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command 'docker load < /tmp/openwiki-image.tar.gz && docker images | grep openwiki'
```

- [ ] **Step 4: 校验 beijing 镜像 LABEL 与本地版本一致**

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command 'docker inspect openwiki:latest --format "{{json .Config.Labels}}"'
```

---

### Task 10: beijing 启动容器并暴露 17388 端口

**Files:**
- 无文件修改；远程操作

- [ ] **Step 1: 启动容器**

DB_DSN 指向 wulanchabu MySQL 公网 `8.160.163.69:3306`，端口映射 `17388:80`。

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command '
docker run -d \
  --name openwiki \
  --restart always \
  -p 17388:80 \
  -e DB_DSN="openwiki:openwiki_2026@tcp(8.160.163.69:3306)/openwiki?parseTime=true&multiStatements=true&charset=utf8mb4" \
  -e PORTAL_DB_DSN="openwiki:openwiki_2026@tcp(8.160.163.69:3306)/openwiki?parseTime=true&multiStatements=true&charset=utf8mb4" \
  -e DB_AUTO_MIGRATE=true \
  -v /data/openwiki:/data/openwiki \
  openwiki:latest
'
```

- [ ] **Step 2: 等待容器健康检查通过**

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command '
for i in $(seq 1 30); do
  STATUS=$(docker inspect openwiki --format "{{.State.Health.Status}}" 2>/dev/null)
  echo "attempt $i: $STATUS"
  [ "$STATUS" = "healthy" ] && break
  sleep 2
done
docker logs openwiki 2>&1 | tail -20
'
```

- [ ] **Step 3: 验证 beijing 服务版本端点**

```bash
workbench exec -P beijing -i i-2ze6dswz4p720jq1bulq -r cn-beijing --command '
curl -s http://127.0.0.1:17388/api/version; echo
curl -s -o /dev/null -w "nginx(17388): %{http_code}\n" http://127.0.0.1:17388/
'
```
Expected: version JSON 的 `git_commit` 与本地 `git rev-parse --short HEAD` 一致

- [ ] **Step 4: beijing 安全组放行 17388 端口（公网访问）**

需要用户在阿里云控制台为 beijing ECS 安全组添加入方向规则：TCP 17388，源 0.0.0.0/0。

- [ ] **Step 5: 最终校验 — 公网访问 beijing 17388 版本端点**

Run: `curl -s http://59.110.33.10:17388/api/version`
Expected: 返回与本地 git HEAD 一致的版本 JSON

---

### Task 11: 版本校验 SOP 文档

**Files:**
- Create: `deploy/VERSION_VERIFICATION.md`

- [ ] **Step 1: 编写版本校验 SOP 文档**

文档内容：如何通过 `curl /api/version` 与 `docker inspect` 双通道核验部署的二进制/镜像是否为最新代码。包含：
- 构建时记录版本号命令
- wulanchabu 核验命令
- beijing 核验命令
- 不一致时的排查步骤

- [ ] **Step 2: 提交**

```bash
cd /Users/bigc/openwiki
git add deploy/VERSION_VERIFICATION.md
git commit -m "docs: 版本校验 SOP 文档"
```
