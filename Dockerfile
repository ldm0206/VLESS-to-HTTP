# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# v2h 是一个单文件 Go 程序，Xray 内核以库的形式编译在里面，
# 所以运行镜像里既没有 Xray 可执行文件，也没有内核配置文件。
#
# 构建分两种情况：
#   * CI（每次提交发版）：先在 runner 上用 Go 原生交叉编译出各架构二进制，
#     放进 prebuilt/ 再构建镜像。镜像里不再编译，也就完全不需要 QEMU
#     模拟编译——那一步在 arm/v7 上要几十分钟。
#   * 本机 `docker build .`：没有 prebuilt/，就在构建阶段直接编译（只支持
#     当前架构），代价是每次都重新下载依赖。
# ---------------------------------------------------------------------------

# CI 传 BUILD_BASE=alpine:3.20 来彻底跳过 Go 工具链；本机构建用默认值。
ARG BUILD_BASE=golang:1.26-alpine

# ---- 构建阶段 -------------------------------------------------------------
FROM ${BUILD_BASE} AS build

# buildx 会自动注入这两个参数（linux/arm/v7 → arm + v7）。
ARG TARGETARCH
ARG TARGETVARIANT

# 版本信息由 CI 用提交号注入，手工构建时保持默认值。
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src
COPY . .

# prebuilt/ 里有对应架构的二进制就直接装进去，否则在本机编译（见文件头的说明）。
# 最后执行一次 `version` 自检：原生架构下是真跑，交叉架构下由 QEMU 运行，
# 模拟不了时只打印一行提示，不让构建失败。
RUN set -eux; \
    mkdir -p /out; \
    prebuilt="/src/prebuilt/v2h-linux-${TARGETARCH}${TARGETVARIANT}"; \
    if [ -f "$prebuilt" ]; then \
        echo "使用预编译二进制：$prebuilt"; \
        install -m755 "$prebuilt" /out/v2h; \
    else \
        echo "prebuilt/ 里没有对应二进制，改为在镜像内编译（仅限当前架构）"; \
        CGO_ENABLED=0 go build -trimpath -buildvcs=false \
            -ldflags "-s -w \
                -X github.com/ldm0206/vless-to-http/internal/version.Version=${VERSION} \
                -X github.com/ldm0206/vless-to-http/internal/version.Commit=${COMMIT} \
                -X github.com/ldm0206/vless-to-http/internal/version.Date=${DATE}" \
            -o /out/v2h ./cmd/v2h; \
    fi; \
    /out/v2h version || echo "(skip version self-check: cannot run this arch here)"

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
