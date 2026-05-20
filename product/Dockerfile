# ============================================================
# Stage 1: build
# ============================================================
FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.work go.work.sum ./
COPY product/go.mod product/go.sum ./
COPY pkg/go.mod pkg/go.sum ./

RUN cd product && go mod download
RUN cd pkg && go mod download

COPY . .

RUN cd product && go build -o /product-bin .

# ============================================================
# Stage 2: runtime
# ============================================================
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

EXPOSE 8888 8897

COPY --from=builder /product-bin ./
COPY product/conf ./conf

RUN adduser -D -H -s /sbin/nologin appuser
USER appuser

ENV SERVER_ADDR=0.0.0.0:8888
ENV HEALTH_ADDR=:8897

ENTRYPOINT ["./product-bin", "-c", "conf/config.yaml"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8897/health || exit 1
