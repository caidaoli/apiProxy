package middleware

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// MetricsCollector 统计收集器接口（依赖倒置原则）
type MetricsCollector interface {
	RecordRequest(endpoint string)
	RecordError(endpoint string)
	UpdateResponseMetrics(duration time.Duration)
}

// StatsMiddleware 统计中间件（可选功能）
// 使用中间件模式，将统计逻辑与代理逻辑分离
type StatsMiddleware struct {
	collector MetricsCollector
	enabled   bool
}

// NewStatsMiddleware 创建统计中间件
func NewStatsMiddleware(collector MetricsCollector) *StatsMiddleware {
	return &StatsMiddleware{
		collector: collector,
		enabled:   true,
	}
}

// Handler Gin中间件处理函数
func (m *StatsMiddleware) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.enabled {
			c.Next()
			return
		}

		start := time.Now()

		// 提取endpoint（第一段路径）
		endpoint := extractEndpoint(c.Request.URL.Path)

		// 记录请求（非阻塞）
		m.collector.RecordRequest(endpoint)

		// 继续处理请求
		c.Next()

		// 记录响应时间
		duration := time.Since(start)
		m.collector.UpdateResponseMetrics(duration)

		// 如果是错误响应，记录错误
		if c.Writer.Status() >= 400 {
			m.collector.RecordError(endpoint)
		}
	}
}

// Enable 启用统计
func (m *StatsMiddleware) Enable() {
	m.enabled = true
}

// Disable 禁用统计
func (m *StatsMiddleware) Disable() {
	m.enabled = false
}

// extractEndpoint 从路径提取endpoint
// 例如：/openai/v1/chat -> openai
func extractEndpoint(path string) string {
	if len(path) <= 1 {
		return "unknown"
	}

	// 去掉开头的 /
	path = path[1:]

	// 找到第一个 /
	if idx := strings.Index(path, "/"); idx > 0 {
		return path[:idx]
	}

	return path
}
