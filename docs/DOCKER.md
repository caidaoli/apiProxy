# Docker 部署说明

## 构建和运行

### 使用 Docker

```bash
# 构建镜像
docker build -t api-proxy:latest .

# 运行容器
docker run -d \
  --name api-proxy-server \
  -p 8000:8000 \
  -e PORT=8000 \
  --restart unless-stopped \
  api-proxy:latest
```

### 使用 Docker Compose

```bash
# 启动服务
docker-compose up -d

# 查看日志
docker-compose logs -f

# 停止服务
docker-compose down
```

## 健康检查

容器包含内置的健康检查，会定期检查 `/stats` 端点：

```bash
# 查看容器健康状态
docker ps

# 查看健康检查日志
docker inspect api-proxy-server | grep -A 10 Health
```

## 环境变量

- `PORT`: 服务器监听端口（默认：8000）

## 数据持久化

当前版本的统计数据存储在内存中，容器重启后会丢失。如需持久化，可以考虑：

1. 添加数据库支持
2. 挂载数据卷
3. 使用外部存储服务

## 访问服务

- 主页: http://localhost:8000
- 统计API: http://localhost:8000/stats
- API代理: http://localhost:8000/{endpoint}/*

## 生产环境建议

1. 使用反向代理（如 Nginx）
2. 配置 HTTPS
3. 设置适当的资源限制
4. 配置日志收集
5. 监控和告警
