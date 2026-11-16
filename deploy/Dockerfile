# ==============================================================================
# OpenWiki All-in-One 单容器 (全家桶) 生产级 Dockerfile (ARM64 专版)
# ==============================================================================

FROM node:22-bookworm-slim

WORKDIR /app

# 1. 替换阿里云 Debian 软件源以保证国内高速稳定下载
RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || \
    sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list 2>/dev/null

RUN apt-get update && apt-get install -y --no-install-recommends \
    nginx \
    git \
    openssh-client \
    sqlite3 \
    logrotate \
    curl \
    procps \
    cron \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# 2. 全局安装 openwiki CLI 并建立软链接
RUN npm config set registry https://registry.npmmirror.com && \
    npm install -g openwiki@0.5.1 && \
    ln -sfn $(npm root -g)/openwiki/dist /app/dist

# 3. 创建持久化数据目录与应用目录结构
RUN mkdir -p /data/openwiki/repos \
             /data/openwiki/static \
             /data/openwiki/db \
             /data/openwiki/logs/nginx \
             /data/openwiki/logs/orchestrator \
             /data/openwiki/logs/portal \
             /app/scripts \
             /app/assets/vendor \
             /app/public

# 4. 复制预编译 ARM64 二进制及依赖资源
ARG TARGETARCH
COPY bin/${TARGETARCH:-arm}/orchestrator /app/orchestrator
COPY bin/${TARGETARCH:-arm}/portal /app/portal
COPY bin/${TARGETARCH:-arm}/openwiki-migrate /app/openwiki-migrate
COPY bin/${TARGETARCH:-arm}/scripts/ /app/scripts/
COPY bin/${TARGETARCH:-arm}/assets/ /app/assets/
COPY bin/${TARGETARCH:-arm}/public/ /app/public/
COPY bin/${TARGETARCH:-arm}/config.json /app/config.json
COPY bin/${TARGETARCH:-arm}/config.yaml /app/config.yaml

# 5. 复制系统配置文件与控制脚本
COPY nginx.conf /etc/nginx/nginx.conf
COPY logrotate.conf /etc/logrotate.d/openwiki
COPY schema.sql /app/schema.sql
COPY entrypoint.sh /app/entrypoint.sh
COPY healthcheck.sh /app/healthcheck.sh

# 6. 赋予可执行权限
RUN chmod +x /app/orchestrator /app/portal /app/openwiki-migrate /app/entrypoint.sh /app/healthcheck.sh /app/scripts/*.sh 2>/dev/null || true

# 7. 配置容器健康检查探针
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/app/healthcheck.sh"]

# 8. 声明暴露端口与数据卷
EXPOSE 80 8080 3000
VOLUME ["/data/openwiki"]

ENTRYPOINT ["/app/entrypoint.sh"]
