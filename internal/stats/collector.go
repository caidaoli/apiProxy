package stats

import (
	"context"
	"fmt"
	"log"
	"math"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// Redis存储相关常量
const (
	KeyStatsCounters       = "api_proxy:stats:counters"
	KeyStatsEndpointPrefix = "api_proxy:stats:endpoints:"
)

// EndpointStats 端点统计信息
type EndpointStats struct {
	Total int64 `json:"total"`
	Today int64 `json:"today"`
	Week  int64 `json:"week"`
	Month int64 `json:"month"`
}

// Request 请求记录
type Request struct {
	Endpoint  string `json:"endpoint"`
	Timestamp int64  `json:"timestamp"`
}

// TimeWindow 时间窗口统计
type TimeWindow struct {
	mu          sync.RWMutex
	counters    map[string]*atomic.Int64
	requests    []Request
	lastCleanup time.Time
}

// Stats 统计管理器
type Stats struct {
	mu         sync.RWMutex
	Total      int64                     `json:"total"`
	Endpoints  map[string]*EndpointStats `json:"endpoints"`
	timeWindow *TimeWindow
	lastUpdate time.Time
}

// PerformanceMetrics 性能指标
type PerformanceMetrics struct {
	mu              sync.RWMutex
	RequestsPerSec  float64 `json:"requests_per_sec"`
	AvgResponseTime int64   `json:"avg_response_time_ms"`
	ErrorRate       float64 `json:"error_rate"`
	MemoryUsageMB   float64 `json:"memory_usage_mb"`
	GoroutineCount  int     `json:"goroutine_count"`
	LastUpdated     int64   `json:"last_updated"`
}

// Collector 统计收集器
type Collector struct {
	stats             *Stats
	perfMetrics       *PerformanceMetrics
	requestCount      int64
	errorCount        int64
	responseTimeSum   int64
	responseTimeCount int64
	lastQPSUpdate     int64
	lastRequestCount  int64
	redisClient       *redis.Client // Redis客户端用于持久化
}

// NewCollector 创建统计收集器
func NewCollector(redisClient *redis.Client) *Collector {
	c := &Collector{
		stats: &Stats{
			Endpoints: make(map[string]*EndpointStats),
			timeWindow: &TimeWindow{
				counters: make(map[string]*atomic.Int64),
				requests: make([]Request, 0, 1000),
			},
			lastUpdate: time.Now(),
		},
		perfMetrics: &PerformanceMetrics{
			LastUpdated: time.Now().UnixMilli(),
		},
		redisClient: redisClient,
	}

	// 从Redis加载历史统计数据
	if redisClient != nil {
		if err := c.LoadFromRedis(context.Background()); err != nil {
			log.Printf("⚠️  Failed to load stats from Redis: %v (starting with fresh stats)", err)
		} else {
			log.Println("✅ 统计数据已从Redis恢复")
		}
	}

	// 启动统计更新协程
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.stats.updateSummaryStats()
		}
	}()

	// 启动性能指标更新协程
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.updatePerformanceMetrics()
		}
	}()

	// 启动定时保存到Redis的协程
	if redisClient != nil {
		go func() {
			ticker := time.NewTicker(1 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				if err := c.SaveToRedis(context.Background()); err != nil {
					log.Printf("❌ Failed to save stats to Redis: %v", err)
				} else {
					log.Println("💾 统计数据已保存到Redis")
				}
			}
		}()
		log.Println("🔄 统计数据自动保存已启用 (每1分钟)")
	}

	log.Println("📊 统计收集器已初始化")
	return c
}

// InitializeEndpoints 初始化端点统计
func (c *Collector) InitializeEndpoints(endpoints []string) {
	c.stats.mu.Lock()
	defer c.stats.mu.Unlock()

	for _, endpoint := range endpoints {
		if _, exists := c.stats.Endpoints[endpoint]; !exists {
			c.stats.Endpoints[endpoint] = &EndpointStats{}
			c.stats.timeWindow.counters[endpoint] = &atomic.Int64{}
		}
	}
	log.Printf("📊 已初始化 %d 个端点的统计", len(endpoints))
}

// RecordRequest 记录请求
func (c *Collector) RecordRequest(endpoint string) {
	// 确保端点存在
	c.stats.mu.RLock()
	counter, exists := c.stats.timeWindow.counters[endpoint]
	c.stats.mu.RUnlock()

	if !exists {
		// 动态添加新端点
		c.stats.mu.Lock()
		if _, exists := c.stats.timeWindow.counters[endpoint]; !exists {
			c.stats.Endpoints[endpoint] = &EndpointStats{}
			c.stats.timeWindow.counters[endpoint] = &atomic.Int64{}
			counter = c.stats.timeWindow.counters[endpoint]
		}
		c.stats.mu.Unlock()
	}

	// 原子操作更新计数器
	if counter != nil {
		counter.Add(1)
	}

	// 异步添加详细记录
	go func() {
		c.stats.timeWindow.mu.Lock()
		defer c.stats.timeWindow.mu.Unlock()

		c.stats.timeWindow.requests = append(c.stats.timeWindow.requests, Request{
			Endpoint:  endpoint,
			Timestamp: time.Now().Unix(),
		})

		c.cleanupOldRequests()
	}()
}

// cleanupOldRequests 清理旧请求记录
func (c *Collector) cleanupOldRequests() {
	now := time.Now()
	if now.Sub(c.stats.timeWindow.lastCleanup) < 5*time.Minute {
		return
	}

	cutoff := now.Add(-30 * 24 * time.Hour).Unix()
	var newRequests []Request

	for _, req := range c.stats.timeWindow.requests {
		if req.Timestamp > cutoff {
			newRequests = append(newRequests, req)
		}
	}

	if len(newRequests) > 500 {
		newRequests = newRequests[len(newRequests)-500:]
	}

	c.stats.timeWindow.requests = newRequests
	c.stats.timeWindow.lastCleanup = now
}

// updateSummaryStats 更新汇总统计
func (s *Stats) updateSummaryStats() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	weekAgo := now.AddDate(0, 0, -7).Unix()
	monthAgo := now.AddDate(0, -1, 0).Unix()

	for _, endpointStats := range s.Endpoints {
		atomic.StoreInt64(&endpointStats.Today, 0)
		atomic.StoreInt64(&endpointStats.Week, 0)
		atomic.StoreInt64(&endpointStats.Month, 0)
	}

	totalRequests := int64(0)
	for endpoint, counter := range s.timeWindow.counters {
		if endpointStats, exists := s.Endpoints[endpoint]; exists {
			total := counter.Load()
			atomic.StoreInt64(&endpointStats.Total, total)
			totalRequests += total
		}
	}

	s.Total = totalRequests

	s.timeWindow.mu.RLock()
	for _, req := range s.timeWindow.requests {
		if endpointStats, exists := s.Endpoints[req.Endpoint]; exists {
			if req.Timestamp >= today {
				atomic.AddInt64(&endpointStats.Today, 1)
			}
			if req.Timestamp >= weekAgo {
				atomic.AddInt64(&endpointStats.Week, 1)
			}
			if req.Timestamp >= monthAgo {
				atomic.AddInt64(&endpointStats.Month, 1)
			}
		}
	}
	s.timeWindow.mu.RUnlock()

	s.lastUpdate = now
}

// getStatsSnapshot 获取统计快照
func (s *Stats) getStatsSnapshot() *Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := &Stats{
		Total:     s.Total,
		Endpoints: make(map[string]*EndpointStats),
	}

	for endpoint, endpointStats := range s.Endpoints {
		snapshot.Endpoints[endpoint] = &EndpointStats{
			Total: atomic.LoadInt64(&endpointStats.Total),
			Today: atomic.LoadInt64(&endpointStats.Today),
			Week:  atomic.LoadInt64(&endpointStats.Week),
			Month: atomic.LoadInt64(&endpointStats.Month),
		}
	}

	return snapshot
}

// updatePerformanceMetrics 更新性能指标
func (c *Collector) updatePerformanceMetrics() {
	c.perfMetrics.mu.Lock()
	defer c.perfMetrics.mu.Unlock()

	now := time.Now()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	c.perfMetrics.MemoryUsageMB = math.Round(float64(m.Alloc)/1024/1024*100) / 100
	c.perfMetrics.GoroutineCount = runtime.NumGoroutine()
	c.perfMetrics.LastUpdated = now.UnixMilli()

	totalReqs := int64(0)
	c.stats.mu.RLock()
	for _, counter := range c.stats.timeWindow.counters {
		totalReqs += counter.Load()
	}
	c.stats.mu.RUnlock()

	// 计算QPS
	currentTime := now.Unix()
	lastUpdate := atomic.LoadInt64(&c.lastQPSUpdate)
	currentRequests := atomic.LoadInt64(&c.requestCount)

	if lastUpdate == 0 {
		atomic.StoreInt64(&c.lastQPSUpdate, currentTime)
		atomic.StoreInt64(&c.lastRequestCount, currentRequests)
		c.perfMetrics.RequestsPerSec = 0.0
	} else {
		timeDiff := currentTime - lastUpdate
		if timeDiff > 0 {
			lastReqs := atomic.LoadInt64(&c.lastRequestCount)
			requestDiff := currentRequests - lastReqs

			qps := float64(requestDiff) / float64(timeDiff)

			if c.perfMetrics.RequestsPerSec == 0 {
				c.perfMetrics.RequestsPerSec = qps
			} else {
				c.perfMetrics.RequestsPerSec = 0.3*qps + 0.7*c.perfMetrics.RequestsPerSec
			}

			c.perfMetrics.RequestsPerSec = math.Round(c.perfMetrics.RequestsPerSec*100) / 100

			atomic.StoreInt64(&c.lastQPSUpdate, currentTime)
			atomic.StoreInt64(&c.lastRequestCount, currentRequests)
		}
	}

	// 计算错误率
	totalErrors := atomic.LoadInt64(&c.errorCount)
	if totalReqs > 0 {
		errorRate := float64(totalErrors) / float64(totalReqs) * 100
		c.perfMetrics.ErrorRate = math.Round(errorRate*100) / 100
	}

	// 计算平均响应时间
	totalResponseTime := atomic.LoadInt64(&c.responseTimeSum)
	responseCount := atomic.LoadInt64(&c.responseTimeCount)
	if responseCount > 0 {
		c.perfMetrics.AvgResponseTime = totalResponseTime / responseCount
		if responseCount > 1000 {
			atomic.StoreInt64(&c.responseTimeSum, 0)
			atomic.StoreInt64(&c.responseTimeCount, 0)
		}
	}
}

// HandleStats 处理统计API请求
func (c *Collector) HandleStats(ctx *gin.Context) {
	ctx.Header("Access-Control-Allow-Origin", "*")
	ctx.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
	ctx.Header("Access-Control-Allow-Headers", "Content-Type")

	if ctx.Request.Method == "OPTIONS" {
		ctx.Status(204)
		return
	}

	snapshot := c.stats.getStatsSnapshot()

	c.stats.timeWindow.mu.RLock()
	requests := make([]Request, len(c.stats.timeWindow.requests))
	copy(requests, c.stats.timeWindow.requests)
	c.stats.timeWindow.mu.RUnlock()

	c.perfMetrics.mu.RLock()
	response := gin.H{
		"total":     snapshot.Total,
		"endpoints": snapshot.Endpoints,
		"requests":  requests,
		"performance": gin.H{
			"requests_per_sec":     c.perfMetrics.RequestsPerSec,
			"avg_response_time_ms": c.perfMetrics.AvgResponseTime,
			"error_rate":           c.perfMetrics.ErrorRate,
			"memory_usage_mb":      c.perfMetrics.MemoryUsageMB,
			"goroutine_count":      c.perfMetrics.GoroutineCount,
			"last_updated":         c.perfMetrics.LastUpdated,
		},
	}
	c.perfMetrics.mu.RUnlock()

	ctx.JSON(200, response)
}

// GetErrorCount 获取错误计数器指针
func (c *Collector) GetErrorCount() *int64 {
	return &c.errorCount
}

// GetRequestCount 获取请求计数器指针
func (c *Collector) GetRequestCount() *int64 {
	return &c.requestCount
}

// UpdateResponseMetrics 更新响应指标
func (c *Collector) UpdateResponseMetrics(responseTime int64) {
	atomic.AddInt64(&c.responseTimeSum, responseTime)
	atomic.AddInt64(&c.responseTimeCount, 1)
}

// SaveToRedis 保存统计数据到Redis
func (c *Collector) SaveToRedis(ctx context.Context) error {
	if c.redisClient == nil {
		return fmt.Errorf("redis client is not initialized")
	}

	pipe := c.redisClient.Pipeline()

	// 保存全局计数器
	counters := map[string]interface{}{
		"request_count":       atomic.LoadInt64(&c.requestCount),
		"error_count":         atomic.LoadInt64(&c.errorCount),
		"response_time_sum":   atomic.LoadInt64(&c.responseTimeSum),
		"response_time_count": atomic.LoadInt64(&c.responseTimeCount),
		"total":               c.stats.Total,
		"last_update":         time.Now().Unix(),
	}
	pipe.HSet(ctx, KeyStatsCounters, counters)

	// 保存每个endpoint的统计数据
	c.stats.mu.RLock()
	for prefix, stats := range c.stats.Endpoints {
		endpointKey := KeyStatsEndpointPrefix + prefix
		endpointData := map[string]interface{}{
			"total": stats.Total,
			"today": stats.Today,
			"week":  stats.Week,
			"month": stats.Month,
		}
		pipe.HSet(ctx, endpointKey, endpointData)
	}
	c.stats.mu.RUnlock()

	// 执行批量操作
	_, err := pipe.Exec(ctx)
	return err
}

// LoadFromRedis 从Redis加载统计数据
func (c *Collector) LoadFromRedis(ctx context.Context) error {
	if c.redisClient == nil {
		return fmt.Errorf("redis client is not initialized")
	}

	// 加载全局计数器
	counters, err := c.redisClient.HGetAll(ctx, KeyStatsCounters).Result()
	if err != nil {
		return fmt.Errorf("failed to load counters: %w", err)
	}

	// 如果没有数据，返回nil（不是错误）
	if len(counters) == 0 {
		return nil
	}

	// 恢复计数器
	if val, ok := counters["request_count"]; ok {
		if count, err := strconv.ParseInt(val, 10, 64); err == nil {
			atomic.StoreInt64(&c.requestCount, count)
		}
	}
	if val, ok := counters["error_count"]; ok {
		if count, err := strconv.ParseInt(val, 10, 64); err == nil {
			atomic.StoreInt64(&c.errorCount, count)
		}
	}
	if val, ok := counters["response_time_sum"]; ok {
		if sum, err := strconv.ParseInt(val, 10, 64); err == nil {
			atomic.StoreInt64(&c.responseTimeSum, sum)
		}
	}
	if val, ok := counters["response_time_count"]; ok {
		if count, err := strconv.ParseInt(val, 10, 64); err == nil {
			atomic.StoreInt64(&c.responseTimeCount, count)
		}
	}
	if val, ok := counters["total"]; ok {
		if total, err := strconv.ParseInt(val, 10, 64); err == nil {
			c.stats.Total = total
		}
	}

	// 加载所有endpoint统计数据
	keys, err := c.redisClient.Keys(ctx, KeyStatsEndpointPrefix+"*").Result()
	if err != nil {
		return fmt.Errorf("failed to get endpoint keys: %w", err)
	}

	c.stats.mu.Lock()
	defer c.stats.mu.Unlock()

	for _, key := range keys {
		prefix := key[len(KeyStatsEndpointPrefix):]
		data, err := c.redisClient.HGetAll(ctx, key).Result()
		if err != nil {
			log.Printf("⚠️  Failed to load stats for endpoint %s: %v", prefix, err)
			continue
		}

		stats := &EndpointStats{}
		if val, ok := data["total"]; ok {
			if total, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.Total = total
			}
		}
		if val, ok := data["today"]; ok {
			if today, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.Today = today
			}
		}
		if val, ok := data["week"]; ok {
			if week, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.Week = week
			}
		}
		if val, ok := data["month"]; ok {
			if month, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.Month = month
			}
		}

		c.stats.Endpoints[prefix] = stats
	}

	loadedEndpoints := len(keys)
	log.Printf("📊 从Redis恢复了 %d 个endpoint的统计数据", loadedEndpoints)

	return nil
}
