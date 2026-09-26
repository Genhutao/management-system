# syntax=docker/dockerfile:1
# 学管会综合管理系统后端 · 多阶段构建
# 纯 Go sqlite (github.com/glebarez/sqlite)，CGO_ENABLED=0 可产出静态二进制。

FROM golang:1.26-alpine AS builder
WORKDIR /src
# 先拷依赖清单再拉依赖，源码不变时层缓存命中，重建只编译不下载
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/xgh-server ./cmd/server

FROM alpine:3.20
# tzdata：时段判定与排班全部走 time.Local，容器默认 UTC 会让"今天/单双周"整体错位，必须装
RUN apk add --no-cache tzdata ca-certificates && \
    addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /out/xgh-server ./xgh-server
COPY rbac_model.conf ./
COPY static ./static

# 持久化目录：数据库、上传件、密钥文件都在 /data 下（见 compose 卷映射）
RUN mkdir -p /data/uploads && chown -R app:app /data /app
USER app

ENV DB_PATH=/data/xgh_system.db \
    GIN_MODE=release \
    TZ=Asia/Shanghai

EXPOSE 8080
# 健康检查探公开招新接口（免登录），确认进程与路由存活
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/v1/recruit/info || exit 1

ENTRYPOINT ["./xgh-server"]
