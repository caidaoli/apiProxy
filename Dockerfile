# API代理服务器 Docker镜像构建文件
# 使用 BuildKit 特性优化构建性能和缓存

# 语法特性：启用 BuildKit 新特性
# syntax=docker/dockerfile:1.4

# 构建阶段 - 使用多平台构建支持
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

# 安装构建依赖
RUN apk add --no-cache git ca-certificates tzdata

# 设置工作目录
WORKDIR /app

# 设置Go模块代理（加速依赖下载）
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct

# 复制 go mod 文件
COPY go.mod go.sum ./

# 下载依赖（利用缓存层加速）
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 复制源代码
COPY . .

# 编译二进制文件
# -s -w: 去除调试信息和符号表，减小体积约30%
# -trimpath: 去除文件系统路径，提升安全性和可重复性
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build \
    -ldflags="-s -w" \
    -trimpath \
    -o api-proxy .

# 运行阶段
FROM alpine:latest

# 安装运行时依赖
RUN apk --no-cache add ca-certificates tzdata wget

# 创建非root用户
RUN addgroup -g 1001 -S apiproxy && \
    adduser -u 1001 -S apiproxy -G apiproxy

# 设置工作目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /app/api-proxy .

# 复制 Web 静态文件
COPY --from=builder /app/web ./web

# 设置文件权限
RUN chown -R apiproxy:apiproxy /app

# 切换到非root用户
USER apiproxy

# 暴露端口
EXPOSE 8000

# 设置环境变量
ENV PORT=8000
ENV TZ=Asia/Shanghai

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8000/stats || exit 1

# 启动应用
CMD ["./api-proxy"]
