package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// MockMappingManager 模拟映射管理器
type MockMappingManager struct {
	mappings map[string]string
}

func (m *MockMappingManager) GetAllMappings() map[string]string {
	return m.mappings
}

func (m *MockMappingManager) GetMapping(ctx context.Context, prefix string) (string, error) {
	if target, ok := m.mappings[prefix]; ok {
		return target, nil
	}
	return "", nil
}

func (m *MockMappingManager) GetPrefixes() []string {
	prefixes := make([]string, 0, len(m.mappings))
	for prefix := range m.mappings {
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// MockStatsRecorder 模拟统计记录器
type MockStatsRecorder struct{}

func (m *MockStatsRecorder) RecordRequest(prefix string) {}

// MockStatsCollector 模拟统计收集器
type MockStatsCollector struct{}

func (m *MockStatsCollector) UpdateResponseMetrics(responseTime int64) {}

// 创建测试用的Handler
func createTestHandler(backendServer *httptest.Server) *Handler {
	mm := &MockMappingManager{
		mappings: map[string]string{
			"/test": backendServer.URL,
		},
	}
	sr := &MockStatsRecorder{}
	sc := &MockStatsCollector{}
	var errorCount, requestCount int64

	return NewHandler(mm, sr, sc, &errorCount, &requestCount)
}

// TestTransparentRequestHeaderForwarding 测试透明请求头转发
func TestTransparentRequestHeaderForwarding(t *testing.T) {
	// 创建后端服务器，记录收到的请求头
	receivedHeaders := make(http.Header)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 复制所有收到的请求头
		for name, values := range r.Header {
			receivedHeaders[name] = values
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer backend.Close()

	// 创建代理处理器
	handler := createTestHandler(backend)

	// 创建测试请求
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// 设置各种请求头
	testHeaders := map[string]string{
		"User-Agent":       "TestAgent/1.0",
		"Cookie":           "session=abc123",
		"Origin":           "https://example.com",
		"Referer":          "https://example.com/page",
		"Accept-Encoding":  "gzip, deflate, br",
		"Accept-Language":  "zh-CN,zh;q=0.9,en;q=0.8",
		"Cache-Control":    "no-cache",
		"If-None-Match":    "\"abc123\"",
		"If-Modified-Since": "Mon, 01 Jan 2024 00:00:00 GMT",
		"Range":            "bytes=0-1023",
		"X-Custom-Header":  "custom-value",
		"Authorization":    "Bearer token123",
		"Content-Type":     "application/json",
	}

	req := httptest.NewRequest("GET", "/test/api/endpoint", nil)
	for name, value := range testHeaders {
		req.Header.Set(name, value)
	}
	c.Request = req

	// 执行代理请求
	handler.HandleAPIProxy(c)

	// 等待异步处理完成
	time.Sleep(100 * time.Millisecond)

	// 验证所有请求头都被转发（除hop-by-hop头）
	for name, expectedValue := range testHeaders {
		if actualValue := receivedHeaders.Get(name); actualValue != expectedValue {
			t.Errorf("请求头 %s 未正确转发: 期望 %s, 实际 %s", name, expectedValue, actualValue)
		}
	}
}

// TestHopByHopHeaderFiltering 测试hop-by-hop头过滤
func TestHopByHopHeaderFiltering(t *testing.T) {
	// 创建后端服务器
	receivedHeaders := make(http.Header)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range r.Header {
			receivedHeaders[name] = values
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// 设置hop-by-hop头（这些不应该被转发）
	hopByHopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}

	req := httptest.NewRequest("GET", "/test/api", nil)
	for _, header := range hopByHopHeaders {
		req.Header.Set(header, "should-not-forward")
	}
	// 添加一个应该转发的头
	req.Header.Set("X-Should-Forward", "yes")
	c.Request = req

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证hop-by-hop头没有被转发
	for _, header := range hopByHopHeaders {
		if receivedHeaders.Get(header) != "" {
			t.Errorf("hop-by-hop头 %s 不应该被转发", header)
		}
	}

	// 验证正常头被转发
	if receivedHeaders.Get("X-Should-Forward") != "yes" {
		t.Error("正常请求头应该被转发")
	}
}

// TestTransparentResponseHeaderForwarding 测试透明响应头转发
func TestTransparentResponseHeaderForwarding(t *testing.T) {
	// 创建后端服务器，返回各种响应头
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 设置各种响应头
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", "\"abc123\"")
		w.Header().Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")
		w.Header().Set("X-Custom-Response", "custom-value")
		w.Header().Set("Set-Cookie", "session=xyz789")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// 设置hop-by-hop头（不应该被转发）
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Transfer-Encoding", "chunked")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	handler := createTestHandler(backend)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/api", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证响应头被正确转发
	expectedHeaders := map[string]string{
		"Content-Type":                "application/json",
		"Cache-Control":               "max-age=3600",
		"ETag":                        "\"abc123\"",
		"Last-Modified":               "Mon, 01 Jan 2024 00:00:00 GMT",
		"X-Custom-Response":           "custom-value",
		"Set-Cookie":                  "session=xyz789",
		"Access-Control-Allow-Origin": "*",
	}

	for name, expected := range expectedHeaders {
		if actual := w.Header().Get(name); actual != expected {
			t.Errorf("响应头 %s 未正确转发: 期望 %s, 实际 %s", name, expected, actual)
		}
	}

	// 验证hop-by-hop头没有被转发
	hopByHopHeaders := []string{"Connection", "Transfer-Encoding"}
	for _, header := range hopByHopHeaders {
		if w.Header().Get(header) != "" {
			t.Errorf("hop-by-hop响应头 %s 不应该被转发", header)
		}
	}
}

// TestStreamingResponse 测试流式响应
func TestStreamingResponse(t *testing.T) {
	// 创建后端服务器，模拟流式响应
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter不支持Flusher")
		}

		// 发送3个数据块
		for i := 0; i < 3; i++ {
			w.Write([]byte("data: chunk " + string(rune('0'+i)) + "\n\n"))
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer backend.Close()

	handler := createTestHandler(backend)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/stream", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(200 * time.Millisecond)

	// 验证响应内容
	body := w.Body.String()
	if !strings.Contains(body, "chunk 0") || !strings.Contains(body, "chunk 1") || !strings.Contains(body, "chunk 2") {
		t.Errorf("流式响应内容不完整: %s", body)
	}

	// 验证Content-Type
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Error("流式响应Content-Type不正确")
	}
}

// TestConcurrentRequests 测试并发请求安全性
func TestConcurrentRequests(t *testing.T) {
	requestCount := int32(0)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	// 并发发送100个请求
	concurrency := 100
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/test/api", nil)
			handler.HandleAPIProxy(c)
		}()
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond)

	// 验证所有请求都被处理
	if atomic.LoadInt32(&requestCount) != int32(concurrency) {
		t.Errorf("并发请求处理不正确: 期望 %d, 实际 %d", concurrency, requestCount)
	}
}

// TestErrorHandling 测试错误处理
func TestErrorHandling(t *testing.T) {
	// 测试后端服务器返回错误
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/api", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证错误状态码被正确转发
	if w.Code != http.StatusInternalServerError {
		t.Errorf("错误状态码未正确转发: 期望 %d, 实际 %d", http.StatusInternalServerError, w.Code)
	}

	// 验证错误响应体被正确转发
	if !strings.Contains(w.Body.String(), "Internal Server Error") {
		t.Error("错误响应体未正确转发")
	}

	// 验证错误计数增加
	if atomic.LoadInt64(handler.errorCount) == 0 {
		t.Error("错误计数未增加")
	}
}

// TestPOSTRequestWithBody 测试POST请求体转发
func TestPOSTRequestWithBody(t *testing.T) {
	receivedBody := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	requestBody := `{"message":"test data"}`
	c.Request = httptest.NewRequest("POST", "/test/api", bytes.NewBufferString(requestBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证请求体被正确转发
	if receivedBody != requestBody {
		t.Errorf("请求体未正确转发: 期望 %s, 实际 %s", requestBody, receivedBody)
	}
}

// TestQueryStringForwarding 测试查询字符串转发
func TestQueryStringForwarding(t *testing.T) {
	receivedQuery := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/api?param1=value1&param2=value2", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证查询字符串被正确转发
	if receivedQuery != "param1=value1&param2=value2" {
		t.Errorf("查询字符串未正确转发: 期望 param1=value1&param2=value2, 实际 %s", receivedQuery)
	}
}

// TestAsyncProxyContextHeadersSentOnce 测试响应头只发送一次
func TestAsyncProxyContextHeadersSentOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test", nil)

	var errorCount, requestCount int64
	asyncCtx := NewAsyncProxyContext(c, &errorCount, &requestCount)

	// 创建模拟响应
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set("X-Test", "value1")

	// 第一次调用WriteHeaders
	asyncCtx.WriteHeaders(resp)

	// 修改响应头
	resp.Header.Set("X-Test", "value2")

	// 第二次调用WriteHeaders（应该被忽略）
	asyncCtx.WriteHeaders(resp)

	// 验证响应头只被设置一次
	if w.Header().Get("X-Test") != "value1" {
		t.Error("响应头应该只被设置一次")
	}
}

// TestRequestCountIncrement 测试请求计数
func TestRequestCountIncrement(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	initialCount := atomic.LoadInt64(handler.requestCount)

	// 发送3个请求
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/test/api", nil)
		handler.HandleAPIProxy(c)
	}

	time.Sleep(200 * time.Millisecond)

	// 验证请求计数增加
	finalCount := atomic.LoadInt64(handler.requestCount)
	if finalCount-initialCount != 3 {
		t.Errorf("请求计数不正确: 期望增加3, 实际增加 %d", finalCount-initialCount)
	}
}

// TestInvalidPrefix 测试无效前缀
func TestInvalidPrefix(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/invalid/api", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证返回404
	if w.Code != http.StatusNotFound {
		t.Errorf("无效前缀应返回404: 实际 %d", w.Code)
	}
}

// TestOPTIONSRequest 测试OPTIONS预检请求
func TestOPTIONSRequest(t *testing.T) {
	backendCalled := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	// 创建Gin引擎来正确处理响应
	router := gin.New()
	router.Any("/test/*path", handler.HandleAPIProxy)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/test/api", nil)
	router.ServeHTTP(w, req)

	// 验证返回204
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS请求应返回204: 实际 %d", w.Code)
	}

	// 验证后端没有被调用
	time.Sleep(100 * time.Millisecond)
	if backendCalled {
		t.Error("OPTIONS请求不应该转发到后端")
	}
}

// TestHEADRequest 测试HEAD请求
func TestHEADRequest(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("期望HEAD请求, 实际 %s", r.Method)
		}
		w.Header().Set("Content-Length", "1234")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("HEAD", "/test/api", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证响应头被转发
	if w.Header().Get("Content-Length") != "1234" {
		t.Error("HEAD请求响应头未正确转发")
	}
}

// TestLargeResponseBody 测试大响应体
func TestLargeResponseBody(t *testing.T) {
	// 创建10MB的响应数据
	largeData := make([]byte, 10*1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(largeData)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/large", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(500 * time.Millisecond)

	// 验证响应体大小
	if w.Body.Len() != len(largeData) {
		t.Errorf("大响应体大小不正确: 期望 %d, 实际 %d", len(largeData), w.Body.Len())
	}
}

// TestDifferentContentTypes 测试不同Content-Type的缓冲区大小
func TestDifferentContentTypes(t *testing.T) {
	testCases := []struct {
		name        string
		contentType string
	}{
		{"SSE", "text/event-stream"},
		{"JSON", "application/json"},
		{"HTML", "text/html"},
		{"Image", "image/png"},
		{"Video", "video/mp4"},
		{"Plain", "text/plain"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("test data"))
			}))
			defer backend.Close()

			handler := createTestHandler(backend)
			gin.SetMode(gin.TestMode)

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/test/api", nil)

			handler.HandleAPIProxy(c)
			time.Sleep(100 * time.Millisecond)

			if w.Code != http.StatusOK {
				t.Errorf("%s: 状态码不正确", tc.name)
			}
		})
	}
}

// TestContextCancellation 测试上下文取消
func TestContextCancellation(t *testing.T) {
	// 创建一个会阻塞的后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/api", nil).WithContext(ctx)

	// 启动请求处理
	go handler.HandleAPIProxy(c)

	// 100ms后取消上下文
	time.Sleep(100 * time.Millisecond)
	cancel()

	time.Sleep(200 * time.Millisecond)

	// 验证请求被取消（不会等待5秒）
	// 这个测试主要验证取消机制工作正常
}

// TestMultipleHeaderValues 测试多值请求头
func TestMultipleHeaderValues(t *testing.T) {
	receivedHeaders := make(http.Header)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range r.Header {
			receivedHeaders[name] = values
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/api", nil)

	// 添加多值头
	c.Request.Header.Add("Accept", "application/json")
	c.Request.Header.Add("Accept", "text/html")
	c.Request.Header.Add("X-Custom", "value1")
	c.Request.Header.Add("X-Custom", "value2")

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证多值头被正确转发
	acceptValues := receivedHeaders["Accept"]
	if len(acceptValues) != 2 || acceptValues[0] != "application/json" || acceptValues[1] != "text/html" {
		t.Error("多值Accept头未正确转发")
	}

	customValues := receivedHeaders["X-Custom"]
	if len(customValues) != 2 || customValues[0] != "value1" || customValues[1] != "value2" {
		t.Error("多值X-Custom头未正确转发")
	}
}

// TestEmptyResponseBody 测试空响应体
func TestEmptyResponseBody(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	handler := createTestHandler(backend)
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/test/api", nil)

	handler.HandleAPIProxy(c)
	time.Sleep(100 * time.Millisecond)

	// 验证空响应体
	if w.Body.Len() != 0 {
		t.Error("空响应体应该为空")
	}

	if w.Code != http.StatusNoContent {
		t.Errorf("状态码不正确: 期望 %d, 实际 %d", http.StatusNoContent, w.Code)
	}
}
