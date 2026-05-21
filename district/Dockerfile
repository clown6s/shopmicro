# ============================================================
# Stage 1: build
# ============================================================
# 使用项目根目录（shopmicro/）作为 build context，
# 这样才能访问 go.work 以及 pkg/ 模块。
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git
ENV GOPROXY=https://goproxy.cn,direct

# 复制 go.work 和所有 go.mod
COPY go.work go.work.sum ./
COPY district/go.mod district/go.sum ./district/
COPY pkg/go.mod pkg/go.sum ./pkg/

# 下载依赖（利用 Docker cache layer）
# GOWORK=off: 忽略 go.work，只下载当前模块的直接依赖
# GOPROXY: 使用国内代理
WORKDIR /build/district
RUN GOWORK=off go mod download
WORKDIR /build/pkg
RUN GOWORK=off go mod download
WORKDIR /build

# 复制源码
COPY . .

# 编译 district 服务
RUN cd district && go build -o /district-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata curl

WORKDIR /app

# gRPC 服务监听 127.0.0.1:8888（仅容器内部），健康检查监听 :8898
EXPOSE 8888 8898

COPY --from=builder /district-bin ./
COPY district/conf ./conf

# 创建非 root 用户运行应用（安全最佳实践）
RUN adduser -D -H -s /sbin/nologin appuser
USER appuser

# 环境变量覆盖敏感配置（无需重新构建镜像）
# MYSQL_DSN, AMAP_KEY, REDIS_ADDR, REDIS_PASSWORD, ETCD_ADDRS
ENV SERVER_ADDR=0.0.0.0:8888
ENV HEALTH_ADDR=:8898

ENTRYPOINT ["./district-bin", "-c", "conf/config.yaml"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -sf http://localhost:8898/health || exit 1
