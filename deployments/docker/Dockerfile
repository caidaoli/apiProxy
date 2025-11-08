# Go版本的高性能API代理服务器Dockerfile
FROM golang:alpine AS builder

# 设置工作目录
WORKDIR /app

# 复制go模块文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY *.go ./
# 复制静态文件和HTML文件
COPY index.html ./
COPY static/ ./static/
# 编译应用
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o api-proxy .

# 运行阶段
FROM alpine:latest

# 安装CA证书
RUN apk --no-cache add ca-certificates tzdata

# 设置时区
ENV TZ=Asia/Shanghai

# 创建非root用户
RUN addgroup -g 1001 -S apiproxy && \
    adduser -u 1001 -S apiproxy -G apiproxy

WORKDIR /home/apiproxy/

# 从构建阶段复制二进制文件和静态文件
COPY --from=builder /app/api-proxy .
COPY --from=builder /app/index.html ./
COPY --from=builder /app/static/ ./static/

# 设置文件权限
RUN chown -R apiproxy:apiproxy .

# 切换到非root用户
USER apiproxy

# 暴露端口
EXPOSE 8000

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8000/stats || exit 1

# 启动应用
CMD ["./api-proxy"]