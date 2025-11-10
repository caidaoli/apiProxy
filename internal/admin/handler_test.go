package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// MockMappingManager 用于测试的模拟映射管理器
type MockMappingManager struct {
	mappings map[string]string
	version  int64
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

func (m *MockMappingManager) AddMapping(ctx context.Context, prefix, target string) error {
	m.mappings[prefix] = target
	m.version++
	return nil
}

func (m *MockMappingManager) UpdateMapping(ctx context.Context, prefix, target string) error {
	m.mappings[prefix] = target
	m.version++
	return nil
}

func (m *MockMappingManager) DeleteMapping(ctx context.Context, prefix string) error {
	delete(m.mappings, prefix)
	m.version++
	return nil
}

func (m *MockMappingManager) ForceReload(ctx context.Context) error {
	// Mock实现:不需要实际重载
	return nil
}

func (m *MockMappingManager) Count() int {
	return len(m.mappings)
}

func (m *MockMappingManager) GetPrefixes() []string {
	prefixes := make([]string, 0, len(m.mappings))
	for prefix := range m.mappings {
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func (m *MockMappingManager) IsInitialized() bool {
	return true
}

func (m *MockMappingManager) GetVersion() int64 {
	return m.version
}

func setupTestRouter(handler *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	handler.SetupRoutes(r)
	return r
}

func addAuthCookie(req *http.Request) {
	req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: url.QueryEscape("test-token")})
}

func TestNewHandler(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	// 设置测试token
	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)

	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}

	if handler.mapper == nil {
		t.Error("mapper not set")
	}

	if handler.adminToken != "test-token" {
		t.Errorf("expected token 'test-token', got %s", handler.adminToken)
	}
}

func TestHandler_GetAllMappings(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/api1": "http://example1.com",
			"/api2": "http://example2.com",
		},
		version: 1,
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 创建请求
	req, _ := http.NewRequest("GET", "/api/mappings", nil)
	addAuthCookie(req)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)

	if response["success"] != true {
		t.Error("expected success true")
	}

	if response["count"].(float64) != 2 {
		t.Errorf("expected count 2, got %v", response["count"])
	}

	if response["version"].(float64) != 1 {
		t.Errorf("expected version 1, got %v", response["version"])
	}
}

func TestHandler_GetPublicMappings(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/api1": "http://example1.com",
		},
	}

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 公开接口不需要认证
	req, _ := http.NewRequest("GET", "/api/public/mappings", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)

	if response["success"] != true {
		t.Error("expected success true")
	}
}

func TestHandler_AddMapping(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 创建请求
	reqBody := map[string]string{
		"prefix": "/newapi",
		"target": "http://new.example.com",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "/api/mappings", bytes.NewBuffer(body))
	addAuthCookie(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", w.Code)
	}

	// 验证映射被添加
	if mapper.mappings["/newapi"] != "http://new.example.com" {
		t.Error("mapping not added")
	}
}

func TestHandler_UpdateMapping(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/api": "http://old.example.com",
		},
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 创建更新请求
	reqBody := map[string]string{
		"target": "http://new.example.com",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("PUT", "/api/mappings/api", bytes.NewBuffer(body))
	addAuthCookie(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// 验证映射被更新
	if mapper.mappings["/api"] != "http://new.example.com" {
		t.Error("mapping not updated")
	}
}

func TestHandler_UpdateMapping_MultiSegment(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/api/v1": "http://old.example.com",
		},
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	reqBody := map[string]string{
		"target": "http://new.example.com",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("PUT", "/api/mappings/api/v1", bytes.NewBuffer(body))
	addAuthCookie(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if mapper.mappings["/api/v1"] != "http://new.example.com" {
		t.Fatalf("mapping not updated for multi segment prefix: %+v", mapper.mappings)
	}
}

func TestHandler_DeleteMapping(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/api": "http://example.com",
		},
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	req, _ := http.NewRequest("DELETE", "/api/mappings/api", nil)
	addAuthCookie(req)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// 验证映射被删除
	if _, exists := mapper.mappings["/api"]; exists {
		t.Error("mapping should be deleted")
	}
}

func TestHandler_DeleteMapping_MultiSegment(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: map[string]string{
			"/api/v1": "http://example.com",
		},
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	req, _ := http.NewRequest("DELETE", "/api/mappings/api/v1", nil)
	addAuthCookie(req)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if _, exists := mapper.mappings["/api/v1"]; exists {
		t.Fatal("mapping should be deleted for multi segment prefix")
	}
}

func TestHandler_AuthMiddleware_NoToken(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 没有token的请求
	req, _ := http.NewRequest("GET", "/api/mappings", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestHandler_AuthMiddleware_InvalidToken(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "correct-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 错误的token cookie
	req, _ := http.NewRequest("GET", "/api/mappings", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: url.QueryEscape("wrong-token")})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestHandler_AuthMiddleware_NoAdminToken(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	// 不设置ADMIN_TOKEN
	os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	req, _ := http.NewRequest("GET", "/api/mappings", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestHandler_AuthMiddleware_Cookie(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	req, _ := http.NewRequest("GET", "/api/mappings", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: url.QueryEscape("test-token")})
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 when cookie matches, got %d", w.Code)
	}
}

func TestHandler_AdminLogin_Success(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	reqBody := map[string]string{
		"token": "test-token",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "/api/admin/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)

	if response["success"] != true {
		t.Error("expected success true")
	}

	foundCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == adminSessionCookie {
			foundCookie = true
			if cookie.Value != url.QueryEscape("test-token") {
				t.Errorf("expected encoded token in cookie, got %s", cookie.Value)
			}
		}
	}
	if !foundCookie {
		t.Error("expected admin session cookie to be set")
	}
}

func TestHandler_AdminLogin_InvalidToken(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "correct-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	reqBody := map[string]string{
		"token": "wrong-token",
	}
	body, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "/api/admin/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestHandler_AdminLogout(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	req, _ := http.NewRequest("POST", "/api/admin/logout", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	foundCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == adminSessionCookie {
			foundCookie = true
			if cookie.MaxAge != -1 {
				t.Error("expected logout cookie to be expired")
			}
		}
	}
	if !foundCookie {
		t.Error("expected logout to return clearing cookie")
	}
}

func TestHandler_AddMapping_InvalidJSON(t *testing.T) {
	mapper := &MockMappingManager{
		mappings: make(map[string]string),
	}

	os.Setenv("ADMIN_TOKEN", "test-token")
	defer os.Unsetenv("ADMIN_TOKEN")

	handler := NewHandler(mapper)
	r := setupTestRouter(handler)

	// 无效的JSON
	req, _ := http.NewRequest("POST", "/api/mappings", bytes.NewBufferString("invalid json"))
	addAuthCookie(req)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}
