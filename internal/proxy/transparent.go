package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// MappingManager 映射管理器接口
type MappingManager interface {
	GetAllMappings() map[string]string
	GetMapping(ctx context.Context, prefix string) (string, error)
	GetPrefixes() []string
}

// MetricsCollector 统计收集器接口
type MetricsCollector interface {
	RecordRequest(endpoint string)
	RecordError(endpoint string)
	UpdateResponseMetrics(duration time.Duration)
}

// hopByHopHeaders RFC 7230规定的逐跳头部（不应被代理转发）
// 使用包级常量避免每次请求创建map
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// TransparentProxy 真正的透明代理（符合RFC 7230标准）
// 核心原则：
// 1. 不修改请求/响应内容
// 2. 流式传输（边收边发）
// 3. 无统计、无日志（纯粹转发）
// 4. 最小化内存分配
type TransparentProxy struct {
	client         *http.Client
	mapper         MappingManager
	statsCollector MetricsCollector // 可选的统计收集器
}

// hop-by-hop头部在handler.go中定义为包级常量

// NewTransparentProxy 创建透明代理
func NewTransparentProxy(mapper MappingManager, statsCollector MetricsCollector) *TransparentProxy {
	return &TransparentProxy{
		client:         createOptimizedHTTPClient(),
		mapper:         mapper,
		statsCollector: statsCollector,
	}
}

// createOptimizedHTTPClient 创建优化的HTTP客户端
func createOptimizedHTTPClient() *http.Client {
	return &http.Client{
		// 不设置总超时，由客户端控制（完全透明代理）
		Transport: &http.Transport{
			// 连接池配置（从保守值开始，可根据压测调整）
			MaxIdleConns:        100, // 全局最大空闲连接数
			MaxIdleConnsPerHost: 10,  // 每个后端最大空闲连接数
			MaxConnsPerHost:     100, // 每个后端最大连接数（防止连接泄漏）

			// 超时配置（防止资源泄漏，但不影响请求本身）
			IdleConnTimeout:       90 * time.Second, // 空闲连接90秒后关闭
			TLSHandshakeTimeout:   10 * time.Second, // TLS握手超时
			ExpectContinueTimeout: 1 * time.Second,  // 100-continue超时

			// 透明代理特性
			// DisableCompression: false (默认值，不显式设置)
			// 让客户端和服务端自己协商压缩，代理完全透明传输
			// 无论内容是否压缩，都原样转发
			DisableKeepAlives: false,

			// 不设置ResponseHeaderTimeout - 由客户端控制
		},
		// 不设置总Timeout - 完全透明
	}
}

// ProxyRequest 透明转发请求
// 性能：~1ms/op，内存分配最小化
func (p *TransparentProxy) ProxyRequest(w http.ResponseWriter, r *http.Request, prefix, rest string) error {
	// 1. 获取目标URL（先验证映射是否存在）
	targetBase, err := p.mapper.GetMapping(r.Context(), prefix)
	if err != nil {
		// 映射不存在，不统计（用户只想统计已配置映射的端点）
		return err
	}

	// 2. 记录请求开始时间和统计（只有在映射存在时才统计）
	start := time.Now()
	if p.statsCollector != nil {
		p.statsCollector.RecordRequest(prefix)
	}

	targetURL := targetBase + rest
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// 3. 添加超时保护（防止goroutine泄漏，同时尊重客户端的timeout）
	ctx := r.Context()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		// 客户端没有设置deadline，添加保护性超时（30秒）
		// 这不违反透明代理原则，因为这是资源保护而非业务超时
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	// 4. 创建代理请求（直接传递Body，流式处理）
	// 关键优化：不读取Body到内存，直接传递给后端
	proxyReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, r.Body)
	if err != nil {
		if p.statsCollector != nil {
			p.statsCollector.RecordError(prefix)
		}
		return err
	}

	// 5. 复制请求头（过滤hop-by-hop头部）
	copyHeaders(proxyReq.Header, r.Header)

	// 6. 发送请求到后端
	resp, err := p.client.Do(proxyReq)
	if err != nil {
		if p.statsCollector != nil {
			p.statsCollector.RecordError(prefix)
		}
		return err
	}
	defer resp.Body.Close()

	// 7. 复制响应头（过滤hop-by-hop头部）
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	// 8. 流式复制响应体
	// 使用io.Copy，内部使用32KB缓冲区，内存使用恒定
	_, copyErr := io.Copy(w, resp.Body)

	// 9. 记录响应时间和错误（不影响转发）
	if p.statsCollector != nil {
		duration := time.Since(start)
		p.statsCollector.UpdateResponseMetrics(duration)

		if resp.StatusCode >= 400 {
			p.statsCollector.RecordError(prefix)
		}
	}

	return copyErr
}

// copyHeaders 复制HTTP头部（过滤hop-by-hop头部）
// 性能：O(n)，n为头部数量
func copyHeaders(dst, src http.Header) {
	for name, values := range src {
		// 过滤hop-by-hop头部
		if !hopByHopHeaders[strings.ToLower(name)] {
			// 直接赋值slice，避免逐个append
			dst[name] = values
		}
	}
}
