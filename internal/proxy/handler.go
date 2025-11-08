package proxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// MappingManager 接口定义，用于解耦
type MappingManager interface {
	GetAllMappings() map[string]string
	GetMapping(ctx context.Context, prefix string) (string, error)
	GetPrefixes() []string
}

// StatsRecorder 统计记录器接口
type StatsRecorder interface {
	RecordRequest(prefix string)
}

// StatsCollector 统计收集器接口
type StatsCollector interface {
	UpdateResponseMetrics(responseTime int64)
}

// Handler 代理处理器
type Handler struct {
	mappingManager MappingManager
	statsRecorder  StatsRecorder
	statsCollector StatsCollector
	httpClient     *http.Client
	errorCount     *int64
	requestCount   *int64
}

// NewHandler 创建代理处理器
func NewHandler(mm MappingManager, sr StatsRecorder, statsCol StatsCollector, errorCount, requestCount *int64) *Handler {
	return &Handler{
		mappingManager: mm,
		statsRecorder:  sr,
		statsCollector: statsCol,
		httpClient:     createHTTPClient(),
		errorCount:     errorCount,
		requestCount:   requestCount,
	}
}

// createHTTPClient 创建优化的HTTP客户端
func createHTTPClient() *http.Client {
	return &http.Client{
		// 不设置超时，完全透明代理
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			// 不设置IdleConnTimeout和ResponseHeaderTimeout，保持透明
		},
	}
}

// extractPrefixAndRest 提取前缀和剩余路径
func (h *Handler) extractPrefixAndRest(pathname string) (string, string) {
	mappings := h.mappingManager.GetAllMappings()
	for prefix := range mappings {
		if after, ok := strings.CutPrefix(pathname, prefix); ok {
			return prefix, after
		}
	}
	return "", ""
}

// AsyncProxyContext 异步代理上下文
type AsyncProxyContext struct {
	ctx          context.Context
	cancel       context.CancelFunc
	clientWriter gin.ResponseWriter
	flusher      http.Flusher
	headersSent  atomic.Bool
	startTime    time.Time
	errorCount   *int64
	requestCount *int64
}

// NewAsyncProxyContext 创建异步代理上下文
func NewAsyncProxyContext(c *gin.Context, errorCount, requestCount *int64) *AsyncProxyContext {
	// 不设置超时，使用原始请求的context
	ctx, cancel := context.WithCancel(c.Request.Context())

	var flusher http.Flusher
	if f, ok := c.Writer.(http.Flusher); ok {
		flusher = f
	}

	return &AsyncProxyContext{
		ctx:          ctx,
		cancel:       cancel,
		clientWriter: c.Writer,
		flusher:      flusher,
		startTime:    time.Now(),
		errorCount:   errorCount,
		requestCount: requestCount,
	}
}

// WriteHeaders 写入响应头（只执行一次）
func (apc *AsyncProxyContext) WriteHeaders(resp *http.Response) {
	if apc.headersSent.CompareAndSwap(false, true) {
		// 透明转发响应头，但过滤hop-by-hop头
		// RFC 7230: hop-by-hop头不应被代理转发
		hopByHopHeaders := map[string]bool{
			"connection":          true,
			"keep-alive":          true,
			"proxy-authenticate":  true,
			"proxy-authorization": true,
			"te":                  true,
			"trailer":             true,
			"transfer-encoding":   true,
			"upgrade":             true,
		}

		for name, values := range resp.Header {
			if !hopByHopHeaders[strings.ToLower(name)] {
				// 原样复制所有值
				apc.clientWriter.Header()[name] = values
			}
		}

		apc.clientWriter.WriteHeader(resp.StatusCode)

		if apc.flusher != nil {
			apc.flusher.Flush()
		}
	}
}

// StreamData 流式传输数据
func (apc *AsyncProxyContext) StreamData(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	if _, err := apc.clientWriter.Write(data); err != nil {
		return err
	}

	if apc.flusher != nil {
		apc.flusher.Flush()
	}

	return nil
}

// HandleAPIProxy 处理API代理请求
func (h *Handler) HandleAPIProxy(c *gin.Context) {
	path := c.Request.URL.Path
	prefix, rest := h.extractPrefixAndRest(path)
	if prefix == "" {
		atomic.AddInt64(h.errorCount, 1)
		c.JSON(404, gin.H{"error": "Not Found"})
		return
	}

	asyncCtx := NewAsyncProxyContext(c, h.errorCount, h.requestCount)
	defer asyncCtx.cancel()

	atomic.AddInt64(h.requestCount, 1)

	// 异步记录请求
	go h.statsRecorder.RecordRequest(prefix)

	if c.Request.Method == "OPTIONS" {
		c.Status(http.StatusNoContent)
		return
	}

	go func() {
		defer asyncCtx.cancel()
		if err := h.handleAsyncAPIRequest(asyncCtx, c, prefix, rest); err != nil {
			log.Printf("Async API request error: %v", err)
			atomic.AddInt64(h.errorCount, 1)
		}
	}()

	<-asyncCtx.ctx.Done()
	h.updateResponseMetrics(asyncCtx.startTime)
}

// handleAsyncAPIRequest 异步处理API请求
func (h *Handler) handleAsyncAPIRequest(asyncCtx *AsyncProxyContext, c *gin.Context, prefix, rest string) error {
	targetBase, err := h.mappingManager.GetMapping(c.Request.Context(), prefix)
	if err != nil {
		return err
	}

	targetURL := targetBase + rest
	if c.Request.URL.RawQuery != "" {
		targetURL += "?" + c.Request.URL.RawQuery
	}

	var body io.Reader
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return err
		}
		body = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(asyncCtx.ctx, c.Request.Method, targetURL, body)
	if err != nil {
		return err
	}

	// 透明转发所有请求头（除hop-by-hop头）
	// RFC 7230: hop-by-hop头不应被代理转发
	hopByHopHeaders := map[string]bool{
		"connection":          true,
		"keep-alive":          true,
		"proxy-authenticate":  true,
		"proxy-authorization": true,
		"te":                  true,
		"trailer":             true,
		"transfer-encoding":   true,
		"upgrade":             true,
	}

	for name, values := range c.Request.Header {
		if !hopByHopHeaders[strings.ToLower(name)] {
			// 原样复制所有值
			req.Header[name] = values
		}
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	asyncCtx.WriteHeaders(resp)

	if resp.StatusCode >= 400 {
		atomic.AddInt64(h.errorCount, 1)
	}

	return streamResponseBody(asyncCtx, resp)
}

// streamResponseBody 流式转发响应体
func streamResponseBody(asyncCtx *AsyncProxyContext, resp *http.Response) error {
	contentType := resp.Header.Get("Content-Type")
	isStreaming := strings.Contains(contentType, "text/event-stream") ||
		strings.Contains(contentType, "application/stream+json") ||
		strings.Contains(contentType, "text/plain")

	var bufferSize int
	if isStreaming {
		bufferSize = 2 * 1024
	} else if strings.Contains(contentType, "text/html") {
		bufferSize = 8 * 1024
	} else if strings.Contains(contentType, "application/json") {
		bufferSize = 16 * 1024
	} else if strings.Contains(contentType, "image/") || strings.Contains(contentType, "video/") {
		bufferSize = 64 * 1024
	} else {
		bufferSize = 32 * 1024
	}

	buffer := make([]byte, bufferSize)

	for {
		select {
		case <-asyncCtx.ctx.Done():
			return asyncCtx.ctx.Err()
		default:
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				if streamErr := asyncCtx.StreamData(buffer[:n]); streamErr != nil {
					return streamErr
				}
			}

			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
}

// updateResponseMetrics 更新响应指标
func (h *Handler) updateResponseMetrics(startTime time.Time) {
	if h.statsCollector != nil {
		responseTime := time.Since(startTime).Milliseconds()
		h.statsCollector.UpdateResponseMetrics(responseTime)
	}
}
