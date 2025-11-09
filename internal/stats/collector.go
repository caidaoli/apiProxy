package stats

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Collector 简化的统计收集器
// KISS原则：使用atomic+RWMutex，去除过度优化的channel和批处理
type Collector struct {
	// 原子计数器(全局统计)
	requestCount int64
	errorCount   int64

	// 响应时间统计(原子操作)
	responseTimeSum   int64 // 纳秒
	responseTimeCount int64

	// 端点统计数据(读写锁保护)
	mu        sync.RWMutex
	endpoints map[string]*EndpointStats

	// 时间序列数据(环形缓冲区,最多保留10000条记录)
	requestsMu       sync.RWMutex
	requests         []RequestRecord // 请求时间戳记录
	maxRequestsCache int             // 最大缓存数量

	// 性能指标缓存
	lastMetricsUpdate time.Time
	cachedMetrics     *PerformanceMetrics

	// Redis客户端(可选持久化)
	redisClient *redis.Client
}

// RequestRecord 请求记录(用于时间序列图表)
type RequestRecord struct {
	Timestamp int64  `json:"timestamp"` // Unix时间戳(秒)
	Endpoint  string `json:"endpoint"`  // 端点路径
}

// PerformanceMetrics 性能指标
type PerformanceMetrics struct {
	RequestsPerSec    float64 `json:"requests_per_sec"`     // 每秒请求数
	AvgResponseTimeMs int64   `json:"avg_response_time_ms"` // 平均响应时间(毫秒)
	ErrorRate         float64 `json:"error_rate"`           // 错误率(%)
	MemoryUsageMB     float64 `json:"memory_usage_mb"`      // 内存使用(MB)
	GoroutineCount    int     `json:"goroutine_count"`      // 协程数量
}

// EndpointStats 端点统计数据
type EndpointStats struct {
	Count       int64 `json:"count"`
	ErrorCount  int64 `json:"error_count"`
	LastRequest int64 `json:"last_request"`
}

// NewCollector 创建统计收集器
func NewCollector(redisClient *redis.Client) *Collector {
	return &Collector{
		endpoints:        make(map[string]*EndpointStats),
		requests:         make([]RequestRecord, 0, 10000),
		maxRequestsCache: 10000, // 最多缓存10000条记录(约占用200KB内存)
		redisClient:      redisClient,
	}
}

// RecordRequest 记录请求
// 简化版本：直接使用锁，性能足够好
func (c *Collector) RecordRequest(endpoint string) {
	atomic.AddInt64(&c.requestCount, 1)

	now := time.Now()
	timestamp := now.Unix()

	c.mu.Lock()
	stats := c.endpoints[endpoint]
	if stats == nil {
		stats = &EndpointStats{}
		c.endpoints[endpoint] = stats
	}
	stats.Count++
	stats.LastRequest = timestamp
	c.mu.Unlock()

	// 记录时间序列数据(环形缓冲区)
	c.requestsMu.Lock()
	if len(c.requests) >= c.maxRequestsCache {
		// 删除最旧的20%数据,避免频繁扩容
		c.requests = c.requests[c.maxRequestsCache/5:]
	}
	c.requests = append(c.requests, RequestRecord{
		Timestamp: timestamp,
		Endpoint:  endpoint,
	})
	c.requestsMu.Unlock()
}

// RecordError 记录错误
func (c *Collector) RecordError(endpoint string) {
	atomic.AddInt64(&c.errorCount, 1)

	c.mu.Lock()
	stats := c.endpoints[endpoint]
	if stats == nil {
		stats = &EndpointStats{}
		c.endpoints[endpoint] = stats
	}
	stats.ErrorCount++
	c.mu.Unlock()
}

// UpdateResponseMetrics 更新响应时间统计
func (c *Collector) UpdateResponseMetrics(duration time.Duration) {
	atomic.AddInt64(&c.responseTimeSum, int64(duration))
	atomic.AddInt64(&c.responseTimeCount, 1)
}

// GetStats 获取统计快照（读锁，快速）
func (c *Collector) GetStats() map[string]*EndpointStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 深拷贝，避免外部修改
	result := make(map[string]*EndpointStats, len(c.endpoints))
	for k, v := range c.endpoints {
		result[k] = &EndpointStats{
			Count:       v.Count,
			ErrorCount:  v.ErrorCount,
			LastRequest: v.LastRequest,
		}
	}

	return result
}

// GetRequests 获取请求时间序列数据(用于图表)
func (c *Collector) GetRequests() []RequestRecord {
	c.requestsMu.RLock()
	defer c.requestsMu.RUnlock()

	// 深拷贝,避免外部修改
	result := make([]RequestRecord, len(c.requests))
	copy(result, c.requests)
	return result
}

// GetPerformanceMetrics 获取性能指标(缓存5秒)
func (c *Collector) GetPerformanceMetrics() *PerformanceMetrics {
	now := time.Now()

	// 如果缓存未过期,直接返回
	if c.cachedMetrics != nil && now.Sub(c.lastMetricsUpdate) < 5*time.Second {
		return c.cachedMetrics
	}

	// 计算性能指标
	totalRequests := atomic.LoadInt64(&c.requestCount)
	totalErrors := atomic.LoadInt64(&c.errorCount)
	responseTimeSum := atomic.LoadInt64(&c.responseTimeSum)
	responseTimeCount := atomic.LoadInt64(&c.responseTimeCount)

	// 计算QPS(基于最近60秒的请求)
	var qps float64
	c.requestsMu.RLock()
	sixtySecondsAgo := now.Unix() - 60
	recentCount := 0
	for i := len(c.requests) - 1; i >= 0; i-- {
		if c.requests[i].Timestamp >= sixtySecondsAgo {
			recentCount++
		} else {
			break
		}
	}
	c.requestsMu.RUnlock()
	qps = float64(recentCount) / 60.0

	// 计算平均响应时间(毫秒)
	var avgResponseMs int64
	if responseTimeCount > 0 {
		avgResponseMs = (responseTimeSum / responseTimeCount) / 1_000_000 // 纳秒转毫秒
	}

	// 计算错误率(%)
	var errorRate float64
	if totalRequests > 0 {
		errorRate = (float64(totalErrors) / float64(totalRequests)) * 100
	}

	// 获取内存和协程信息
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memoryMB := float64(memStats.Alloc) / 1024 / 1024
	goroutines := runtime.NumGoroutine()

	metrics := &PerformanceMetrics{
		RequestsPerSec:    qps,
		AvgResponseTimeMs: avgResponseMs,
		ErrorRate:         errorRate,
		MemoryUsageMB:     memoryMB,
		GoroutineCount:    goroutines,
	}

	// 更新缓存
	c.cachedMetrics = metrics
	c.lastMetricsUpdate = now

	return metrics
}

// GetRequestCount 获取总请求数
func (c *Collector) GetRequestCount() int64 {
	return atomic.LoadInt64(&c.requestCount)
}

// GetErrorCount 获取总错误数
func (c *Collector) GetErrorCount() int64 {
	return atomic.LoadInt64(&c.errorCount)
}

// GetAverageResponseTime 获取平均响应时间
func (c *Collector) GetAverageResponseTime() time.Duration {
	sum := atomic.LoadInt64(&c.responseTimeSum)
	count := atomic.LoadInt64(&c.responseTimeCount)

	if count == 0 {
		return 0
	}

	return time.Duration(sum / count)
}

// SaveToRedis 保存统计数据到Redis（可选）
func (c *Collector) SaveToRedis(ctx context.Context) error {
	if c.redisClient == nil {
		return nil
	}

	// 保存全局计数器
	pipe := c.redisClient.Pipeline()
	pipe.Set(ctx, "stats:request_count", c.GetRequestCount(), 0)
	pipe.Set(ctx, "stats:error_count", c.GetErrorCount(), 0)

	// 保存端点统计
	stats := c.GetStats()
	for endpoint, stat := range stats {
		key := "stats:endpoint:" + endpoint
		pipe.HSet(ctx, key, map[string]interface{}{
			"count":        stat.Count,
			"error_count":  stat.ErrorCount,
			"last_request": stat.LastRequest,
		})
	}

	_, err := pipe.Exec(ctx)
	return err
}

// LoadFromRedis 从Redis加载统计数据（可选）
func (c *Collector) LoadFromRedis(ctx context.Context) error {
	if c.redisClient == nil {
		return nil
	}

	// 加载全局计数器
	requestCount, _ := c.redisClient.Get(ctx, "stats:request_count").Int64()
	errorCount, _ := c.redisClient.Get(ctx, "stats:error_count").Int64()

	atomic.StoreInt64(&c.requestCount, requestCount)
	atomic.StoreInt64(&c.errorCount, errorCount)

	return nil
}

// Close 优雅关闭（简化版本,无需等待goroutine）
func (c *Collector) Close() error {
	// 简化版本不需要复杂的关闭逻辑
	return nil
}

// GetErrorCountPtr 返回错误计数指针（兼容旧接口）
func (c *Collector) GetErrorCountPtr() *int64 {
	return &c.errorCount
}

// GetRequestCountPtr 返回请求计数指针（兼容旧接口）
func (c *Collector) GetRequestCountPtr() *int64 {
	return &c.requestCount
}

// GetDroppedEvents 获取丢弃的事件数（简化版本，始终返回0）
// 保留此方法以兼容现有API，但简化版本不会丢弃事件
func (c *Collector) GetDroppedEvents() int64 {
	return 0
}
