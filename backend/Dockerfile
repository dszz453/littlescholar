# ============ 构建阶段 ============
FROM golang:1.22-alpine AS builder

WORKDIR /build

# 国内网络加速（如不需要可删除）
ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0
ENV GOOS=linux

# 先复制依赖清单，利用 Docker 层缓存
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

# 复制源码并编译
COPY . .

# ============ 构建信息注入 ============
#
# **版本号不在这里传**：它的唯一来源是 internal/version/version.go 的
# `Version` 常量（升版本只改那一行）。这里只补两个"构建那一刻才知道"
# 的字段，让后台「系统状态」和 `GET /api/health` 能回答
# "线上这台跑的到底是哪一次构建"：
#
#   GIT_SHA     由 CI 传（github.sha）；本地 docker build 不传就是空
#   BUILD_TIME  不传就在构建时现取 UTC 时刻
#
# 两者都可空：缺了只是少显示一段，/api/health 的 version 照常有值。
ARG GIT_SHA=""
ARG BUILD_TIME=""

RUN set -e; \
    LDF="-s -w"; \
    if [ -n "$GIT_SHA" ]; then \
        LDF="$LDF -X github.com/littlescholar/core/internal/version.Commit=$GIT_SHA"; \
    fi; \
    if [ -z "$BUILD_TIME" ]; then BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ); fi; \
    LDF="$LDF -X github.com/littlescholar/core/internal/version.BuildTime=$BUILD_TIME"; \
    go mod tidy && \
    go build -ldflags="$LDF" -o /build/core-server ./cmd/server

# ============ 运行阶段 ============
FROM alpine:3.19

# 安装运行时依赖：时区、ca 证书、ffmpeg（转码用）
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    ffmpeg \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

COPY --from=builder /build/core-server /app/core-server

# 数据目录
RUN mkdir -p /app/data/media /app/data/transcode

# 拷贝一份默认配置模板（可用 compose 挂载覆盖）
COPY --from=builder /build/config.yaml /app/config.yaml

ENV TZ=Asia/Shanghai \
    SERVER_MODE=release \
    SERVER_PORT=8080 \
    DATABASE_DRIVER=postgres \
    DATABASE_HOST=postgres \
    DATABASE_PORT=5432 \
    DATABASE_USER=postgres \
    DATABASE_PASSWORD=postgres \
    DATABASE_NAME=littlescholar \
    STORAGE_MEDIA_DIR=/app/data/media \
    STORAGE_TRANSCODE_DIR=/app/data/transcode

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q --spider http://localhost:8080/api/health || exit 1

ENTRYPOINT ["/app/core-server"]
CMD ["-config", "/app/config.yaml"]
