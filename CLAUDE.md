# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

这是一个基于 Go 的异步 API 透明代理服务器,严格遵循 RFC 7230 标准,支持多种 AI API 代理(OpenAI、Claude、Gemini、XAI等),具有动态配置管理、实时统计和高并发能力。

**核心特性:**
- 完全透明代理:不修改请求/响应内容,仅转发
- 异步架构:毫秒级响应,边收边发的流式传输
- 动态配置:Redis存储映射,支持热更新无需重启
- 高并发:基于 goroutine,支持多线程并发处理

## 工具使用规范

### ⚠️ 强制要求:优先使用 Serena MCP

在此代码库中工作时,**必须优先使用 Serena MCP 工具**进行代码分析、搜索和编辑操作。

**为什么必须使用 Serena:**
- 🎯 **符号级精确分析**: 理解 Go 代码结构(函数、类型、接口)
- 🚀 **Token 高效**: 避免读取整个文件,只获取需要的符号
- 🔍 **智能搜索**: 通过符号路径精确定位代码
- ✏️ **安全编辑**: 基于符号的精确替换,避免误改

**强制使用场景:**

1. **代码探索阶段** - 使用 Serena 工具:
   ```
   - mcp__serena__get_symbols_overview    # 获取文件的符号概览
   - mcp__serena__find_symbol             # 查找特定符号(类/函数/方法)
   - mcp__serena__find_referencing_symbols # 查找符号引用
   - mcp__serena__search_for_pattern      # 灵活的模式搜索
   ```

2. **代码编辑阶段** - 使用 Serena 工具:
   ```
   - mcp__serena__replace_symbol_body     # 替换符号体(函数/方法)
   - mcp__serena__insert_after_symbol     # 在符号后插入
   - mcp__serena__insert_before_symbol    # 在符号前插入(如添加import)
   - mcp__serena__rename_symbol           # 重命名符号(全局)
   ```

3. **项目导航** - 使用 Serena 工具:
   ```
   - mcp__serena__list_dir                # 列出目录结构
   - mcp__serena__find_file               # 查找文件
   ```

**禁止的做法:**
- ❌ 直接使用 `Read` 读取整个 Go 源文件(除非文件很小 <100行)
- ❌ 使用 `Grep` 搜索符号名称(应该用 `find_symbol`)
- ❌ 使用 `Edit` 进行基于正则的替换(应该用 `replace_symbol_body`)
- ❌ 使用 `Glob` 查找 Go 文件(应该用 `find_file` 或 `list_dir`)

**正确的工作流程:**

```
步骤1: 使用 get_symbols_overview 获取文件概览
      ↓
步骤2: 使用 find_symbol 精确定位需要的符号(设置 include_body=true 仅在需要时)
      ↓
步骤3: 使用 replace_symbol_body 或其他编辑工具修改代码
      ↓
步骤4: 使用 find_referencing_symbols 检查影响范围
```

**示例:**

```go
// ❌ 错误方式 - 读取整个文件
Read("internal/proxy/transparent.go")  // 直接读取全部代码,浪费token

// ✅ 正确方式 - 使用符号概览
mcp__serena__get_symbols_overview("internal/proxy/transparent.go")
// 然后只读取需要的符号:
mcp__serena__find_symbol(
  name_path="(*TransparentProxy).ProxyRequest",
  relative_path="internal/proxy/transparent.go",
  include_body=true
)
```

**例外情况(可以不用 Serena):**
- 非代码文件(markdown、yaml、配置文件等)
- 查看测试输出或日志
- 执行 shell 命令

**Memory 系统:**
- `read_memory`: 读取项目记忆(如 `transparent_proxy_principles`)
- `write_memory`: 保存重要发现供未来使用
- 开始工作前先调用 `check_onboarding_performed`

## 关键架构原则

### 透明代理合规性(RFC 7230)

**严格禁止:**
- ❌ 修改请求或响应的内容(JSON解析/修改字段)
- ❌ 添加业务逻辑相关的请求/响应头
- ❌ 设置额外的超时限制(由客户端/服务端控制)
- ❌ 缓存完整响应体再转发

**必须遵守:**
- ✅ 原样转发请求/响应头(除 hop-by-hop 头部)
- ✅ 使用流式传输(边收边发,固定32KB缓冲区)
- ✅ 保持原始状态码和Content-Type
- ✅ 仅记录统计信息,不影响转发

**Hop-by-hop 头部(必须过滤):**
Connection, Keep-Alive, Proxy-Authenticate, Proxy-Authorization, TE, Trailer, Transfer-Encoding, Upgrade

### 模块化架构

```
internal/
├── proxy/        # 核心代理逻辑 - 异步转发,流式传输
├── stats/        # 统计收集器 - 原子操作,读写锁保护
├── storage/      # Redis映射管理 - 缓存+后台重载机制
└── admin/        # Web管理界面 - Token认证,CRUD操作
```

**关键设计:**
- `AsyncProxyContext`: 异步代理上下文,支持并发、流式传输、原子头部控制
- `MappingManager`: 本地缓存(5分钟TTL)+后台自动重载,避免每次请求查Redis
- `Collector`: 原子计数器+读写锁,支持高并发统计

## 常用开发命令

### 开发运行

```bash
# 本地运行(需要先配置.env文件)
go run main.go

# 指定端口运行
PORT=9000 go run main.go

# 下载依赖
go mod download

# 整理依赖
go mod tidy
```

### 测试

```bash
# 运行所有测试
go test ./...

# 运行特定模块测试
go test ./internal/proxy/

# 测试覆盖率
go test -cover ./...

# 详细覆盖率报告
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### 代码质量

```bash
# 格式化代码(必须在提交前运行)
go fmt ./...

# 静态分析
go vet ./...

# 构建检查
go build -o apiproxy main.go
```

### Docker 部署

```bash
# Docker Compose(推荐)
cd deployments/docker
docker-compose up -d

# 查看日志
docker-compose logs -f api-proxy

# 停止服务
docker-compose down
```

### 功能测试

```bash
# 测试流式响应(AI API)
curl -X POST "http://localhost:8000/openai/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_KEY" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}],"stream":true}' \
  --no-buffer

# 测试并发性能
for i in {1..20}; do curl "http://localhost:8000/stats" -o /dev/null -s & done; wait

# 查看统计数据
curl "http://localhost:8000/stats" | jq .

# 测试管理API
curl -H "Authorization: Bearer YOUR_ADMIN_TOKEN" \
  http://localhost:8000/api/mappings
```

## 环境配置

**必需环境变量:**
```bash
# Redis连接(URL格式)
API_PROXY_REDIS_URL=redis://:password@host:port/db

# 管理界面Token
ADMIN_TOKEN=your_secure_admin_token

# 可选:服务端口(默认8080)
PORT=8000
```

**配置方式:**
1. 复制 `.env.example` 为 `.env`
2. 编辑 `.env` 设置安全密码和Token
3. 程序启动时自动加载 `.env` 文件

## 代码风格约定

### 命名规范

- **包名**: 小写单词 (proxy, stats, storage)
- **公开类型/函数**: PascalCase (AsyncProxyContext, NewHandler)
- **私有函数/变量**: camelCase (handleAsyncAPIRequest, httpClient)
- **函数前缀**: `apc_` 表示异步代理上下文相关

### 并发安全

**必须使用:**
- `sync/atomic`: 简单计数器(requestCount, errorCount)
- `sync.RWMutex`: 保护共享数据结构(Stats, PerformanceMetrics)
- 读多写少场景使用 `RLock/RUnlock`
- `atomic.Bool`: 原子布尔状态(headersSent)

**示例:**
```go
// 原子计数器
atomic.AddInt64(&requestCount, 1)

// 读写锁
s.mu.RLock()
defer s.mu.RUnlock()
// 读取操作
```

### 资源管理

- 使用 `defer` 确保资源释放: `defer resp.Body.Close()`
- 使用 `context.Context` 控制超时和取消
- 避免 goroutine 泄漏,确保有退出机制

### 错误处理

```go
// 立即检查错误
if err != nil {
    log.Printf("Error: %v", err)
    return err
}

// 记录但不中断透明转发
if err := stats.RecordRequest(prefix); err != nil {
    log.Printf("Failed to record stats: %v", err)
    // 继续处理请求
}
```

## 性能优化原则

1. **固定缓冲区**: 使用32KB缓冲区,避免大内存分配
2. **流式传输**: 边收边发,内存使用恒定(5-15MB)
3. **连接复用**: HTTP连接池(MaxIdleConns=100)
4. **原子操作优于锁**: 简单计数使用atomic包
5. **采样更新**: 性能指标10%采样更新,避免每次计算

## 添加新功能检查清单

在添加新功能前,必须确认:

- [ ] 是否遵守透明代理原则(不修改请求/响应内容)?
- [ ] 是否正确处理并发安全(atomic/RWMutex)?
- [ ] 是否有资源泄漏风险(defer关闭,context取消)?
- [ ] 是否添加了单元测试?
- [ ] 是否运行了 `go fmt` 和 `go vet`?
- [ ] 是否更新了相关文档(如果需要)?

## 项目路由说明

- `/` 或 `/index.html`: 统计面板
- `/stats`: JSON统计数据
- `/admin`: API映射管理界面
- `/api/mappings`: 管理API(需Token认证)
- `/<prefix>/*`: 透明代理转发

## 关键文件说明

- `internal/proxy/transparent.go`: 透明代理核心实现,流式转发
- `internal/storage/redis.go`: Redis映射管理(缓存+RWMutex)
- `internal/stats/collector.go`: 无锁统计收集器(channel+批处理)
- `internal/admin/handler.go`: Web管理界面
- `main.go`: 入口文件,路由设置,服务启动
- `deployments/docker/`: Docker相关配置

## 开发注意事项

1. **透明代理是第一原则**: 任何修改请求/响应内容的功能都违反项目核心原则
2. **并发安全**: 所有共享状态必须有保护机制
3. **内存效率**: 避免缓存大对象,使用流式处理
4. **测试覆盖**: 新功能必须有单元测试
5. **日志规范**: 使用 `log.Printf` 记录关键事件,避免过度日志
