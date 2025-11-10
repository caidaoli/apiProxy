# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

高性能 API 透明代理服务器,严格遵循 RFC 7230 标准。核心设计:流式转发(32KB缓冲区)、动态配置(Redis+本地缓存)、高并发(atomic+RWMutex)。

**技术栈:** Go 1.25+, Gin 1.11, Redis 7.4+, go-redis v9.16

**核心模块:**
- `internal/proxy/transparent.go` - RFC 7230 合规的流式转发引擎(32KB固定缓冲区,HTTP连接池)
- `internal/storage/redis.go` - MappingManager(本地缓存30s TTL + 后台自动重载10s周期 + Redis Pub/Sub实时同步)
- `internal/stats/collector.go` - Collector(atomic计数器 + RWMutex + 10%采样更新性能指标)
- `internal/middleware/stats.go` - 统计收集中间件(非阻塞)
- `internal/admin/handler.go` - Web管理界面(Token认证,CRUD操作)
- `main.go` - 服务入口和路由配置

## 工具使用规范

### ⚠️ 强制要求:优先使用 Serena MCP

在此代码库中工作时,**必须优先使用 Serena MCP 工具**进行代码分析、搜索和编辑操作。

**核心工作流程:**
```
1. check_onboarding_performed     # 检查是否已初始化(每次会话开始)
2. read_memory                    # 读取相关memory(基于任务需要)
3. get_symbols_overview           # 获取文件符号概览(避免读取完整文件)
4. find_symbol                    # 精确定位符号(include_body=true仅在必须时)
5. replace_symbol_body            # 修改代码(符号级精确替换)
6. find_referencing_symbols       # 检查影响范围(修改后验证)
```

**禁止操作(必须用Serena替代):**
- ❌ `Read` → `get_symbols_overview` + `find_symbol` (仅在必要时读取符号体)
- ❌ `Grep` → `find_symbol` (精确符号查找) 或 `search_for_pattern` (正则搜索)
- ❌ `Edit` → `replace_symbol_body` / `insert_after_symbol` / `insert_before_symbol`
- ❌ `Glob` → `find_file` (文件名模式匹配) 或 `list_dir` (目录遍历)

**例外:** 非代码文件(`.md`, `.yaml`, `.json`, `.env`)可直接使用 Read/Edit。

**可用Memory:**
- `project_overview` - 项目整体架构和设计理念
- `codebase_structure` - 目录结构和模块关系
- `transparent_proxy_principles` - 透明代理核心原则(RFC 7230)
- `code_style_conventions` - Go代码风格和并发安全规范
- `suggested_commands` - 常用开发命令
- `architecture_patterns` - 架构模式和最佳实践
- `multi_instance_sync` - 多实例同步机制

## 架构约束 (不可违反)

### 1. 透明代理合规性(RFC 7230) - 第一原则

**严格禁止:**
- ❌ 修改请求/响应内容(JSON解析/字段修改/body缓存)
- ❌ 添加业务逻辑请求/响应头
- ❌ 设置额外超时限制
- ❌ 缓存完整响应体

**必须遵守:**
- ✅ 原样转发请求/响应头(除hop-by-hop头部: Connection, Keep-Alive, Proxy-Authenticate, Proxy-Authorization, TE, Trailer, Transfer-Encoding, Upgrade)
- ✅ 流式传输(边收边发,32KB固定缓冲区,`io.CopyBuffer`)
- ✅ 保持原始状态码和Content-Type
- ✅ 仅记录统计,失败不影响转发

### 2. 并发安全规范

**简单计数器:**
```go
atomic.AddInt64(&requestCount, 1)  // 使用 sync/atomic
atomic.LoadInt64(&errorCount)
```

**共享数据结构:**
```go
type Collector struct {
    mu    sync.RWMutex  // 读多写少场景
    stats map[string]*Stats
}

// 读操作
c.mu.RLock()
defer c.mu.RUnlock()

// 写操作
c.mu.Lock()
defer c.mu.Unlock()
```

**原子布尔状态:**
```go
var headersSent atomic.Bool
headersSent.Store(true)
if headersSent.Load() { ... }
```

### 3. 资源管理规范

**必须使用 defer:**
```go
defer resp.Body.Close()          // HTTP响应
defer file.Close()                // 文件
defer cancel()                    // context取消
```

**避免goroutine泄漏:**
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()  // 确保goroutine退出机制
```

## 开发命令

### 测试
```bash
go test ./...                                          # 运行所有测试
go test ./internal/proxy/ -v                           # 单个包+详细输出
go test ./internal/proxy/ -run TestProxyRequest        # 单个测试函数
go test -cover ./...                                   # 覆盖率摘要
go test -coverprofile=coverage.out ./...               # 生成覆盖率报告
go tool cover -html=coverage.out                       # 浏览器查看覆盖率
go test -bench=. -benchmem ./internal/proxy/           # 基准测试
go test -bench=BenchmarkCollector -benchmem ./internal/stats/
```

### 代码质量(提交前必须运行)
```bash
go fmt ./...                    # 格式化代码
go vet ./...                    # 静态分析
go build -o apiproxy main.go    # 构建检查
```

### 本地运行
```bash
go run main.go                  # 使用.env配置运行
PORT=9000 go run main.go        # 指定端口
go mod tidy                     # 整理依赖
```

## 代码风格

### 命名
- 包名: `proxy`, `stats`, `storage` (小写单词)
- 公开符号: `TransparentProxy`, `NewHandler` (PascalCase)
- 私有符号: `copyHeaders`, `httpClient` (camelCase)

### 错误处理
```go
// 立即检查错误
if err != nil {
    log.Printf("Error: %v", err)
    return err
}

// 统计失败不中断转发
if err := stats.RecordRequest(prefix); err != nil {
    log.Printf("Failed to record stats: %v", err)
    // 继续处理请求
}
```
## 代码规范

### Go语言现代化要求
- 使用`any`替代`interface{}`(Go 1.18+)
- 充分利用泛型和类型推导
- 遵循**KISS原则**,优先简洁可读的代码
- 遵循**DRY原则**,消除重复代码
- 遵循**SOLID原则**,单一职责、依赖抽象
- 强制执行`go fmt`和`go vet`

### 错误处理
- 使用标准Go错误处理(`error`接口和`errors`包)
- 支持错误链(Go 1.13+ `errors.Unwrap`)
- **Fail-Fast策略**: 配置错误立即退出,避免生产风险
## 新功能检查清单

修改代码前必须确认:

- [ ] 是否遵守透明代理原则(不修改请求/响应,仅流式转发)?
- [ ] 是否正确处理并发安全(atomic计数/RWMutex保护共享数据)?
- [ ] 是否有资源泄漏风险(defer关闭HTTP/文件/context)?
- [ ] 是否添加了单元测试(新功能)和基准测试(性能敏感代码)?
- [ ] 是否运行了 `go fmt ./...` 和 `go vet ./...`?
- [ ] 是否验证了性能影响(与修改前基准测试对比)?

## 性能优化要点

1. **固定缓冲区**: 32KB缓冲区,避免大内存分配
2. **流式传输**: `io.CopyBuffer`,内存恒定(5-15MB)
3. **连接复用**: HTTP连接池(MaxIdleConns=100, MaxConnsPerHost=200)
4. **原子操作优于锁**: 简单计数用`sync/atomic`,复杂数据结构用`sync.RWMutex`
5. **采样更新**: 性能指标10%采样,避免每次计算

## 关键设计决策

1. **透明代理是第一原则** - 违反RFC 7230的功能不可接受
2. **并发安全强制要求** - 所有共享状态必须有保护机制
3. **内存效率优先** - 避免缓存大对象,使用流式处理
4. **统计失败不影响转发** - 错误日志记录但不中断代理
5. **多实例依赖Redis Pub/Sub** - 确保Redis连接稳定性
