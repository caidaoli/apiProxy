package main

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
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

// 全局变量
var (
	stats             *Stats
	perfMetrics       *PerformanceMetrics
	requestCount      int64
	errorCount        int64
	responseTimeSum   int64
	responseTimeCount int64
	lastQPSUpdate     int64 // 上次QPS更新时间（Unix秒）
	lastRequestCount  int64 // 上次QPS更新时的请求总数
)

// initStats 初始化统计系统
func initStats() {
	stats = &Stats{
		Endpoints: make(map[string]*EndpointStats),
		timeWindow: &TimeWindow{
			counters: make(map[string]*atomic.Int64),
			requests: make([]Request, 0, 1000), // 预分配容量
		},
		lastUpdate: time.Now(),
	}

	perfMetrics = &PerformanceMetrics{
		LastUpdated: time.Now().UnixMilli(),
	}

	// 初始化所有端点的统计
	endpoints := []string{
		"/openai", "/gemini", "/claude", "/xai", "/cohere", "/fireworks",
		"/groq", "/huggingface", "/meta", "/novita", "/openrouter",
		"/portkey", "/sophnet", "/telegram", "/together", "/cerebras",
		"/discord", "/gnothink",
	}

	for _, endpoint := range endpoints {
		stats.Endpoints[endpoint] = &EndpointStats{}
		stats.timeWindow.counters[endpoint] = &atomic.Int64{}
	}

	// 启动统计更新协程
	go func() {
		ticker := time.NewTicker(3 * time.Second) // 优化：从5秒改为3秒
		defer ticker.Stop()
		for range ticker.C {
			stats.updateSummaryStats()
		}
	}()

	// 启动性能指标更新协程
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			updatePerformanceMetrics()
		}
	}()
}

// recordRequest 记录请求（异步执行）
func (s *Stats) recordRequest(endpoint string) {
	// 原子操作更新计数器
	if counter, exists := s.timeWindow.counters[endpoint]; exists {
		counter.Add(1)
	}

	// 异步添加详细记录
	go func() {
		s.timeWindow.mu.Lock()
		defer s.timeWindow.mu.Unlock()

		// 添加新请求记录
		s.timeWindow.requests = append(s.timeWindow.requests, Request{
			Endpoint:  endpoint,
			Timestamp: time.Now().Unix(),
		})

		// 异步清理旧记录
		s.cleanupOldRequests()
	}()
}

// cleanupOldRequests 清理旧请求记录
func (s *Stats) cleanupOldRequests() {
	now := time.Now()
	if now.Sub(s.timeWindow.lastCleanup) < 5*time.Minute {
		return // 5分钟内只清理一次
	}

	cutoff := now.Add(-30 * 24 * time.Hour).Unix() // 30天前
	var newRequests []Request

	for _, req := range s.timeWindow.requests {
		if req.Timestamp > cutoff {
			newRequests = append(newRequests, req)
		}
	}

	// 如果记录数超过500条，也进行清理
	if len(newRequests) > 500 {
		newRequests = newRequests[len(newRequests)-500:]
	}

	s.timeWindow.requests = newRequests
	s.timeWindow.lastCleanup = now
}

// updateSummaryStats 更新汇总统计
func (s *Stats) updateSummaryStats() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	weekAgo := now.AddDate(0, 0, -7).Unix()
	monthAgo := now.AddDate(0, -1, 0).Unix()

	// 重置时间窗口统计（Today, Week, Month），但保留Total
	for _, endpointStats := range s.Endpoints {
		atomic.StoreInt64(&endpointStats.Today, 0)
		atomic.StoreInt64(&endpointStats.Week, 0)
		atomic.StoreInt64(&endpointStats.Month, 0)
	}

	// 计算总请求数
	totalRequests := int64(0)
	for endpoint, counter := range s.timeWindow.counters {
		if endpointStats, exists := s.Endpoints[endpoint]; exists {
			total := counter.Load()
			atomic.StoreInt64(&endpointStats.Total, total)
			totalRequests += total
		}
	}

	// 更新全局总请求数
	s.Total = totalRequests

	// 统计时间窗口内的请求
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
func updatePerformanceMetrics() {
	perfMetrics.mu.Lock()
	defer perfMetrics.mu.Unlock()

	now := time.Now()

	// 获取内存使用情况
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	perfMetrics.MemoryUsageMB = math.Round(float64(m.Alloc)/1024/1024*100) / 100
	perfMetrics.GoroutineCount = runtime.NumGoroutine()
	perfMetrics.LastUpdated = now.UnixMilli()

	// 计算总请求数
	totalReqs := int64(0)
	stats.mu.RLock()
	for _, counter := range stats.timeWindow.counters {
		totalReqs += counter.Load()
	}
	stats.mu.RUnlock()

	// 计算QPS（每秒请求数）- 使用更高效的方法
	currentTime := now.Unix()
	lastUpdate := atomic.LoadInt64(&lastQPSUpdate)
	currentRequests := atomic.LoadInt64(&requestCount) // 使用全局请求计数

	if lastUpdate == 0 {
		// 第一次更新，初始化
		atomic.StoreInt64(&lastQPSUpdate, currentTime)
		atomic.StoreInt64(&lastRequestCount, currentRequests)
		perfMetrics.RequestsPerSec = 0.0
	} else {
		timeDiff := currentTime - lastUpdate
		if timeDiff > 0 {
			// 计算请求增量
			lastReqs := atomic.LoadInt64(&lastRequestCount)
			requestDiff := currentRequests - lastReqs

			// 计算QPS
			qps := float64(requestDiff) / float64(timeDiff)

			// 使用指数移动平均平滑QPS值
			if perfMetrics.RequestsPerSec == 0 {
				perfMetrics.RequestsPerSec = qps
			} else {
				// 0.3的权重给新值，0.7的权重给旧值，实现平滑过渡
				perfMetrics.RequestsPerSec = 0.3*qps + 0.7*perfMetrics.RequestsPerSec
			}

			// 保留2位小数
			perfMetrics.RequestsPerSec = math.Round(perfMetrics.RequestsPerSec*100) / 100

			// 更新记录
			atomic.StoreInt64(&lastQPSUpdate, currentTime)
			atomic.StoreInt64(&lastRequestCount, currentRequests)
		}
	}

	// 计算错误率（保留2位小数）
	totalErrors := atomic.LoadInt64(&errorCount)
	if totalReqs > 0 {
		errorRate := float64(totalErrors) / float64(totalReqs) * 100
		// 使用math.Round保证精确的2位小数
		perfMetrics.ErrorRate = math.Round(errorRate*100) / 100
	}

	// 计算平均响应时间
	totalResponseTime := atomic.LoadInt64(&responseTimeSum)
	responseCount := atomic.LoadInt64(&responseTimeCount)
	if responseCount > 0 {
		perfMetrics.AvgResponseTime = totalResponseTime / responseCount
		// 定期重置响应时间统计以保持数据新鲜
		if responseCount > 1000 {
			atomic.StoreInt64(&responseTimeSum, 0)
			atomic.StoreInt64(&responseTimeCount, 0)
		}
	}
}

// handleStats 处理统计API请求
func handleStats(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")

	if c.Request.Method == "OPTIONS" {
		c.Status(204)
		return
	}

	snapshot := stats.getStatsSnapshot()

	// 获取请求时间戳数据用于图表
	stats.timeWindow.mu.RLock()
	requests := make([]Request, len(stats.timeWindow.requests))
	copy(requests, stats.timeWindow.requests)
	stats.timeWindow.mu.RUnlock()

	// 添加性能指标
	perfMetrics.mu.RLock()
	response := gin.H{
		"total":     snapshot.Total,
		"endpoints": snapshot.Endpoints,
		"requests":  requests, // 添加请求时间戳数据
		"performance": gin.H{
			"requests_per_sec":     perfMetrics.RequestsPerSec,
			"avg_response_time_ms": perfMetrics.AvgResponseTime,
			"error_rate":           perfMetrics.ErrorRate,
			"memory_usage_mb":      perfMetrics.MemoryUsageMB,
			"goroutine_count":      perfMetrics.GoroutineCount,
			"last_updated":         perfMetrics.LastUpdated,
		},
	}
	perfMetrics.mu.RUnlock()

	c.JSON(200, response)
}
