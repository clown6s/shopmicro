# ============================================================
# Stage 1: build
# ============================================================
# 使用项目根目录（shopmicro/）作为 build context，
# 这样才能访问 go.work 以及 pkg/ 模块。
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git

# 复制 go.work 和所有 go.mod
COPY go.work go.work.sum ./
COPY user/go.mod user/go.sum ./
COPY pkg/go.mod pkg/go.sum ./

# 下载依赖（利用 Docker cache layer）
RUN cd user && go mod download
RUN cd pkg && go mod download

# 复制源码
COPY . .

# 编译 user 服务
RUN cd user && go build -o /user-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# gRPC 服务监听 127.0.0.1:8888（仅容器内部），健康检查监听 :8899
EXPOSE 8888 8899

COPY --from=builder /user-bin ./
COPY user/conf ./conf

# 环境变量覆盖敏感配置（无需重新构建镜像）
# MYSQL_DSN, JWT_SECRET, REDIS_ADDR, REDIS_PASSWORD, ETCD_ADDRS
ENV SERVER_ADDR=0.0.0.0:8888
ENV HEALTH_ADDR=:8899

ENTRYPOINT ["./user-bin", "-c", "conf/config.yaml"]
