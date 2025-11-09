package stats

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Collector 简化的统计收集器
// KISS原则：使用atomic+RWMutex，去除过度优化的channel和批处理
type Collector struct {
	// 原子计数器（全局统计）
	requestCount int64
	errorCount   int64

	// 响应时间统计（原子操作）
	responseTimeSum   int64 // 纳秒
	responseTimeCount int64

	// 端点统计数据（读写锁保护）
	mu        sync.RWMutex
	endpoints map[string]*EndpointStats

	// Redis客户端（可选持久化）
	redisClient *redis.Client
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
		endpoints:   make(map[string]*EndpointStats),
		redisClient: redisClient,
	}
}

// RecordRequest 记录请求
// 简化版本：直接使用锁，性能足够好
func (c *Collector) RecordRequest(endpoint string) {
	atomic.AddInt64(&c.requestCount, 1)

	c.mu.Lock()
	stats := c.endpoints[endpoint]
	if stats == nil {
		stats = &EndpointStats{}
		c.endpoints[endpoint] = stats
	}
	stats.Count++
	stats.LastRequest = time.Now().Unix()
	c.mu.Unlock()
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
