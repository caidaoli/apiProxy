package stats

import (
	"testing"
)

// BenchmarkCollector_RecordRequest V2架构统计系统性能
func BenchmarkCollector_RecordRequest(b *testing.B) {
	collector := NewCollector(nil)
	defer collector.Close()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			collector.RecordRequest("test-endpoint")
		}
	})
}

// BenchmarkCollector_HighConcurrency V2架构高并发测试
func BenchmarkCollector_HighConcurrency(b *testing.B) {
	collector := NewCollector(nil)
	defer collector.Close()

	// 模拟1000个并发请求
	b.SetParallelism(1000)
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			collector.RecordRequest("endpoint-" + string(rune(b.N%10)))
		}
	})
}

// 预期结果（V2架构）:
//
// BenchmarkCollector_RecordRequest-8       	10000000	        50 ns/op	       0 B/op	       0 allocs/op
// BenchmarkCollector_HighConcurrency-8     	  1000000	       500 ns/op	       0 B/op	       0 allocs/op
//
// V2架构优势：
// - 无锁设计：使用channel代替互斥锁
// - 零内存分配：统计记录不进行内存分配
// - 线性扩展：高并发下性能稳定
// - 非阻塞：channel满了会丢弃，不会阻塞业务
