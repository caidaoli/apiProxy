package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNewRateLimiter(t *testing.T) {
	limiter := NewRateLimiter(100)
	if limiter == nil {
		t.Fatal("NewRateLimiter returned nil")
	}
	if limiter.limiter == nil {
		t.Error("limiter not initialized")
	}
}

func TestRateLimiter_Middleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 创建一个非常低的限流器（1 req/s, burst 2）
	limiter := NewRateLimiter(1)

	router := gin.New()
	router.Use(limiter.Middleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	// 前两个请求应该通过（burst = 2）
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d should pass, got status %d", i+1, w.Code)
		}
	}

	// 第三个请求应该被限流
	req3 := httptest.NewRequest("GET", "/test", nil)
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("third request should be rate limited, got status %d", w3.Code)
	}
}
