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

# 复制 go.work 和所有 go.mod（注意：COPY src dst，dst 末尾有 / 才是目录）
COPY go.work go.work.sum ./
COPY user/go.mod user/go.sum ./user/
COPY pkg/go.mod pkg/go.sum ./pkg/

# 下载依赖（利用 Docker cache layer）
# GOWORK=off: 忽略 go.work，只下载当前模块的直接依赖
# GOPROXY: 使用国内代理
WORKDIR /build/user
RUN GOWORK=off go mod download
WORKDIR /build/pkg
RUN GOWORK=off go mod download
WORKDIR /build

# 复制源码
COPY . .

# 编译 user 服务
RUN cd user && go build -o /user-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata curl

WORKDIR /app

EXPOSE 6001 8899

COPY --from=builder /user-bin ./
COPY user/conf ./conf

# 创建非 root 用户运行应用（安全最佳实践）
RUN adduser -D -H -s /sbin/nologin appuser
USER appuser

ENTRYPOINT ["./user-bin", "-c", "conf/config.yaml"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=5 \
  CMD curl -sf http://localhost:8899/health || exit 1
