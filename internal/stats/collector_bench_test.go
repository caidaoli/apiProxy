package stats

import (
	"testing"
	"time"
)

// Benchmark_Collector_RecordRequest 测试记录请求的性能
func Benchmark_Collector_RecordRequest(b *testing.B) {
	c := NewCollector(nil)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// 模拟不同端点（增加锁竞争）
			endpoint := "endpoint" + string(rune('0'+i%10))
			c.RecordRequest(endpoint)
			i++
		}
	})
}

// Benchmark_Collector_RecordRequest_SingleEndpoint 单端点性能测试
func Benchmark_Collector_RecordRequest_SingleEndpoint(b *testing.B) {
	c := NewCollector(nil)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.RecordRequest("test-endpoint")
		}
	})
}

// Benchmark_Collector_RecordError 测试记录错误的性能
func Benchmark_Collector_RecordError(b *testing.B) {
	c := NewCollector(nil)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.RecordError("test-endpoint")
		}
	})
}

// Benchmark_Collector_UpdateResponseMetrics 测试更新响应时间的性能
func Benchmark_Collector_UpdateResponseMetrics(b *testing.B) {
	c := NewCollector(nil)
	duration := 100 * time.Millisecond

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.UpdateResponseMetrics(duration)
		}
	})
}

// Benchmark_Collector_GetStats 测试获取统计快照的性能
func Benchmark_Collector_GetStats(b *testing.B) {
	c := NewCollector(nil)

	// 预填充一些数据
	for i := 0; i < 100; i++ {
		endpoint := "endpoint" + string(rune('0'+i%10))
		c.RecordRequest(endpoint)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.GetStats()
	}
}

// Benchmark_Collector_Mixed 混合场景性能测试
func Benchmark_Collector_Mixed(b *testing.B) {
	c := NewCollector(nil)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			endpoint := "endpoint" + string(rune('0'+i%10))

			switch i % 4 {
			case 0:
				c.RecordRequest(endpoint)
			case 1:
				c.RecordError(endpoint)
			case 2:
				c.UpdateResponseMetrics(100 * time.Millisecond)
			case 3:
				_ = c.GetStats()
			}
			i++
		}
	})
}
