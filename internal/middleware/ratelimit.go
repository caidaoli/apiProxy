package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimiter 全局速率限制器（简单实现）
type RateLimiter struct {
	limiter *rate.Limiter
}

// NewRateLimiter 创建速率限制器
// requestsPerSecond: 每秒允许的请求数
func NewRateLimiter(requestsPerSecond int) *RateLimiter {
	return &RateLimiter{
		limiter: rate.NewLimiter(rate.Limit(requestsPerSecond), requestsPerSecond*2),
	}
}

// Middleware 返回速率限制中间件
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rl.limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "Rate limit exceeded",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
