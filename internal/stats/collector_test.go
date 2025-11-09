package stats

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestNewCollector(t *testing.T) {
	c := NewCollector(nil)

	if c == nil {
		t.Fatal("NewCollector returned nil")
	}

	if c.endpoints == nil {
		t.Error("endpoints map not initialized")
	}

	if c.GetRequestCount() != 0 {
		t.Error("initial request count should be 0")
	}

	if c.GetErrorCount() != 0 {
		t.Error("initial error count should be 0")
	}
}

func TestCollector_RecordRequest(t *testing.T) {
	c := NewCollector(nil)

	// 记录第一个请求
	c.RecordRequest("test-endpoint")

	if c.GetRequestCount() != 1 {
		t.Errorf("expected request count 1, got %d", c.GetRequestCount())
	}

	stats := c.GetStats()
	if len(stats) != 1 {
		t.Errorf("expected 1 endpoint, got %d", len(stats))
	}

	endpointStats, ok := stats["test-endpoint"]
	if !ok {
		t.Fatal("test-endpoint not found in stats")
	}

	if endpointStats.Count != 1 {
		t.Errorf("expected count 1, got %d", endpointStats.Count)
	}

	if endpointStats.LastRequest == 0 {
		t.Error("LastRequest should be set")
	}

	// 记录同一个endpoint的第二个请求
	c.RecordRequest("test-endpoint")

	if c.GetRequestCount() != 2 {
		t.Errorf("expected request count 2, got %d", c.GetRequestCount())
	}

	stats = c.GetStats()
	endpointStats = stats["test-endpoint"]
	if endpointStats.Count != 2 {
		t.Errorf("expected count 2, got %d", endpointStats.Count)
	}

	// 记录不同endpoint
	c.RecordRequest("another-endpoint")

	if c.GetRequestCount() != 3 {
		t.Errorf("expected request count 3, got %d", c.GetRequestCount())
	}

	stats = c.GetStats()
	if len(stats) != 2 {
		t.Errorf("expected 2 endpoints, got %d", len(stats))
	}
}

func TestCollector_RecordError(t *testing.T) {
	c := NewCollector(nil)

	// 记录错误
	c.RecordError("test-endpoint")

	if c.GetErrorCount() != 1 {
		t.Errorf("expected error count 1, got %d", c.GetErrorCount())
	}

	stats := c.GetStats()
	endpointStats := stats["test-endpoint"]

	if endpointStats.ErrorCount != 1 {
		t.Errorf("expected error count 1, got %d", endpointStats.ErrorCount)
	}

	// 记录多个错误
	c.RecordError("test-endpoint")
	c.RecordError("test-endpoint")

	if c.GetErrorCount() != 3 {
		t.Errorf("expected error count 3, got %d", c.GetErrorCount())
	}

	stats = c.GetStats()
	endpointStats = stats["test-endpoint"]

	if endpointStats.ErrorCount != 3 {
		t.Errorf("expected endpoint error count 3, got %d", endpointStats.ErrorCount)
	}
}

func TestCollector_UpdateResponseMetrics(t *testing.T) {
	c := NewCollector(nil)

	// 记录一个响应时间
	c.UpdateResponseMetrics(100 * time.Millisecond)

	avgTime := c.GetAverageResponseTime()
	if avgTime != 100*time.Millisecond {
		t.Errorf("expected avg time 100ms, got %v", avgTime)
	}

	// 记录第二个响应时间
	c.UpdateResponseMetrics(200 * time.Millisecond)

	avgTime = c.GetAverageResponseTime()
	expected := 150 * time.Millisecond
	if avgTime != expected {
		t.Errorf("expected avg time %v, got %v", expected, avgTime)
	}

	// 记录第三个响应时间
	c.UpdateResponseMetrics(300 * time.Millisecond)

	avgTime = c.GetAverageResponseTime()
	expected = 200 * time.Millisecond
	if avgTime != expected {
		t.Errorf("expected avg time %v, got %v", expected, avgTime)
	}
}

func TestCollector_GetAverageResponseTime_ZeroCount(t *testing.T) {
	c := NewCollector(nil)

	// 没有记录时应该返回0
	avgTime := c.GetAverageResponseTime()
	if avgTime != 0 {
		t.Errorf("expected avg time 0, got %v", avgTime)
	}
}

func TestCollector_GetStats_DeepCopy(t *testing.T) {
	c := NewCollector(nil)

	c.RecordRequest("test-endpoint")

	stats1 := c.GetStats()
	stats2 := c.GetStats()

	// 验证是深拷贝，修改stats1不影响stats2
	stats1["test-endpoint"].Count = 999

	if stats2["test-endpoint"].Count == 999 {
		t.Error("GetStats should return a deep copy")
	}

	// 验证原始数据未被修改
	stats3 := c.GetStats()
	if stats3["test-endpoint"].Count != 1 {
		t.Error("original stats should not be modified")
	}
}

func TestCollector_GetDroppedEvents(t *testing.T) {
	c := NewCollector(nil)

	// 简化版本应该始终返回0
	dropped := c.GetDroppedEvents()
	if dropped != 0 {
		t.Errorf("expected 0 dropped events, got %d", dropped)
	}
}

func TestCollector_Close(t *testing.T) {
	c := NewCollector(nil)

	err := c.Close()
	if err != nil {
		t.Errorf("Close should not return error, got %v", err)
	}
}

func TestCollector_SaveToRedis_NilClient(t *testing.T) {
	c := NewCollector(nil)

	ctx := context.Background()
	err := c.SaveToRedis(ctx)

	// nil client应该不报错
	if err != nil {
		t.Errorf("SaveToRedis with nil client should not error, got %v", err)
	}
}

func TestCollector_LoadFromRedis_NilClient(t *testing.T) {
	c := NewCollector(nil)

	ctx := context.Background()
	err := c.LoadFromRedis(ctx)

	// nil client应该不报错
	if err != nil {
		t.Errorf("LoadFromRedis with nil client should not error, got %v", err)
	}
}

func TestCollector_GetRequestCountPtr(t *testing.T) {
	c := NewCollector(nil)

	ptr := c.GetRequestCountPtr()
	if ptr == nil {
		t.Fatal("GetRequestCountPtr returned nil")
	}

	if *ptr != 0 {
		t.Errorf("expected initial value 0, got %d", *ptr)
	}

	c.RecordRequest("test")
	if *ptr != 1 {
		t.Errorf("expected value 1 after record, got %d", *ptr)
	}
}

func TestCollector_GetErrorCountPtr(t *testing.T) {
	c := NewCollector(nil)

	ptr := c.GetErrorCountPtr()
	if ptr == nil {
		t.Fatal("GetErrorCountPtr returned nil")
	}

	if *ptr != 0 {
		t.Errorf("expected initial value 0, got %d", *ptr)
	}

	c.RecordError("test")
	if *ptr != 1 {
		t.Errorf("expected value 1 after record, got %d", *ptr)
	}
}

// 并发测试
func TestCollector_Concurrent(t *testing.T) {
	c := NewCollector(nil)

	const goroutines = 100
	const requestsPerGoroutine = 1000

	done := make(chan bool, goroutines)

	// 启动多个goroutine并发写入
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			endpoint := "endpoint-" + string(rune('0'+id%10))
			for j := 0; j < requestsPerGoroutine; j++ {
				c.RecordRequest(endpoint)
				if j%3 == 0 {
					c.RecordError(endpoint)
				}
				if j%5 == 0 {
					c.UpdateResponseMetrics(time.Duration(j) * time.Millisecond)
				}
			}
			done <- true
		}(i)
	}

	// 等待所有goroutine完成
	for i := 0; i < goroutines; i++ {
		<-done
	}

	// 验证总数
	expectedTotal := goroutines * requestsPerGoroutine
	if c.GetRequestCount() != int64(expectedTotal) {
		t.Errorf("expected request count %d, got %d", expectedTotal, c.GetRequestCount())
	}

	// 验证stats可以安全读取
	stats := c.GetStats()
	if len(stats) == 0 {
		t.Error("stats should not be empty")
	}
}

// 边界测试
func TestCollector_EmptyEndpoint(t *testing.T) {
	c := NewCollector(nil)

	// 空endpoint应该也能正常工作
	c.RecordRequest("")

	stats := c.GetStats()
	if len(stats) != 1 {
		t.Error("empty endpoint should be recorded")
	}

	if stats[""].Count != 1 {
		t.Error("empty endpoint count incorrect")
	}
}

// TestCollector_GetRequests 测试获取请求列表
func TestCollector_GetRequests(t *testing.T) {
	c := NewCollector(nil)

	// 初始应该为空
	requests := c.GetRequests()
	if len(requests) != 0 {
		t.Errorf("expected empty requests, got %d", len(requests))
	}

	// 记录几个请求
	c.RecordRequest("/api/test1")
	c.RecordRequest("/api/test2")
	c.RecordRequest("/api/test3")

	requests = c.GetRequests()
	if len(requests) != 3 {
		t.Errorf("expected 3 requests, got %d", len(requests))
	}

	// 验证深拷贝（修改返回值不应影响内部状态）
	requests[0].Endpoint = "modified"
	newRequests := c.GetRequests()
	if newRequests[0].Endpoint == "modified" {
		t.Error("GetRequests should return deep copy")
	}
}

// TestCollector_GetPerformanceMetrics 测试性能指标
// TestCollector_GetPerformanceMetrics 测试性能指标
func TestCollector_GetPerformanceMetrics(t *testing.T) {
	c := NewCollector(nil)

	// 记录一些请求和错误
	c.RecordRequest("/api/test")
	c.UpdateResponseMetrics(100*time.Millisecond)

	c.RecordRequest("/api/test")
	c.UpdateResponseMetrics(200*time.Millisecond)
	
	c.RecordRequest("/api/test")
	c.RecordError("/api/test")

	// 等待缓存过期后再获取
	time.Sleep(6 * time.Second)
	
	metrics := c.GetPerformanceMetrics()
	if metrics == nil {
		t.Fatal("metrics should not be nil")
	}

	// 验证平均响应时间 (100ms + 200ms) / 2 = 150ms
	if metrics.AvgResponseTimeMs != 150 {
		t.Errorf("expected avg response time 150ms, got %d", metrics.AvgResponseTimeMs)
	}

	// 3个请求，1个错误，错误率应该是 33.33%
	expectedErrorRate := (1.0 / 3.0) * 100
	if metrics.ErrorRate < expectedErrorRate-1 || metrics.ErrorRate > expectedErrorRate+1 {
		t.Errorf("expected error rate ~%.2f%%, got %.2f%%", expectedErrorRate, metrics.ErrorRate)
	}
	
	// 验证内存和协程数据存在
	if metrics.MemoryUsageMB <= 0 {
		t.Error("memory usage should be > 0")
	}
	
	if metrics.GoroutineCount <= 0 {
		t.Error("goroutine count should be > 0")
	}
}
// TestCollector_GetPerformanceMetrics_Cache 测试性能指标缓存
func TestCollector_GetPerformanceMetrics_Cache(t *testing.T) {
	c := NewCollector(nil)

	c.RecordRequest("/api/test")

	// 第一次调用
	metrics1 := c.GetPerformanceMetrics()
	
	// 立即第二次调用应该返回缓存
	metrics2 := c.GetPerformanceMetrics()

	// 应该是同一个对象
	if metrics1 != metrics2 {
		t.Error("should return cached metrics")
	}

	// 等待缓存过期
	time.Sleep(6 * time.Second)
	
	// 再次调用应该重新计算
	metrics3 := c.GetPerformanceMetrics()
	if metrics1 == metrics3 {
		t.Error("should recalculate metrics after cache expiry")
	}
}

// TestCollector_SaveAndLoadRedis 测试 Redis 持久化
func TestCollector_SaveAndLoadRedis(t *testing.T) {
	// 使用 miniredis 模拟
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// 创建 collector 并记录数据
	c1 := NewCollector(client)
	c1.RecordRequest("/api/test1")
	c1.RecordRequest("/api/test2")
	c1.RecordError("/api/test3")

	// 保存到 Redis
	ctx := context.Background()
	if err := c1.SaveToRedis(ctx); err != nil {
		t.Fatalf("failed to save to redis: %v", err)
	}

	// 创建新的 collector 并从 Redis 加载
	c2 := NewCollector(client)
	if err := c2.LoadFromRedis(ctx); err != nil {
		t.Fatalf("failed to load from redis: %v", err)
	}

	// 验证数据一致
	if c2.GetRequestCount() != c1.GetRequestCount() {
		t.Errorf("request count mismatch: expected %d, got %d",
			c1.GetRequestCount(), c2.GetRequestCount())
	}

	if c2.GetErrorCount() != c1.GetErrorCount() {
		t.Errorf("error count mismatch: expected %d, got %d",
			c1.GetErrorCount(), c2.GetErrorCount())
	}
}

