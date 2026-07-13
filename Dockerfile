# ============================================================
# Stage 1: Build danmaku 二进制
# ============================================================
FROM golang:1.26-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 编译主服务（不包含压测工具）
# CGO_ENABLED=0 不需要 gcc/musl-dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/danmaku .

# ============================================================
# Stage 2: 最小运行镜像
# ============================================================
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# 创建非 root 用户
RUN adduser -D -u 1000 danmaku

WORKDIR /app
COPY --from=builder /bin/danmaku .
COPY --from=builder /build/templates ./templates
COPY --from=builder /build/config.yaml.example ./config.yaml.example

# 默认配置（用户可通过挂载 config.yaml 或环境变量覆盖）
RUN cp config.yaml.example config.yaml

RUN chown -R danmaku:danmaku /app

USER danmaku

EXPOSE 8080

# SIGTERM 触发 Go 的优雅关闭
STOPSIGNAL SIGTERM

CMD ["./danmaku"]
