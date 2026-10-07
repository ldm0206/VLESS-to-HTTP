# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# v2h 是一个单文件 Go 程序，Xray 内核以库的形式编译在里面，
# 所以运行镜像里既没有 Xray 可执行文件，也没有内核配置文件。
# ---------------------------------------------------------------------------

# ---- 构建阶段 -------------------------------------------------------------
FROM golang:1.26-alpine AS build

# 版本信息由 CI 用 tag / commit 注入，手工构建时保持默认值。
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# 依赖清单单独一层：只改源码时 go mod download 的缓存不会被冲掉。
COPY go.mod go.sum ./
RUN go mod download

# 其余源码（.dockerignore 已经把 .git、data/、文档等排除在外）。
COPY . .

# CGO_ENABLED=0 让产物完全静态，可直接跑在 scratch/alpine 上；
# -trimpath 去掉本地路径，-s -w 去掉符号表和调试信息；
# -buildvcs=false：镜像里没有 .git，显式关掉 VCS stamping，避免构建机装没装 git 影响结果。
ENV CGO_ENABLED=0
RUN go build -trimpath -buildvcs=false \
        -ldflags "-s -w \
            -X github.com/ldm0206/vless-to-http/internal/version.Version=${VERSION} \
            -X github.com/ldm0206/vless-to-http/internal/version.Commit=${COMMIT} \
            -X github.com/ldm0206/vless-to-http/internal/version.Date=${DATE}" \
        -o /out/v2h ./cmd/v2h

# ---- 运行阶段 -------------------------------------------------------------
FROM alpine:3.20

# 拉订阅、校验 Cloudflare Turnstile 都要 CA 证书；tzdata 让日志按本地时区显示。
# 前端（面板 SPA）已经 go:embed 进二进制，不需要额外的静态文件。
RUN apk add --no-cache ca-certificates tzdata

# 非 root 运行。uid/gid 固定为 1000，宿主上 chown 挂载目录时好写。
RUN addgroup -g 1000 -S v2h \
 && adduser -u 1000 -S -D -H -G v2h -s /sbin/nologin v2h \
 && mkdir -p /data \
 && chown v2h:v2h /data \
 && chmod 0700 /data

COPY --from=build /out/v2h /usr/local/bin/v2h

# 数据目录：config.yaml（0600，含明文代理密码）、cache/、state/、logs/ 都在这里。
# 用宿主目录挂载时（见 docker-compose.yml），必须先 chown 1000:1000，否则容器写不进去。
ENV V2H_DATA_DIR=/data
VOLUME ["/data"]

EXPOSE 9080 8080 1080

USER v2h
WORKDIR /data

# 健康检查直接用二进制自己，镜像里不需要 curl / wget：
# `v2h status` 的第一步就是 GET http://<panel.listen>/api/health（见 internal/cli/cli.go
# 的 apiClient → client.Health），之后还会用 config.yaml 里的 api_token 走一次
# 带鉴权的 /api/status，因此它验证的比裸 /api/health 更多（连接、鉴权、路由都测到了）；
# 面板地址也是从 config.yaml 读的，改了 panel.listen 不用改这里。
# 前提：panel.token_ips 里保留 127.0.0.1/8（默认值），否则容器会被标记为 unhealthy。
HEALTHCHECK --interval=30s --timeout=15s --start-period=30s --retries=3 \
    CMD ["v2h", "status", "--json"]

ENTRYPOINT ["v2h"]
CMD ["serve"]
