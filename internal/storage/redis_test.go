package storage

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	return mr, client
}

func TestNewMappingManager(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	ctx := context.Background()

	// 初始化Redis数据（reloadMappings要求必须有数据）
	client.HSet(ctx, KeyMappings, "/init", "http://init.example.com")
	client.Set(ctx, KeyMappingsVersion, "1", 0)

	// 创建映射管理器
	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}

	// 初始化
	err := mm.reloadMappings(ctx)
	if err != nil {
		t.Fatalf("reloadMappings failed: %v", err)
	}

	// 手动设置initialized状态（因为我们不是通过NewMappingManager创建的）
	mm.initialized.Store(true)

	if !mm.IsInitialized() {
		t.Error("MappingManager should be initialized")
	}

	if mm.GetVersion() == 0 {
		t.Error("version should be set")
	}

	if mm.Count() != 1 {
		t.Errorf("expected 1 mapping, got %d", mm.Count())
	}
}

func TestMappingManager_AddMapping(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 添加映射
	err := mm.AddMapping(ctx, "/test", "http://example.com")
	if err != nil {
		t.Fatalf("AddMapping failed: %v", err)
	}

	// 验证Redis中存储成功
	val, err := client.HGet(ctx, KeyMappings, "/test").Result()
	if err != nil {
		t.Fatalf("HGet failed: %v", err)
	}
	if val != "http://example.com" {
		t.Errorf("expected http://example.com, got %s", val)
	}

	// 验证缓存更新
	if mm.cache["/test"] != "http://example.com" {
		t.Error("cache not updated")
	}

	// 验证版本递增
	initialVersion := mm.GetVersion()
	if initialVersion == 0 {
		t.Error("version should be incremented")
	}
}

func TestMappingManager_AddMapping_Invalid(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	tests := []struct {
		name   string
		prefix string
		target string
	}{
		{"empty prefix", "", "http://example.com"},
		{"empty target", "/test", ""},
		{"invalid URL", "/test", "not-a-url"},
		{"prefix without slash", "test", "http://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mm.AddMapping(ctx, tt.prefix, tt.target)
			if err == nil {
				t.Error("expected error for invalid mapping")
			}
		})
	}
}

func TestMappingManager_GetMapping(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 先添加一个映射
	mm.AddMapping(ctx, "/api", "http://api.example.com")

	// 获取存在的映射
	target, err := mm.GetMapping(ctx, "/api")
	if err != nil {
		t.Fatalf("GetMapping failed: %v", err)
	}
	if target != "http://api.example.com" {
		t.Errorf("expected http://api.example.com, got %s", target)
	}

	// 获取不存在的映射
	_, err = mm.GetMapping(ctx, "/nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent mapping")
	}
}

func TestMappingManager_UpdateMapping(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 先添加一个映射
	mm.AddMapping(ctx, "/api", "http://old.example.com")
	oldVersion := mm.GetVersion()

	// 更新映射
	err := mm.UpdateMapping(ctx, "/api", "http://new.example.com")
	if err != nil {
		t.Fatalf("UpdateMapping failed: %v", err)
	}

	// 验证更新成功
	target, _ := mm.GetMapping(ctx, "/api")
	if target != "http://new.example.com" {
		t.Errorf("expected http://new.example.com, got %s", target)
	}

	// 验证版本递增
	if mm.GetVersion() <= oldVersion {
		t.Error("version should be incremented after update")
	}

	// 更新不存在的映射应该失败
	err = mm.UpdateMapping(ctx, "/nonexistent", "http://test.com")
	if err == nil {
		t.Error("expected error when updating nonexistent mapping")
	}
}

func TestMappingManager_DeleteMapping(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 先添加一个映射
	mm.AddMapping(ctx, "/api", "http://example.com")
	initialCount := mm.Count()

	// 删除映射
	err := mm.DeleteMapping(ctx, "/api")
	if err != nil {
		t.Fatalf("DeleteMapping failed: %v", err)
	}

	// 验证删除成功
	_, err = mm.GetMapping(ctx, "/api")
	if err == nil {
		t.Error("mapping should be deleted")
	}

	// 验证计数减少
	if mm.Count() != initialCount-1 {
		t.Error("count should decrease after deletion")
	}

	// 删除不存在的映射应该失败
	err = mm.DeleteMapping(ctx, "/nonexistent")
	if err == nil {
		t.Error("expected error when deleting nonexistent mapping")
	}
}

func TestMappingManager_GetAllMappings(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 添加多个映射
	testMappings := map[string]string{
		"/api1": "http://api1.example.com",
		"/api2": "http://api2.example.com",
		"/api3": "http://api3.example.com",
	}

	for prefix, target := range testMappings {
		mm.AddMapping(ctx, prefix, target)
	}

	// 获取所有映射
	mappings := mm.GetAllMappings()

	if len(mappings) != len(testMappings) {
		t.Errorf("expected %d mappings, got %d", len(testMappings), len(mappings))
	}

	for prefix, expectedTarget := range testMappings {
		if mappings[prefix] != expectedTarget {
			t.Errorf("expected %s for %s, got %s", expectedTarget, prefix, mappings[prefix])
		}
	}
}

func TestMappingManager_Count(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 初始计数应该是0
	if mm.Count() != 0 {
		t.Errorf("expected initial count 0, got %d", mm.Count())
	}

	// 添加映射
	mm.AddMapping(ctx, "/api1", "http://example.com")
	if mm.Count() != 1 {
		t.Errorf("expected count 1, got %d", mm.Count())
	}

	mm.AddMapping(ctx, "/api2", "http://example.com")
	if mm.Count() != 2 {
		t.Errorf("expected count 2, got %d", mm.Count())
	}

	// 删除映射
	mm.DeleteMapping(ctx, "/api1")
	if mm.Count() != 1 {
		t.Errorf("expected count 1 after deletion, got %d", mm.Count())
	}
}

func TestMappingManager_GetPrefixes(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	// 添加映射
	expectedPrefixes := []string{"/api1", "/api2", "/api3"}
	for _, prefix := range expectedPrefixes {
		mm.AddMapping(ctx, prefix, "http://example.com")
	}

	// 获取前缀
	prefixes := mm.GetPrefixes()

	if len(prefixes) != len(expectedPrefixes) {
		t.Errorf("expected %d prefixes, got %d", len(expectedPrefixes), len(prefixes))
	}

	// 验证所有前缀都存在
	prefixMap := make(map[string]bool)
	for _, p := range prefixes {
		prefixMap[p] = true
	}

	for _, expected := range expectedPrefixes {
		if !prefixMap[expected] {
			t.Errorf("prefix %s not found", expected)
		}
	}
}

func TestMappingManager_GetPrefixesSorted(t *testing.T) {
	mm := &MappingManager{
		cache: map[string]string{
			"/a":         "http://a",
			"/openai":    "http://openai",
			"/openai/v1": "http://openai-v1",
		},
	}

	got := mm.GetPrefixes()
	expected := []string{"/openai/v1", "/openai", "/a"}
	if len(got) != len(expected) {
		t.Fatalf("expected %d prefixes, got %d", len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("expected order %v, got %v", expected, got)
		}
	}
}

func TestMappingManager_ReloadMappings(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}

	ctx := context.Background()

	// 直接在Redis中添加数据
	client.HSet(ctx, KeyMappings, "/direct", "http://direct.example.com")
	client.Set(ctx, KeyMappingsVersion, "123", 0)

	// 重新加载
	err := mm.reloadMappings(ctx)
	if err != nil {
		t.Fatalf("reloadMappings failed: %v", err)
	}

	// 验证缓存更新
	if mm.cache["/direct"] != "http://direct.example.com" {
		t.Error("cache not updated after reload")
	}

	// 验证版本更新
	if mm.GetVersion() != 123 {
		t.Errorf("expected version 123, got %d", mm.GetVersion())
	}

	// 验证最后重载时间
	lastReload := mm.lastReload.Load()
	if lastReload == 0 {
		t.Error("lastReload should be set")
	}

	// 注意：initialized状态只在NewMappingManager中设置
	// reloadMappings本身不设置这个状态
}

func TestMappingManager_Close(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}

	// 启动后台goroutine
	mm.wg.Add(1)
	go func() {
		defer mm.wg.Done()
		<-mm.stopChan
	}()

	// 关闭管理器
	err := mm.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// 验证stopChan已关闭
	select {
	case <-mm.stopChan:
		// 正常，已关闭
	case <-time.After(100 * time.Millisecond):
		t.Error("stopChan should be closed")
	}
}

func TestMappingManager_GetClient(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}

	if mm.GetClient() != client {
		t.Error("GetClient should return the redis client")
	}
}

func TestParseRedisURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantError bool
	}{
		{
			name:      "valid URL",
			url:       "redis://localhost:6379/0",
			wantError: false,
		},
		{
			name:      "valid URL with password",
			url:       "redis://:password@localhost:6379/0",
			wantError: false,
		},
		{
			name:      "invalid URL",
			url:       "not-a-redis-url",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseRedisURL(tt.url)
			if tt.wantError {
				if err == nil {
					t.Error("expected error for invalid URL")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if opts == nil {
					t.Error("options should not be nil")
				}
			}
		})
	}
}

func TestValidateMapping(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		target    string
		wantError bool
	}{
		{"valid mapping", "/api", "http://example.com", false},
		{"valid https", "/api", "https://example.com", false},
		{"empty prefix", "", "http://example.com", true},
		{"empty target", "/api", "", true},
		{"prefix without slash", "api", "http://example.com", true},
		{"invalid URL", "/api", "not-a-url", true},
		{"wrong scheme", "/api", "ftp://example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMapping(tt.prefix, tt.target)
			if tt.wantError {
				if err == nil {
					t.Error("expected error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// 并发测试
func TestMappingManager_Concurrent(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	mm := &MappingManager{
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	mm.initialized.Store(true)

	ctx := context.Background()

	const goroutines = 50
	const operationsPerGoroutine = 100

	done := make(chan bool, goroutines)

	// 并发添加和读取
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			prefix := "/api" + string(rune('0'+id%10))
			target := "http://example" + string(rune('0'+id%10)) + ".com"

			for j := 0; j < operationsPerGoroutine; j++ {
				switch j % 4 {
				case 0:
					mm.AddMapping(ctx, prefix, target)
				case 1:
					mm.GetMapping(ctx, prefix)
				case 2:
					mm.GetAllMappings()
				case 3:
					mm.Count()
				}
			}
		}(i)
	}

	// 等待完成
	for i := 0; i < goroutines; i++ {
		<-done
	}

	// 验证最终状态一致
	if mm.Count() == 0 {
		t.Error("should have mappings after concurrent operations")
	}
}
