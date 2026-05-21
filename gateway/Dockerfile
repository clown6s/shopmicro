# ============================================================
# Stage 1: build
# ============================================================
# 使用项目根目录（shopmicro/）作为 build context，
# 这样才能访问 go.work、gateway/、pkg/ 以及各服务的 proto 文件。
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git
ENV GOPROXY=https://goproxy.cn,direct

# 复制 go.work 和所有 go.mod
COPY go.work go.work.sum ./
COPY gateway/go.mod gateway/go.sum ./gateway/
COPY pkg/go.mod pkg/go.sum ./pkg/

# 下载依赖（利用 Docker cache layer）
# GOWORK=off: 忽略 go.work，只下载当前模块的直接依赖
# GOPROXY: 使用国内代理
WORKDIR /build/gateway
RUN GOWORK=off go mod download
WORKDIR /build/pkg
RUN GOWORK=off go mod download
WORKDIR /build

# 复制源码（包括 proto 文件，go work use 已将所有模块纳入工作空间）
COPY . .

# P2-01: 准备所有服务的 proto 文件到统一目录（替代硬编码 user/）
RUN mkdir -p /proto-out && \
    for svc_dir in /build/*/api; do \
      [ -d "$svc_dir" ] || continue; \
      svc_name=$(basename "$(dirname "$svc_dir")"); \
      [ "$svc_name" = "gateway" ] && continue; \
      [ "$svc_name" = "pkg" ] && continue; \
      mkdir -p "/proto-out/$svc_name"; \
      cp -r "$svc_dir" "/proto-out/$svc_name/api"; \
    done

# 编译 gateway
RUN cd gateway && go build -o /gateway-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata curl

WORKDIR /app

# 复制二进制和配置
COPY --from=builder /gateway-bin ./
COPY gateway/conf ./conf

# 创建非 root 用户运行应用（安全最佳实践）
RUN adduser -D -H -s /sbin/nologin appuser
USER appuser

# P2-01: 复制所有服务的 proto 文件（供 generic proxy 运行时使用）
# proto 约定路径: {protoBase}{service}/api/{service}.proto
# 设置 PROTO_BASE=/app/service-protos 以使用容器内路径
COPY --from=builder /proto-out /app/service-protos

EXPOSE 8080

# R5-P3-04: 使用 curl 替代 wget（Alpine busybox wget 未来版本可能移除）
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -sf http://localhost:8080/health || exit 1

# protoBase 在容器内应指向 /app/service-protos/
# 通过环境变量覆盖 config.yaml 中的 protoBase（无需重新构建镜像）
ENV PROTO_BASE=/app/service-protos/

ENTRYPOINT ["./gateway-bin", "-c", "conf/config.yaml"]
