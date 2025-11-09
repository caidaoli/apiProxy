package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// MockCollector 用于测试的模拟统计收集器
type MockCollector struct {
	recordedRequests map[string]int
	recordedErrors   map[string]int
	responseTimes    []time.Duration
	requestCount     int64
	errorCount       int64
	droppedEvents    int64
}

func NewMockCollector() *MockCollector {
	return &MockCollector{
		recordedRequests: make(map[string]int),
		recordedErrors:   make(map[string]int),
		responseTimes:    make([]time.Duration, 0),
	}
}

func (m *MockCollector) RecordRequest(endpoint string) {
	m.recordedRequests[endpoint]++
	m.requestCount++
}

func (m *MockCollector) RecordError(endpoint string) {
	m.recordedErrors[endpoint]++
	m.errorCount++
}

func (m *MockCollector) UpdateResponseMetrics(duration time.Duration) {
	m.responseTimes = append(m.responseTimes, duration)
}

func (m *MockCollector) GetRequestCount() int64 {
	return m.requestCount
}

func (m *MockCollector) GetErrorCount() int64 {
	return m.errorCount
}

func (m *MockCollector) GetDroppedEvents() int64 {
	return m.droppedEvents
}

func (m *MockCollector) GetAverageResponseTime() time.Duration {
	if len(m.responseTimes) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range m.responseTimes {
		sum += d
	}
	return sum / time.Duration(len(m.responseTimes))
}

func TestNewStatsMiddleware(t *testing.T) {
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	if middleware == nil {
		t.Fatal("NewStatsMiddleware returned nil")
	}

	if middleware.collector == nil {
		t.Error("collector not set")
	}

	if !middleware.enabled {
		t.Error("middleware should be enabled by default")
	}
}

func TestStatsMiddleware_Handler_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	// 创建测试路由
	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// 发送请求
	req := httptest.NewRequest("GET", "/test/api", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 验证请求被记录
	if collector.requestCount != 1 {
		t.Errorf("expected 1 request, got %d", collector.requestCount)
	}

	// 验证endpoint被正确提取
	if collector.recordedRequests["test"] != 1 {
		t.Errorf("expected endpoint 'test' to be recorded, got %v", collector.recordedRequests)
	}

	// 验证响应时间被记录
	if len(collector.responseTimes) != 1 {
		t.Errorf("expected 1 response time, got %d", len(collector.responseTimes))
	}

	// 验证没有错误被记录
	if collector.errorCount != 0 {
		t.Errorf("expected 0 errors, got %d", collector.errorCount)
	}
}

func TestStatsMiddleware_Handler_Error(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	// 创建返回错误的路由
	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		c.String(http.StatusInternalServerError, "error")
	})

	// 发送请求
	req := httptest.NewRequest("GET", "/test/api", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 验证请求被记录
	if collector.requestCount != 1 {
		t.Errorf("expected 1 request, got %d", collector.requestCount)
	}

	// 验证错误被记录
	if collector.errorCount != 1 {
		t.Errorf("expected 1 error, got %d", collector.errorCount)
	}

	if collector.recordedErrors["test"] != 1 {
		t.Errorf("expected endpoint 'test' error to be recorded, got %v", collector.recordedErrors)
	}
}

func TestStatsMiddleware_Handler_4xxError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		c.String(http.StatusBadRequest, "bad request")
	})

	req := httptest.NewRequest("GET", "/test/api", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 4xx也应该被记录为错误
	if collector.errorCount != 1 {
		t.Errorf("expected 1 error for 4xx status, got %d", collector.errorCount)
	}
}

func TestStatsMiddleware_Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	// 禁用中间件
	middleware.Disable()

	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("GET", "/test/api", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 禁用后不应该记录
	if collector.requestCount != 0 {
		t.Errorf("expected 0 requests when disabled, got %d", collector.requestCount)
	}
}

func TestStatsMiddleware_Enable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	// 先禁用再启用
	middleware.Disable()
	middleware.Enable()

	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("GET", "/test/api", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 启用后应该记录
	if collector.requestCount != 1 {
		t.Errorf("expected 1 request when enabled, got %d", collector.requestCount)
	}
}

func TestExtractEndpoint(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/test/api/v1", "test"},
		{"/openai/v1/chat", "openai"},
		{"/api", "api"},
		{"/", "unknown"},
		{"", "unknown"},
		{"/test", "test"},
	}

	for _, tt := range tests {
		result := extractEndpoint(tt.path)
		if result != tt.expected {
			t.Errorf("extractEndpoint(%s) = %s, expected %s", tt.path, result, tt.expected)
		}
	}
}

func TestStatsMiddleware_MultipleRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	r.GET("/api2/endpoint", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// 发送多个请求到不同endpoint
	req1 := httptest.NewRequest("GET", "/test/api", nil)
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	req2 := httptest.NewRequest("GET", "/api2/endpoint", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	req3 := httptest.NewRequest("GET", "/test/api", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)

	// 验证总请求数
	if collector.requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", collector.requestCount)
	}

	// 验证不同endpoint的计数
	if collector.recordedRequests["test"] != 2 {
		t.Errorf("expected 2 requests to 'test', got %d", collector.recordedRequests["test"])
	}

	if collector.recordedRequests["api2"] != 1 {
		t.Errorf("expected 1 request to 'api2', got %d", collector.recordedRequests["api2"])
	}
}

func TestStatsMiddleware_ResponseTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	collector := NewMockCollector()
	middleware := NewStatsMiddleware(collector)

	r := gin.New()
	r.Use(middleware.Handler())
	r.GET("/test/api", func(c *gin.Context) {
		// 模拟处理时间
		time.Sleep(10 * time.Millisecond)
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest("GET", "/test/api", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 验证响应时间被记录
	if len(collector.responseTimes) != 1 {
		t.Fatal("response time not recorded")
	}

	// 验证响应时间大于处理时间
	if collector.responseTimes[0] < 10*time.Millisecond {
		t.Errorf("expected response time >= 10ms, got %v", collector.responseTimes[0])
	}
}
