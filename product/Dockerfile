# ============================================================
# Stage 1: build
# ============================================================
# 使用项目根目录（shopmicro/）作为 build context，
# 这样才能访问 go.work 以及 pkg/ 模块。
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git

# 复制 go.work 和所有 go.mod
COPY go.work go.work.sum ./
COPY product/go.mod product/go.sum ./
COPY pkg/go.mod pkg/go.sum ./

# 下载依赖（利用 Docker cache layer）
RUN cd product && go mod download
RUN cd pkg && go mod download

# 复制源码
COPY . .

# 编译 product 服务
RUN cd product && go build -o /product-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata curl

WORKDIR /app

# gRPC 服务监听 0.0.0.0:8888（仅容器内部），健康检查监听 :8897
EXPOSE 8888 8897

COPY --from=builder /product-bin ./
COPY product/conf ./conf

# 创建非 root 用户运行应用（安全最佳实践）
RUN adduser -D -H -s /sbin/nologin appuser
USER appuser

# 环境变量覆盖敏感配置（无需重新构建镜像）
# MYSQL_DSN, REDIS_ADDR, REDIS_PASSWORD, ETCD_ADDRS
ENV SERVER_ADDR=0.0.0.0:8888
ENV HEALTH_ADDR=:8897

ENTRYPOINT ["./product-bin", "-c", "conf/config.yaml"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -sf http://localhost:8897/health || exit 1
