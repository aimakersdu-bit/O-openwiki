# ==============================================================================
# OpenWiki All-in-One 单容器 (全家桶) 生产级 Dockerfile
# 包含：Nginx (80), Portal (8080), Orchestrator (3000), OpenWiki CLI, Logrotate
# 说明：OpenWiki CLI 在镜像构建阶段通过命令全局安装，不包含本地源码。
# ==============================================================================

FROM node:22-bookworm-slim

# 设置工作目录
WORKDIR /app

# 1. 安装系统依赖 (nginx, git, sqlite3, logrotate, curl, procps, cron)
RUN apt-get update && apt-get install -y --no-install-recommends \
    nginx \
    git \
    sqlite3 \
    logrotate \
    curl \
    procps \
    cron \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# 2. 在容器内部全局安装 openwiki CLI (无需本地源码打包)
RUN npm install -g openwiki

# 3. 创建数据目录与资源目录结构
RUN mkdir -p /data/openwiki/repos \
             /data/openwiki/static \
             /data/openwiki/db \
             /data/openwiki/logs/nginx \
             /data/openwiki/logs/orchestrator \
             /data/openwiki/logs/portal \
             /app/scripts \
             /app/dist \
             /app/assets/vendor \
             /app/public

# 4. 从发布包 (arm 或 x86) 复制二进制与依赖资源
ARG TARGETARCH
COPY bin/${TARGETARCH:-arm}/orchestrator /app/orchestrator
COPY bin/${TARGETARCH:-arm}/portal /app/portal
COPY bin/${TARGETARCH:-arm}/scripts/ /app/scripts/
COPY bin/${TARGETARCH:-arm}/assets/vendor/ /app/assets/vendor/
COPY bin/${TARGETARCH:-arm}/public/ /app/public/
COPY bin/${TARGETARCH:-arm}/config.json /app/config.json
COPY bin/${TARGETARCH:-arm}/config.yaml /app/config.yaml

# 5. 复制配置文件、Nginx 配置及启动/探针脚本
COPY nginx.conf /etc/nginx/nginx.conf
COPY logrotate.conf /etc/logrotate.d/openwiki
COPY entrypoint.sh /app/entrypoint.sh
COPY healthcheck.sh /app/healthcheck.sh

# 6. 赋予可执行权限
RUN chmod +x /app/orchestrator /app/portal /app/entrypoint.sh /app/healthcheck.sh /app/scripts/*.sh 2>/dev/null || true

# 7. 配置容器健康与就绪检查探针 (Healthcheck & Readiness Probe)
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/app/healthcheck.sh"]

# 8. 声明对外暴露端口
EXPOSE 80 8080 3000

# 9. 声明有状态持久化数据卷
VOLUME ["/data/openwiki"]

# 10. 容器启动控制入口
ENTRYPOINT ["/app/entrypoint.sh"]
