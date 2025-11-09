# API 透明代理服务器

⚡ 高性能、符合 RFC 7230 标准的透明 API 代理服务器

## 核心特性

- **🔥 完全透明** - 严格遵循 RFC 7230，不修改请求/响应内容
- **💧 流式传输** - 边收边发，恒定内存使用（32KB缓冲区）
- **⚡ 高性能** - 原子操作统计系统，支持数万级并发
- **🚀 低延迟** - 平均响应时间 <50ms，P99 <100ms
- **📊 实时监控** - 内置统计面板和管理界面
- **🔧 热更新** - Redis 存储配置，动态加载无需重启

## 快速开始

### 本地运行

**前提条件**: Go 1.25.0+ 和 Redis

```bash
# 1. 克隆项目
git clone <repo-url>
cd apiProxy

# 2. 安装依赖
go mod download

# 3. 配置环境变量
cp .env.example .env
# 编辑 .env 设置: API_PROXY_REDIS_URL 和 ADMIN_TOKEN

# 4. 启动 Redis
docker run -d -p 6379:6379 --name redis redis:7-alpine

# 5. 启动服务（支持空 Redis 启动）
go run main.go
# 默认监听 http://localhost:8000
# ⚠️  服务会显示警告但正常启动，即使 Redis 中没有映射数据

# 6. 通过 API 添加第一个映射
curl -X POST http://localhost:8000/api/mappings \
  -H "Authorization: Bearer your_admin_token" \
  -H "Content-Type: application/json" \
  -d '{"prefix":"/api/v1","target":"https://api.example.com"}'

# 7. 或通过 Web 管理界面添加映射
# 访问 http://localhost:8000/admin
```

### Docker Compose 部署（推荐）

```bash
# 1. 配置环境变量
cp .env.example .env
# 编辑 .env 设置 REDIS_PASSWORD 和 ADMIN_TOKEN

# 2. 启动所有服务（自动创建 Redis 容器）
docker-compose up -d

# 3. 查看日志
docker-compose logs -f api-proxy

# 4. 初始化映射（首次启动）
curl -X POST http://localhost:8000/api/mappings \
  -H "Authorization: Bearer your_admin_token" \
  -H "Content-Type: application/json" \
  -d '{"prefix":"/openai","target":"https://api.openai.com"}'

# 5. 验证映射
curl http://localhost:8000/api/public/mappings
```

### 使用远程 Redis Cloud

```bash
# 1. 启动服务（使用远程 Redis）
docker compose -f docker-compose.test.yml up -d

# 2. 添加映射（即使 Redis 为空也能启动）
curl -X POST http://localhost:1111/api/mappings \
  -H "Authorization: Bearer testofli" \
  -H "Content-Type: application/json" \
  -d '{"prefix":"/cerebras","target":"https://api.cerebras.ai"}'

# 3. 查看所有映射
curl http://localhost:1111/api/public/mappings
```

## 环境变量

```bash
# Redis 连接
API_PROXY_REDIS_URL=redis://:password@localhost:6379/0

# 管理界面认证令牌
ADMIN_TOKEN=your_secure_token

# 服务端口（可选，默认 8000）
PORT=8000

# 统计功能开关（可选，默认启用）
ENABLE_STATS=true
```

## 核心架构

### 透明代理层
```go
// 完全透明转发，符合 RFC 7230
type TransparentProxy struct {
    client *http.Client
    mapper MappingManager
}

// 流式处理，零拷贝
func (p *TransparentProxy) ProxyRequest(w http.ResponseWriter, r *http.Request) error {
    // 直接传递 Body，避免内存分配
    req, _ := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)

    // 流式复制响应
    _, err = io.Copy(w, resp.Body)
    return err
}
```

### 高性能统计系统
```go
// 基于原子操作和读写锁的高性能设计
type Collector struct {
    requestCount      int64         // 原子计数器
    errorCount        int64         // 原子计数器
    responseTimeSum   int64         // 原子累加
    responseTimeCount int64         // 原子累加

    mu        sync.RWMutex          // 保护 endpoints map
    endpoints map[string]*EndpointStats
}

// 原子操作记录（64ns/op, 0 allocs）
func (c *Collector) RecordRequest(endpoint string) {
    atomic.AddInt64(&c.requestCount, 1)
}

func (c *Collector) RecordError() {
    atomic.AddInt64(&c.errorCount, 1)
}
```

### 连接池优化
```go
&http.Client{
    Transport: &http.Transport{
        MaxIdleConns:        1000,  // 全局连接池
        MaxIdleConnsPerHost: 100,   // 每个后端 100 连接
        MaxConnsPerHost:     200,   // 防止连接泄漏
        IdleConnTimeout:     90 * time.Second,
    },
}
```

## 主要路由

| 路径 | 功能 | 认证 |
|------|------|------|
| `/` | 统计面板（HTML） | 无 |
| `/stats` | 统计数据（JSON） | 无 |
| `/admin` | 管理界面（HTML） | Token |
| `/api/mappings` | 映射管理（API） | Token |
| `/<prefix>/*` | 透明代理转发 | 无 |

## API 使用示例

### OpenAI 代理
```bash
curl -X POST "http://localhost:8000/openai/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}],"stream":true}' \
  --no-buffer
```

### Claude 代理
```bash
curl -X POST "http://localhost:8000/claude/v1/messages" \
  -H "Content-Type: application/json" \
  -H "x-api-key: YOUR_API_KEY" \
  -d '{"model":"claude-3-opus-20240229","messages":[...]}'
```

## 管理界面

访问 `http://localhost:8000/admin` 打开管理面板：

1. **登录** - 输入 ADMIN_TOKEN
2. **查看映射** - 列出所有 API 路由
3. **添加映射** - 新增代理路由
4. **编辑/删除** - 修改现有路由
5. **实时生效** - 无需重启服务

### 管理 API
```bash
# 获取所有映射
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8000/api/mappings

# 添加映射
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prefix":"/newapi","target":"https://api.example.com"}' \
  http://localhost:8000/api/mappings

# 删除映射
curl -X DELETE \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8000/api/mappings/newapi
```

## 性能指标

### 基准测试结果
```bash
# 代理性能测试
BenchmarkTransparentProxy-16      23532    57751 ns/op    69707 B/op    109 allocs/op
BenchmarkLargeBody-16              1411   936505 ns/op    58203 B/op    156 allocs/op

# 统计收集器性能
BenchmarkCollector_RecordRequest   18M     64.82 ns/op        0 B/op      0 allocs/op
```

### 并发测试
```bash
# 1000 并发请求
hey -n 10000 -c 1000 http://localhost:8000/test/api

# 结果: ~80,000 QPS, 平均延迟 <100ms
```

### 内存使用
- **空闲**: 5-10 MB
- **中负载**: 15-25 MB
- **高负载**: 30-50 MB

## 透明代理原则

根据 RFC 7230，本代理严格遵循以下规则：

### ✅ 必须做
- 原样转发请求/响应头（除 hop-by-hop 头）
- 原样转发请求/响应体
- 流式传输（边收边发）
- 保持原始状态码和 Content-Type

### ❌ 禁止做
- 修改请求/响应内容
- 添加业务逻辑头部
- 缓存完整响应体
- 设置额外超时限制

### Hop-by-Hop 头部（必须过滤）
```
Connection, Keep-Alive, Proxy-Authenticate, Proxy-Authorization,
TE, Trailer, Transfer-Encoding, Upgrade
```

## 开发

### 运行测试
```bash
# 单元测试
go test ./...

# 基准测试
go test -bench=. -benchmem ./internal/proxy/
go test -bench=. -benchmem ./internal/stats/

# 代码检查
go fmt ./...
go vet ./...
```

### 构建
```bash
# 本地构建
go build -o apiproxy main.go

# Docker 构建
docker build -t apiproxy .
```

## 项目结构

```
apiProxy/
├── main.go                    # 主服务器
├── internal/
│   ├── proxy/
│   │   └── transparent.go     # 透明代理核心
│   ├── storage/
│   │   └── redis.go           # Redis 映射管理
│   ├── stats/
│   │   └── collector.go       # 统计收集器
│   ├── admin/
│   │   └── handler.go         # 管理界面
│   └── middleware/
│       └── stats.go           # 统计中间件
├── web/
│   ├── templates/             # HTML 模板
│   └── static/                # 静态资源
└── deployments/
    └── docker/                # Docker 配置
```

## 技术栈

- **Go 1.25.0+** - 高性能并发编程
- **Gin 1.11.0** - HTTP 框架
- **Redis 7.4+** - 配置存储
- **Docker** - 容器化部署

## 许可证

MIT License

## 贡献

欢迎提交 Issue 和 Pull Request！

---

**审查标准**: 代码遵循 Linus Torvalds 风格审查，严格执行 KISS、DRY、YAGNI 和 SOLID 原则。
