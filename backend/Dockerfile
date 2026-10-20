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

# ============ whisper.cpp（本机语音听写）============
#
# 为什么要自己做一份：这套库是**英语学习资料**，网上没有现成字幕可搜；
# 而云端网关（OpenAI 兼容的 /audio/transcriptions）里根本没有转写模型
# （whisper-1 / gpt-4o-transcribe / whisper-large-v3 / whisper 四个名字
# 全回 `No available channel for model`）。唯一稳的路是把模型放进容器。
#
# 为什么在镜像里编译而不是下预编译包：仓库只发 macOS/Windows 的 release，
# Linux 二进制要自己编；这一步在 CI 上做（runner 有网、有 CPU），
# 服务器只 pull，跟其它镜像的构建方式保持一致。
FROM alpine:3.19 AS whisper

RUN apk add --no-cache build-base cmake curl

# 版本钉死：whisper-cli 的参数名在版本间改过（-np / -tp 都是后加的），
# 跟着 master 走会出现"今天能转、明天报用法错误"。
ARG WHISPER_REF=v1.7.4

RUN set -eux; \
    curl -fsSL --retry 3 -o /tmp/w.tar.gz \
      "https://codeload.github.com/ggerganov/whisper.cpp/tar.gz/refs/tags/${WHISPER_REF}"; \
    mkdir -p /src && tar -xzf /tmp/w.tar.gz -C /src --strip-components=1

WORKDIR /src/build
# 静态链 whisper/ggml（BUILD_SHARED_LIBS=OFF），运行镜像里就不必带一堆 .so；
# GGML_NATIVE=OFF 是为了"编译机 CPU 新、跑在 NAS 上"不会因指令集不兼容而崩；
# GGML_OPENMP=OFF 少一个 libgomp 运行时依赖。
RUN cmake .. \
      -DCMAKE_BUILD_TYPE=Release \
      -DBUILD_SHARED_LIBS=OFF \
      -DWHISPER_BUILD_TESTS=OFF \
      -DWHISPER_BUILD_EXAMPLES=ON \
      -DWHISPER_BUILD_SERVER=OFF \
      -DGGML_NATIVE=OFF \
      -DGGML_OPENMP=OFF \
      -DGGML_BLAS=OFF \
 && cmake --build . --config Release -j"$(nproc)"

# 模型：优先英文 small 量化版（181MB，识别质量明显好过 base，4 核跑得动），
# 拿不到就降级 base。**统一改名成 model.bin** —— 配置里只认这一个路径，
# 换模型不用改 compose、也不用重新对路径。
#
# 先试 hf-mirror（国内可达），再试 huggingface，最后才报错退出：
# 静默失败会产出一个"带 whisper-cli 但没有模型"的镜像，
# 表现是听写永远不可用，而构建日志一片绿。
RUN set -eux; \
    mkdir -p /models; ok=0; \
    for m in small.en-q5_1 small.en base.en; do \
      for host in hf-mirror.com huggingface.co; do \
        if curl -fsSL --retry 2 --retry-delay 2 --max-time 1200 \
             -o /models/model.bin \
             "https://${host}/ggerganov/whisper.cpp/resolve/main/ggml-${m}.bin"; then \
          echo "whisper 模型 = ${m} (${host})"; ok=1; break 2; \
        fi; \
      done; \
    done; \
    [ "$ok" = "1" ] || { echo "模型下载失败：三个候选全不可用"; exit 1; }; \
    ls -lh /models/model.bin

# ============ 运行阶段 ============
FROM alpine:3.19

# 安装运行时依赖：时区、ca 证书、ffmpeg（转码 + 抽音轨）、
# libstdc++（whisper-cli 是 C++ 编译的，musl 基础镜像里没有）
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    ffmpeg \
    libstdc++ \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

COPY --from=builder /build/core-server /app/core-server

# 本机语音听写：whisper-cli + 模型（见上面的 whisper 阶段）
COPY --from=whisper /src/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=whisper /models/model.bin /app/models/model.bin

# 构建期自检：缺 so 的二进制在这里就暴露，而不是等线上第一次听写才失败。
# 判据用 --help 输出里的 "usage" —— 缺共享库时输出的是
# "Error loading shared library ..."，一定不匹配。
RUN set -eux; \
    out="$(whisper-cli --help 2>&1 || true)"; \
    echo "$out" | head -5; \
    echo "$out" | grep -qi "usage" || { echo "whisper-cli 跑不起来（多半缺共享库）"; exit 1; }; \
    test -s /app/models/model.bin

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
