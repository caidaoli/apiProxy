package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
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
	mappingManager  MappingManager
	statsRecorder   StatsRecorder
	statsCollector  StatsCollector
	httpClient      *http.Client
	errorCount      *int64
	requestCount    *int64
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
		Timeout: 1800 * time.Second, // 30分钟，适合长时间AI流式响应
		Transport: &http.Transport{
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   256,
			IdleConnTimeout:       120 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
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
func NewAsyncProxyContext(c *gin.Context, timeout time.Duration, errorCount, requestCount *int64) *AsyncProxyContext {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)

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
func (apc *AsyncProxyContext) WriteHeaders(resp *http.Response, securityHeaders map[string]string) {
	if apc.headersSent.CompareAndSwap(false, true) {
		apc.clientWriter.WriteHeader(resp.StatusCode)

		for name, values := range resp.Header {
			for _, value := range values {
				apc.clientWriter.Header().Add(name, value)
			}
		}

		for name, value := range securityHeaders {
			apc.clientWriter.Header().Set(name, value)
		}

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

	timeout := getTimeoutForEndpoint(prefix)
	asyncCtx := NewAsyncProxyContext(c, timeout, h.errorCount, h.requestCount)
	defer asyncCtx.cancel()

	atomic.AddInt64(h.requestCount, 1)

	// 异步记录请求
	go h.statsRecorder.RecordRequest(prefix)

	securityHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"X-Accel-Buffering":      "no",
	}

	if c.Request.Method == "OPTIONS" {
		c.Status(http.StatusNoContent)
		return
	}

	go func() {
		defer asyncCtx.cancel()
		if err := h.handleAsyncAPIRequest(asyncCtx, c, prefix, rest, securityHeaders); err != nil {
			log.Printf("Async API request error: %v", err)
			atomic.AddInt64(h.errorCount, 1)
		}
	}()

	<-asyncCtx.ctx.Done()
	h.updateResponseMetrics(asyncCtx.startTime)
}

// handleAsyncAPIRequest 异步处理API请求
func (h *Handler) handleAsyncAPIRequest(asyncCtx *AsyncProxyContext, c *gin.Context, prefix, rest string, securityHeaders map[string]string) error {
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

		// 特殊处理 gnothink 端点
		if prefix == "/gnothink" && c.Request.Method == "POST" &&
			strings.Contains(c.GetHeader("Content-Type"), "application/json") {
			if bodyJSON, err := fastJSONPatch(bodyBytes); err == nil {
				bodyBytes = bodyJSON
			}
		}

		body = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(asyncCtx.ctx, c.Request.Method, targetURL, body)
	if err != nil {
		return err
	}

	// 复制请求头
	commonHeaders := []string{"content-type", "authorization", "accept", "anthropic-version"}
	for _, header := range commonHeaders {
		if value := c.GetHeader(header); value != "" {
			req.Header.Set(header, value)
		}
	}

	for name, values := range c.Request.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-") {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
	}

	// API特殊处理
	if prefix == "/claude" && req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Go-API-Proxy/1.0")
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	asyncCtx.WriteHeaders(resp, securityHeaders)

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

// HandleProxy 处理网页代理请求
func (h *Handler) HandleProxy(c *gin.Context) {
	targetURLString := strings.TrimPrefix(c.Request.URL.Path, "/proxy/")
	if !strings.HasPrefix(targetURLString, "http") {
		c.String(400, "Invalid proxy URL. Must start with http:// or https:// after /proxy/")
		return
	}

	targetURL, err := url.Parse(targetURLString)
	if err != nil {
		c.String(400, "Invalid URL format")
		return
	}

	asyncCtx := NewAsyncProxyContext(c, 120*time.Second, h.errorCount, h.requestCount)
	defer asyncCtx.cancel()

	securityHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer-when-downgrade",
		"X-Frame-Options":        "",
		"X-Accel-Buffering":      "no",
	}

	if c.Request.Method == "OPTIONS" {
		c.Status(http.StatusNoContent)
		return
	}

	go func() {
		defer asyncCtx.cancel()
		if err := h.handleAsyncProxyRequest(asyncCtx, c, targetURL, securityHeaders); err != nil {
			log.Printf("Async proxy request error: %v", err)
		}
	}()

	<-asyncCtx.ctx.Done()
}

// handleAsyncProxyRequest 异步处理代理请求
func (h *Handler) handleAsyncProxyRequest(asyncCtx *AsyncProxyContext, c *gin.Context, targetURL *url.URL, securityHeaders map[string]string) error {
	var body io.Reader
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return err
		}
		body = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(asyncCtx.ctx, c.Request.Method, targetURL.String(), body)
	if err != nil {
		return err
	}

	allowedHeaders := []string{"accept", "content-type", "authorization", "user-agent",
		"accept-encoding", "accept-language", "cache-control", "pragma", "x-requested-with",
		"range", "if-range", "if-modified-since", "if-none-match"}

	for _, header := range allowedHeaders {
		if value := c.GetHeader(header); value != "" {
			req.Header.Set(header, value)
		}
	}

	if referer := c.GetHeader("referer"); referer != "" {
		req.Header.Set("referer", strings.Replace(referer,
			c.Request.URL.Scheme+"://"+c.Request.Host, targetURL.Scheme+"://"+targetURL.Host, 1))
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	asyncCtx.WriteHeaders(resp, securityHeaders)

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		return handleHTMLStreamRewrite(asyncCtx, resp, targetURL)
	}
	return streamResponseBody(asyncCtx, resp)
}

// handleHTMLStreamRewrite 异步HTML流式重写
func handleHTMLStreamRewrite(asyncCtx *AsyncProxyContext, resp *http.Response, targetURL *url.URL) error {
	scheme := "http"
	if asyncCtx.clientWriter.Header().Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	proxyBase := scheme + "://" + asyncCtx.clientWriter.Header().Get("Host") + "/proxy/"

	rewriter := &AsyncHTMLRewriter{
		asyncCtx:  asyncCtx,
		targetURL: targetURL,
		proxyBase: proxyBase,
		buffer:    make([]byte, 0, 4096),
	}

	contentLength := resp.ContentLength
	var bufferSize int
	if contentLength > 0 {
		if contentLength < 1024*1024 {
			bufferSize = 4 * 1024
		} else if contentLength < 10*1024*1024 {
			bufferSize = 16 * 1024
		} else {
			bufferSize = 64 * 1024
		}
	} else {
		bufferSize = 8 * 1024
	}

	buffer := make([]byte, bufferSize)

	for {
		select {
		case <-asyncCtx.ctx.Done():
			rewriter.Flush()
			return asyncCtx.ctx.Err()
		default:
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				if writeErr := rewriter.Write(buffer[:n]); writeErr != nil {
					return writeErr
				}
			}

			if err != nil {
				if err == io.EOF {
					rewriter.Flush()
					return nil
				}
				return err
			}
		}
	}
}

// AsyncHTMLRewriter 异步HTML重写器
type AsyncHTMLRewriter struct {
	asyncCtx  *AsyncProxyContext
	targetURL *url.URL
	proxyBase string
	buffer    []byte
}

// Write 实现异步HTML重写
func (h *AsyncHTMLRewriter) Write(data []byte) error {
	h.buffer = append(h.buffer, data...)

	processed := h.processBuffer()
	if len(processed) > 0 {
		return h.asyncCtx.StreamData(processed)
	}

	return nil
}

// processBuffer 处理HTML缓冲区
func (h *AsyncHTMLRewriter) processBuffer() []byte {
	if len(h.buffer) == 0 {
		return nil
	}

	baseURL := h.targetURL.Scheme + "://" + h.targetURL.Host
	content := string(h.buffer)

	patterns := []struct{ old, new string }{
		{`href="` + baseURL, `href="` + h.proxyBase + baseURL},
		{`src="` + baseURL, `src="` + h.proxyBase + baseURL},
		{`action="` + baseURL, `action="` + h.proxyBase + baseURL},
		{`href='` + baseURL, `href='` + h.proxyBase + baseURL},
		{`src='` + baseURL, `src='` + h.proxyBase + baseURL},
		{`action='` + baseURL, `action='` + h.proxyBase + baseURL},
		{`url("` + baseURL, `url("` + h.proxyBase + baseURL},
		{`url('` + baseURL, `url('` + h.proxyBase + baseURL},
	}

	for _, pattern := range patterns {
		content = strings.ReplaceAll(content, pattern.old, pattern.new)
	}

	keepSize := 1024
	if len(h.buffer) > 32*1024 {
		keepSize = 2048
	} else if len(h.buffer) < 4*1024 {
		keepSize = 512
	}

	if len(h.buffer) > keepSize {
		processed := []byte(content[:len(content)-keepSize])
		h.buffer = []byte(content[len(content)-keepSize:])
		return processed
	}

	return nil
}

// Flush 刷新剩余内容
func (h *AsyncHTMLRewriter) Flush() {
	if len(h.buffer) > 0 {
		baseURL := h.targetURL.Scheme + "://" + h.targetURL.Host
		content := string(h.buffer)

		patterns := []struct{ old, new string }{
			{`href="` + baseURL, `href="` + h.proxyBase + baseURL},
			{`src="` + baseURL, `src="` + h.proxyBase + baseURL},
			{`action="` + baseURL, `action="` + h.proxyBase + baseURL},
			{`href='` + baseURL, `href='` + h.proxyBase + baseURL},
			{`src='` + baseURL, `src='` + h.proxyBase + baseURL},
			{`action='` + baseURL, `action='` + h.proxyBase + baseURL},
		}

		for _, pattern := range patterns {
			content = strings.ReplaceAll(content, pattern.old, pattern.new)
		}

		h.asyncCtx.StreamData([]byte(content))
		h.buffer = nil
	}
}

// updateResponseMetrics 更新响应指标
func (h *Handler) updateResponseMetrics(startTime time.Time) {
	if h.statsCollector != nil {
		responseTime := time.Since(startTime).Milliseconds()
		h.statsCollector.UpdateResponseMetrics(responseTime)
	}
}

// fastJSONPatch 快速JSON补丁处理
func fastJSONPatch(bodyBytes []byte) ([]byte, error) {
	var bodyJSON map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &bodyJSON); err != nil {
		return bodyBytes, err
	}

	if generationConfig, ok := bodyJSON["generationConfig"].(map[string]interface{}); ok {
		generationConfig["thinkingConfig"] = map[string]interface{}{
			"thinkingBudget": 0,
		}
	} else {
		bodyJSON["generationConfig"] = map[string]interface{}{
			"thinkingConfig": map[string]interface{}{
				"thinkingBudget": 0,
			},
		}
	}
	return json.Marshal(bodyJSON)
}

// getTimeoutForEndpoint 根据API端点返回合适的超时时间
func getTimeoutForEndpoint(prefix string) time.Duration {
	aiEndpoints := map[string]time.Duration{
		"/openai":     1800 * time.Second,
		"/claude":     1800 * time.Second,
		"/gemini":     1800 * time.Second,
		"/gnothink":   1800 * time.Second,
		"/groq":       1800 * time.Second,
		"/xai":        1800 * time.Second,
		"/cohere":     1800 * time.Second,
		"/together":   1800 * time.Second,
		"/fireworks":  1800 * time.Second,
		"/openrouter": 1800 * time.Second,
		"/cerebras":   1800 * time.Second,
	}

	if timeout, exists := aiEndpoints[prefix]; exists {
		return timeout
	}

	return 60 * time.Second
}
