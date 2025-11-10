package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// MockMappingManager 用于测试的模拟映射管理器
type MockMappingManager struct {
	mappings map[string]string
	err      error
}

func (m *MockMappingManager) GetAllMappings() map[string]string {
	return m.mappings
}

func (m *MockMappingManager) GetMapping(ctx context.Context, prefix string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if target, ok := m.mappings[prefix]; ok {
		return target, nil
	}
	return "", errors.New("mapping not found")
}

func (m *MockMappingManager) GetPrefixes() []string {
	prefixes := make([]string, 0, len(m.mappings))
	for prefix := range m.mappings {
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func TestNewTransparentProxy(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/test": "http://example.com",
		},
	}

	proxy := NewTransparentProxy(mapper, nil)

	if proxy == nil {
		t.Fatal("NewTransparentProxy returned nil")
	}

	if proxy.client == nil {
		t.Error("HTTP client not initialized")
	}

	if proxy.mapper == nil {
		t.Error("mapper not set")
	}
}

func TestTransparentProxy_ProxyRequest_Success(t *testing.T) {
	// 创建模拟后端服务器
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求被正确转发
		if r.URL.Path != "/api/test" {
			t.Errorf("expected path /api/test, got %s", r.URL.Path)
		}

		// 检查自定义头是否被转发
		if r.Header.Get("X-Custom-Header") != "test-value" {
			t.Error("custom header not forwarded")
		}

		// 返回响应
		w.Header().Set("X-Response-Header", "response-value")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer backend.Close()

	// 创建代理
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/test": backend.URL,
		},
	}
	proxy := NewTransparentProxy(mapper, nil)

	// 创建测试请求
	req := httptest.NewRequest("GET", "http://localhost/test/api/test", nil)
	req.Header.Set("X-Custom-Header", "test-value")

	// 创建响应记录器
	w := httptest.NewRecorder()

	// 执行代理请求
	err := proxy.ProxyRequest(w, req, "/test", "/api/test")
	if err != nil {
		t.Fatalf("ProxyRequest failed: %v", err)
	}

	// 验证响应
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if w.Header().Get("X-Response-Header") != "response-value" {
		t.Error("response header not forwarded")
	}

	if w.Body.String() != "success" {
		t.Errorf("expected body 'success', got %s", w.Body.String())
	}
}

func TestTransparentProxy_ProxyRequest_WithQueryString(t *testing.T) {
	// 创建模拟后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证查询字符串被正确转发
		if r.URL.RawQuery != "key=value&foo=bar" {
			t.Errorf("expected query 'key=value&foo=bar', got %s", r.URL.RawQuery)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/test": backend.URL,
		},
	}
	proxy := NewTransparentProxy(mapper, nil)

	req := httptest.NewRequest("GET", "http://localhost/test/api?key=value&foo=bar", nil)
	w := httptest.NewRecorder()

	err := proxy.ProxyRequest(w, req, "/test", "/api")
	if err != nil {
		t.Fatalf("ProxyRequest failed: %v", err)
	}
}

func TestTransparentProxy_ProxyRequest_WithBody(t *testing.T) {
	expectedBody := `{"test":"data"}`

	// 创建模拟后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求体被正确转发
		body, _ := io.ReadAll(r.Body)
		if string(body) != expectedBody {
			t.Errorf("expected body %s, got %s", expectedBody, string(body))
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/test": backend.URL,
		},
	}
	proxy := NewTransparentProxy(mapper, nil)

	req := httptest.NewRequest("POST", "http://localhost/test/api", strings.NewReader(expectedBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	err := proxy.ProxyRequest(w, req, "/test", "/api")
	if err != nil {
		t.Fatalf("ProxyRequest failed: %v", err)
	}
}

func TestTransparentProxy_ProxyRequest_MappingNotFound(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{},
	}
	proxy := NewTransparentProxy(mapper, nil)

	req := httptest.NewRequest("GET", "http://localhost/test/api", nil)
	w := httptest.NewRecorder()

	err := proxy.ProxyRequest(w, req, "/nonexistent", "/api")
	if err == nil {
		t.Error("expected error for nonexistent mapping")
	}
}

func TestTransparentProxy_ProxyRequest_BackendError(t *testing.T) {
	// 创建返回错误的后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("backend error"))
	}))
	defer backend.Close()

	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/test": backend.URL,
		},
	}
	proxy := NewTransparentProxy(mapper, nil)

	req := httptest.NewRequest("GET", "http://localhost/test/api", nil)
	w := httptest.NewRecorder()

	err := proxy.ProxyRequest(w, req, "/test", "/api")
	if err != nil {
		t.Fatalf("ProxyRequest should not error on backend error: %v", err)
	}

	// 验证错误状态码被正确转发
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}

	if w.Body.String() != "backend error" {
		t.Errorf("expected body 'backend error', got %s", w.Body.String())
	}
}

func TestTransparentProxy_HopByHopHeaders(t *testing.T) {
	// 创建后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证hop-by-hop头部被过滤
		hopByHopHeaders := []string{
			"Connection",
			"Keep-Alive",
			"Proxy-Authenticate",
			"Proxy-Authorization",
			"Te",
			"Trailer",
			"Transfer-Encoding",
			"Upgrade",
		}

		for _, header := range hopByHopHeaders {
			if r.Header.Get(header) != "" {
				t.Errorf("hop-by-hop header %s should be filtered", header)
			}
		}

		// 返回一些hop-by-hop头
		w.Header().Set("Connection", "close")
		w.Header().Set("X-Custom-Header", "should-be-forwarded")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/test": backend.URL,
		},
	}
	proxy := NewTransparentProxy(mapper, nil)

	req := httptest.NewRequest("GET", "http://localhost/test/api", nil)
	// 添加hop-by-hop头
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("X-Custom-Header", "test")

	w := httptest.NewRecorder()

	err := proxy.ProxyRequest(w, req, "/test", "/api")
	if err != nil {
		t.Fatalf("ProxyRequest failed: %v", err)
	}

	// 验证响应头中hop-by-hop被过滤
	if w.Header().Get("Connection") != "" {
		t.Error("Connection header should be filtered from response")
	}

	// 验证普通头被转发
	if w.Header().Get("X-Custom-Header") != "should-be-forwarded" {
		t.Error("custom header should be forwarded")
	}
}

func TestCopyHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("X-Custom-Header", "value")
	src.Set("Connection", "keep-alive")
	src.Set("Content-Type", "application/json")

	dst := http.Header{}
	copyHeaders(dst, src)

	// 验证普通头被复制
	if dst.Get("X-Custom-Header") != "value" {
		t.Error("custom header not copied")
	}

	if dst.Get("Content-Type") != "application/json" {
		t.Error("content-type not copied")
	}

	// 验证hop-by-hop头被过滤
	if dst.Get("Connection") != "" {
		t.Error("hop-by-hop header should be filtered")
	}
}

// MockStatsCollector 用于测试统计收集
type MockStatsCollector struct {
	recordRequestCalled bool
	recordErrorCalled   bool
	lastPrefix          string
}

func (m *MockStatsCollector) RecordRequest(prefix string) {
	m.recordRequestCalled = true
	m.lastPrefix = prefix
}

func (m *MockStatsCollector) RecordError(prefix string) {
	m.recordErrorCalled = true
	m.lastPrefix = prefix
}

func (m *MockStatsCollector) UpdateResponseMetrics(duration time.Duration) {
	// no-op for testing
}

// TestTransparentProxy_StatsOnlyForConfiguredMapping 验证只有配置了映射的端点才会被统计
func TestTransparentProxy_StatsOnlyForConfiguredMapping(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/configured": "http://example.com",
		},
	}

	// 测试1: 映射不存在时，不应该调用 RecordRequest
	t.Run("no stats for unconfigured endpoint", func(t *testing.T) {
		mockStats := &MockStatsCollector{}
		proxy := NewTransparentProxy(mapper, mockStats)

		req := httptest.NewRequest("GET", "http://localhost/unconfigured/path", nil)
		w := httptest.NewRecorder()

		_ = proxy.ProxyRequest(w, req, "/unconfigured", "/path")

		if mockStats.recordRequestCalled {
			t.Error("RecordRequest should not be called for unconfigured endpoint")
		}
	})

	// 测试2: 映射存在时，应该调用 RecordRequest
	t.Run("stats for configured endpoint", func(t *testing.T) {
		mockStats := &MockStatsCollector{}

		// 创建一个mock HTTP服务器
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}))
		defer server.Close()

		mapper := &MockMappingManager{
			mappings: map[string]string{
				"/configured": server.URL,
			},
		}

		proxy := NewTransparentProxy(mapper, mockStats)

		req := httptest.NewRequest("GET", "http://localhost/configured/path", nil)
		w := httptest.NewRecorder()

		_ = proxy.ProxyRequest(w, req, "/configured", "/path")

		if !mockStats.recordRequestCalled {
			t.Error("RecordRequest should be called for configured endpoint")
		}

		if mockStats.lastPrefix != "/configured" {
			t.Errorf("expected prefix '/configured', got '%s'", mockStats.lastPrefix)
		}
	})
}
