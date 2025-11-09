# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

高性能 API 透明代理服务器,严格遵循 RFC 7230 标准。核心设计:流式转发(32KB缓冲区)、动态配置(Redis+本地缓存)、高并发(atomic+RWMutex)。

**技术栈:** Go 1.25+, Gin 1.11, Redis 7.4+, go-redis v9.16

**项目结构:**
- `internal/proxy/` - 透明代理核心(流式转发、RFC 7230合规)
- `internal/storage/` - Redis映射管理(缓存+自动重载)
- `internal/stats/` - 统计收集器(原子操作+读写锁)
- `internal/middleware/` - 统计中间件
- `internal/admin/` - Web管理界面
- `main.go` - 服务入口和路由配置

## 工具使用规范

### ⚠️ 强制要求:优先使用 Serena MCP

在此代码库中工作时,**必须优先使用 Serena MCP 工具**进行代码分析、搜索和编辑操作。

**核心工作流程:**
```
1. get_symbols_overview     # 获取文件概览
2. find_symbol              # 精确定位符号 (include_body=true 仅在需要时)
3. replace_symbol_body      # 修改代码
4. find_referencing_symbols # 检查影响范围
```

**禁止使用 (用 Serena 替代):**
- ❌ `Read` → 使用 `get_symbols_overview` + `find_symbol`
- ❌ `Grep` → 使用 `find_symbol` 或 `search_for_pattern`
- ❌ `Edit` → 使用 `replace_symbol_body`
- ❌ `Glob` → 使用 `find_file` 或 `list_dir`

**例外:** 非代码文件(markdown、yaml、日志)可直接使用 Read/Edit。

**Memory 系统:**
- 开始工作前调用 `check_onboarding_performed`
- 使用 `read_memory`/`write_memory` 保存/读取项目知识

## 关键架构原则

### 透明代理合规性(RFC 7230)

这是项目的**第一原则**,任何违反透明代理的功能都不可接受。

**严格禁止:**
- ❌ 修改请求/响应内容(JSON解析/修改字段)
- ❌ 添加业务逻辑相关的请求/响应头
- ❌ 设置额外的超时限制
- ❌ 缓存完整响应体

**必须遵守:**
- ✅ 原样转发请求/响应头(除 hop-by-hop 头部)
- ✅ 流式传输(边收边发,32KB缓冲区)
- ✅ 保持原始状态码和Content-Type
- ✅ 仅记录统计信息,不影响转发

**Hop-by-hop 头部(必须过滤):**
Connection, Keep-Alive, Proxy-Authenticate, Proxy-Authorization, TE, Trailer, Transfer-Encoding, Upgrade

### 关键架构组件

1. **TransparentProxy** (internal/proxy/)
   - RFC 7230 合规的流式转发引擎,固定32KB缓冲区
   - HTTP连接池(MaxIdleConns=100, MaxConnsPerHost=200)

2. **MappingManager** (internal/storage/)
   - 本地缓存(5分钟TTL) + 后台自动重载
   - 缓存命中率 >99%,避免频繁查询Redis

3. **Collector** (internal/stats/)
   - 原子计数器(`sync/atomic`) + 读写锁(`sync.RWMutex`)
   - 10% 采样更新性能指标,避免每次计算

## 常用开发命令

### 测试

```bash
# 运行所有测试
go test ./...

# 运行单个包的测试
go test ./internal/proxy/ -v
go test ./internal/stats/ -v

# 运行单个测试函数
go test ./internal/proxy/ -run TestProxyRequest
go test ./internal/stats/ -run TestCollector_RecordRequest

# 测试覆盖率
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# 基准测试
go test -bench=. -benchmem ./internal/proxy/
go test -bench=BenchmarkCollector -benchmem ./internal/stats/
```

### 代码质量

```bash
# 格式化代码(提交前必须运行)
go fmt ./...

# 静态分析
go vet ./...

# 构建检查
go build -o apiproxy main.go
```

### 开发运行

```bash
# 本地运行(需先配置 .env)
go run main.go

# 指定端口运行
PORT=9000 go run main.go

# 依赖管理
go mod download      # 下载依赖
go mod tidy          # 整理依赖
```

## 代码风格约定

### 命名规范
- **包名**: 小写单词 (proxy, stats, storage)
- **公开类型/函数**: PascalCase (TransparentProxy, NewHandler)
- **私有函数/变量**: camelCase (copyHeaders, httpClient)

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

1. **固定缓冲区**: 32KB缓冲区,避免大内存分配
2. **流式传输**: 边收边发,内存恒定(5-15MB)
3. **连接复用**: HTTP连接池(MaxIdleConns=100)
4. **原子操作优于锁**: 简单计数用atomic包
5. **采样更新**: 性能指标10%采样,避免每次计算

## 添加新功能检查清单

开发新功能时必须确认:

- [ ] 是否遵守透明代理原则(不修改请求/响应)?
- [ ] 是否正确处理并发安全(atomic/RWMutex)?
- [ ] 是否有资源泄漏风险(defer关闭,context)?
- [ ] 是否添加了单元测试和基准测试?
- [ ] 是否运行了 `go fmt` 和 `go vet`?
- [ ] 是否验证了性能影响(基准测试对比)?

## 关键注意事项

1. **透明代理是第一原则** - 任何修改请求/响应内容的功能都不可接受
2. **并发安全** - 所有共享状态必须有保护机制(atomic/RWMutex)
3. **内存效率** - 避免缓存大对象,使用流式处理
4. **错误处理** - 统计/日志失败不应影响代理转发
5. **测试覆盖** - 新功能必须有单元测试,性能敏感代码需要基准测试
