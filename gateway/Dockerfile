# ============================================================
# Stage 1: build
# ============================================================
# 使用项目根目录（shopmicro/）作为 build context，
# 这样才能访问 go.work、gateway/、pkg/ 以及各服务的 proto 文件。
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git

# 复制 go.work 和所有 go.mod
COPY go.work go.work.sum ./
COPY gateway/go.mod gateway/go.sum ./
COPY pkg/go.mod pkg/go.sum ./

# 下载依赖（利用 Docker cache layer）
RUN cd gateway && go mod download
RUN cd pkg && go mod download

# 复制源码（包括 proto 文件，go work use 已将所有模块纳入工作空间）
COPY . .

# 编译 gateway
RUN cd gateway && go build -o /gateway-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# 复制二进制和配置
COPY --from=builder /gateway-bin ./
COPY gateway/conf ./conf

# 复制服务 proto 文件（供 generic proxy 运行时使用）
# proto 约定路径: {protoBase}{service}/api/{service}.proto
# 设置 PROTO_BASE=/app/service-protos 以使用容器内路径
COPY user/api /app/service-protos/user/api

EXPOSE 8080

# protoBase 在容器内应指向 /app/service-protos/
# 通过环境变量覆盖 config.yaml 中的 protoBase（无需重新构建镜像）
ENV PROTO_BASE=/app/service-protos/

ENTRYPOINT ["./gateway-bin", "-c", "conf/config.yaml"]
