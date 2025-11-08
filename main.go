// Go 版本的高性能 API 代理服务器
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// 全局变量
var (
	apiMapping = map[string]string{
		"/discord":     "https://discord.com/api",
		"/telegram":    "https://api.telegram.org",
		"/openai":      "https://api.openai.com",
		"/claude":      "https://api.anthropic.com",
		"/gemini":      "https://generativelanguage.googleapis.com",
		"/gnothink":    "https://generativelanguage.googleapis.com",
		"/meta":        "https://www.meta.ai/api",
		"/groq":        "https://api.groq.com/openai",
		"/xai":         "https://api.x.ai",
		"/cohere":      "https://api.cohere.ai",
		"/huggingface": "https://api-inference.huggingface.co",
		"/together":    "https://api.together.xyz",
		"/novita":      "https://api.novita.ai",
		"/portkey":     "https://api.portkey.ai",
		"/fireworks":   "https://api.fireworks.ai",
		"/openrouter":  "https://openrouter.ai/api",
		"/sophnet":     "https://sophnet.com/",
		"/cerebras":    "https://api.cerebras.ai",
	}
	httpClient = &http.Client{
		Timeout: 1800 * time.Second, // 增加到30分钟，适合长时间AI流式响应
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second, // 连接超时
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          256,               // 增加最大空闲连接数
			MaxIdleConnsPerHost:   256,               // 增加每个主机的最大空闲连接数
			IdleConnTimeout:       120 * time.Second, // 增加空闲连接超时
			ResponseHeaderTimeout: 60 * time.Second,  // 增加响应头超时
		},
	}
)

// extractPrefixAndRest 提取前缀和剩余路径
func extractPrefixAndRest(pathname string) (string, string) {
	for prefix := range apiMapping {
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
func NewAsyncProxyContext(c *gin.Context, timeout time.Duration) *AsyncProxyContext {
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
		errorCount:   &errorCount,
		requestCount: &requestCount,
	}
}

// WriteHeaders 写入响应头（只执行一次）
func (apc *AsyncProxyContext) WriteHeaders(resp *http.Response, securityHeaders map[string]string) {
	if apc.headersSent.CompareAndSwap(false, true) {
		// 设置响应状态码
		apc.clientWriter.WriteHeader(resp.StatusCode)

		// 复制响应头
		for name, values := range resp.Header {
			for _, value := range values {
				apc.clientWriter.Header().Add(name, value)
			}
		}

		// 添加安全头
		for name, value := range securityHeaders {
			apc.clientWriter.Header().Set(name, value)
		}

		// 立即刷新头部
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

	// 立即刷新到客户端
	if apc.flusher != nil {
		apc.flusher.Flush()
	}

	return nil
}

// handleAPIProxy 处理API代理请求（异步优化版）
func handleAPIProxy(c *gin.Context) {
	path := c.Request.URL.Path
	prefix, rest := extractPrefixAndRest(path)
	if prefix == "" {
		atomic.AddInt64(&errorCount, 1)
		c.JSON(404, gin.H{"error": "Not Found"})
		return
	}

	// 根据API端点设置合适的超时时间
	timeout := getTimeoutForEndpoint(prefix)
	asyncCtx := NewAsyncProxyContext(c, timeout)
	defer asyncCtx.cancel()

	atomic.AddInt64(&requestCount, 1)

	// 异步记录请求，避免阻塞主流程
	go stats.recordRequest(prefix)

	// 准备安全头
	securityHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"X-Accel-Buffering":      "no", // 禁用代理缓存
	}

	// 处理 OPTIONS 请求
	if c.Request.Method == "OPTIONS" {
		// 对于非浏览器请求，可以直接返回成功
		c.Status(http.StatusNoContent)
		return
	}

	// 异步发送请求并流式转发响应
	go func() {
		defer asyncCtx.cancel()

		if err := apc_handleAsyncAPIRequest(asyncCtx, c, prefix, rest, securityHeaders); err != nil {
			log.Printf("Async API request error: %v", err)
			atomic.AddInt64(&errorCount, 1)
		}
	}()

	// 等待异步处理完成或超时
	<-asyncCtx.ctx.Done()

	// 更新性能指标
	apc_updateResponseMetrics(asyncCtx.startTime)
}

// apc_handleAsyncAPIRequest 异步处理API请求
func apc_handleAsyncAPIRequest(asyncCtx *AsyncProxyContext, c *gin.Context, prefix, rest string, securityHeaders map[string]string) error {
	// 构建目标URL
	targetURL := apiMapping[prefix] + rest
	if c.Request.URL.RawQuery != "" {
		targetURL += "?" + c.Request.URL.RawQuery
	}

	// 准备请求体
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

	// 创建请求
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

	// 复制以 x- 开头的自定义头
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

	// 发送请求（使用支持取消的客户端）
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 立即写入响应头
	asyncCtx.WriteHeaders(resp, securityHeaders)

	// 检查响应状态码
	if resp.StatusCode >= 400 {
		atomic.AddInt64(&errorCount, 1)
	}

	// 流式转发响应体
	return apc_streamResponseBody(asyncCtx, resp)
}

// apc_streamResponseBody 流式转发响应体
func apc_streamResponseBody(asyncCtx *AsyncProxyContext, resp *http.Response) error {
	// 检测是否为流式响应
	contentType := resp.Header.Get("Content-Type")
	isStreaming := strings.Contains(contentType, "text/event-stream") ||
		strings.Contains(contentType, "application/stream+json") ||
		strings.Contains(contentType, "text/plain") // OpenAI的流式响应通常是text/plain

	var bufferSize int
	if isStreaming {
		bufferSize = 2 * 1024 // 2KB for streaming APIs (OpenAI, Claude等)
	} else if strings.Contains(contentType, "text/html") {
		bufferSize = 8 * 1024 // 8KB for HTML pages
	} else if strings.Contains(contentType, "application/json") {
		bufferSize = 16 * 1024 // 16KB for JSON APIs
	} else if strings.Contains(contentType, "image/") || strings.Contains(contentType, "video/") {
		bufferSize = 64 * 1024 // 64KB for media files
	} else {
		bufferSize = 32 * 1024 // 32KB for other content
	}

	buffer := make([]byte, bufferSize)

	for {
		select {
		case <-asyncCtx.ctx.Done():
			return asyncCtx.ctx.Err()
		default:
			// 对于流式响应，不设置严格的读取超时，让context控制整体超时
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				if streamErr := asyncCtx.StreamData(buffer[:n]); streamErr != nil {
					return streamErr
				}
			}

			if err != nil {
				if err == io.EOF {
					return nil // 正常结束
				}
				return err
			}
		}
	}
}

// handleProxy 处理网页代理请求（异步优化版）
func handleProxy(c *gin.Context) {
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

	// 创建异步代理上下文
	asyncCtx := NewAsyncProxyContext(c, 120*time.Second) // 2分钟超时，适合网页加载
	defer asyncCtx.cancel()

	// 准备安全头
	securityHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer-when-downgrade",
		"X-Frame-Options":        "", // 允许在iframe中嵌入
		"X-Accel-Buffering":      "no",
	}

	// 处理 OPTIONS 请求
	if c.Request.Method == "OPTIONS" {
		c.Status(http.StatusNoContent)
		return
	}

	// 异步发送请求并流式转发响应
	go func() {
		defer asyncCtx.cancel()

		if err := apc_handleAsyncProxyRequest(asyncCtx, c, targetURL, securityHeaders); err != nil {
			log.Printf("Async proxy request error: %v", err)
		}
	}()

	// 等待异步处理完成或超时
	<-asyncCtx.ctx.Done()
}

// apc_handleAsyncProxyRequest 异步处理代理请求
func apc_handleAsyncProxyRequest(asyncCtx *AsyncProxyContext, c *gin.Context, targetURL *url.URL, securityHeaders map[string]string) error {
	// 准备请求体
	var body io.Reader
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return err
		}
		body = bytes.NewReader(bodyBytes)
	}

	// 创建请求
	req, err := http.NewRequestWithContext(asyncCtx.ctx, c.Request.Method, targetURL.String(), body)
	if err != nil {
		return err
	}

	// 复制请求头
	allowedHeaders := []string{"accept", "content-type", "authorization", "user-agent",
		"accept-encoding", "accept-language", "cache-control", "pragma", "x-requested-with",
		"range", "if-range", "if-modified-since", "if-none-match"}

	for _, header := range allowedHeaders {
		if value := c.GetHeader(header); value != "" {
			req.Header.Set(header, value)
		}
	}

	// 处理 referer 头
	if referer := c.GetHeader("referer"); referer != "" {
		req.Header.Set("referer", strings.Replace(referer,
			c.Request.URL.Scheme+"://"+c.Request.Host, targetURL.Scheme+"://"+targetURL.Host, 1))
	}

	// 发送请求
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 立即写入响应头
	asyncCtx.WriteHeaders(resp, securityHeaders)

	// 根据内容类型选择处理方式
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		return apc_handleHTMLStreamRewrite(asyncCtx, resp, targetURL)
	} else {
		return apc_streamResponseBody(asyncCtx, resp)
	}
}

// apc_handleHTMLStreamRewrite 异步HTML流式重写
func apc_handleHTMLStreamRewrite(asyncCtx *AsyncProxyContext, resp *http.Response, targetURL *url.URL) error {
	// 创建HTML重写器
	scheme := "http"
	if asyncCtx.clientWriter.Header().Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	proxyBase := scheme + "://" + asyncCtx.clientWriter.Header().Get("Host") + "/proxy/"

	rewriter := &AsyncHTMLRewriter{
		asyncCtx:  asyncCtx,
		targetURL: targetURL,
		proxyBase: proxyBase,
		buffer:    make([]byte, 0, 4096), // 初始4KB，动态增长
	}

	// 根据Content-Length动态调整缓冲区大小
	contentLength := resp.ContentLength
	var bufferSize int
	if contentLength > 0 {
		if contentLength < 1024*1024 { // 小于1MB
			bufferSize = 4 * 1024 // 4KB
		} else if contentLength < 10*1024*1024 { // 小于10MB
			bufferSize = 16 * 1024 // 16KB
		} else {
			bufferSize = 64 * 1024 // 64KB for large pages
		}
	} else {
		bufferSize = 8 * 1024 // 默认8KB
	}

	// 流式处理HTML内容
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

	// 优化的URL重写模式 - 使用更高效的字符串替换
	patterns := []struct{ old, new string }{
		{`href="` + baseURL, `href="` + h.proxyBase + baseURL},
		{`src="` + baseURL, `src="` + h.proxyBase + baseURL},
		{`action="` + baseURL, `action="` + h.proxyBase + baseURL},
		{`href='` + baseURL, `href='` + h.proxyBase + baseURL},
		{`src='` + baseURL, `src='` + h.proxyBase + baseURL},
		{`action='` + baseURL, `action='` + h.proxyBase + baseURL},
		// 添加更多常见的URL模式
		{`url("` + baseURL, `url("` + h.proxyBase + baseURL},
		{`url('` + baseURL, `url('` + h.proxyBase + baseURL},
	}

	// 批量替换，减少字符串操作次数
	for _, pattern := range patterns {
		content = strings.ReplaceAll(content, pattern.old, pattern.new)
	}

	// 动态调整保留大小，根据缓冲区大小优化
	keepSize := 1024             // 默认保留1KB
	if len(h.buffer) > 32*1024 { // 如果缓冲区大于32KB
		keepSize = 2048 // 保留2KB，减少处理频率
	} else if len(h.buffer) < 4*1024 { // 如果缓冲区小于4KB
		keepSize = 512 // 只保留512B，更快处理
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

// apc_updateResponseMetrics 更新响应指标
func apc_updateResponseMetrics(startTime time.Time) {
	responseTime := time.Since(startTime).Milliseconds()

	// 使用原子操作累计响应时间和计数
	atomic.AddInt64(&responseTimeSum, responseTime)
	atomic.AddInt64(&responseTimeCount, 1)
}

// fastJSONPatch 快速JSON补丁处理
func fastJSONPatch(bodyBytes []byte) ([]byte, error) {
	var bodyJSON map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &bodyJSON); err != nil {
		return bodyBytes, err
	}

	// 添加 thinkingConfig
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
	// AI流式API需要更长的超时时间 - 30分钟
	aiEndpoints := map[string]time.Duration{
		"/openai":     1800 * time.Second, // 30分钟
		"/claude":     1800 * time.Second, // 30分钟
		"/gemini":     1800 * time.Second, // 30分钟
		"/gnothink":   1800 * time.Second, // 30分钟
		"/groq":       1800 * time.Second, // 30分钟
		"/xai":        1800 * time.Second, // 30分钟
		"/cohere":     1800 * time.Second, // 30分钟
		"/together":   1800 * time.Second, // 30分钟
		"/fireworks":  1800 * time.Second, // 30分钟
		"/openrouter": 1800 * time.Second, // 30分钟
		"/cerebras":   1800 * time.Second, // 30分钟
	}

	if timeout, exists := aiEndpoints[prefix]; exists {
		return timeout
	}

	// 其他API使用较短的超时时间
	return 60 * time.Second
}

// main 主函数
func main() {
	// 设置生产模式
	gin.SetMode(gin.ReleaseMode)

	// 初始化统计系统
	initStats()

	// 创建路由
	r := gin.New()

	// 添加日志中间件
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("[%s] %s - \"%s %s %s\" %d %s %d %s \"%s\"\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"), // 时间戳
			param.ClientIP,            // 客户端 IP
			param.Method,              // HTTP 方法
			param.Path,                // 请求路径
			param.Request.Proto,       // HTTP 协议 (e.g., HTTP/1.1)
			param.StatusCode,          // 状态码
			param.Latency,             // 处理延迟
			param.BodySize,            // 响应体大小
			param.ErrorMessage,        // 错误信息
			param.Request.UserAgent(), // User-Agent
		)
	}))

	// 添加恢复中间件
	r.Use(gin.Recovery())

	// 设置路由
	r.GET("/", handleIndex)
	r.GET("/index.html", handleIndex)
	r.GET("/robots.txt", handleRobotsTxt)
	r.GET("/stats", handleStats)

	// 静态文件服务
	r.Static("/static", "./static")

	// API代理路由
	for prefix := range apiMapping {
		r.Any(prefix+"/*path", handleAPIProxy)
	}

	// 网页代理路由
	r.Any("/proxy/*path", handleProxy)

	// 启动服务器
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	log.Printf("🚀 API代理服务器已启动 (Go优化版) 端口:%s", port)
	log.Printf("🕒 统计数据每分钟自动刷新页面")
	log.Printf("⚡ 性能优化：异步统计、内存优化、锁竞争减少")
	log.Printf("⏱️  超时配置：AI API 30分钟，其他API 1分钟，HTTP客户端 30分钟")
	log.Printf("📊 访问 http://localhost:%s 查看统计信息", port)

	// 使用自定义HTTP服务器以更好地控制
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// 启动服务器
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务器启动失败: %v", err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("正在关闭服务器...")

	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("服务器强制关闭:", err)
	}

	log.Println("服务器已关闭")
}

// handleIndex 处理首页
func handleIndex(c *gin.Context) {
	c.File("index.html")
}

// handleRobotsTxt 处理robots.txt
func handleRobotsTxt(c *gin.Context) {
	c.Header("Content-Type", "text/plain")
	c.String(200, "User-agent: *\nDisallow: /\n")
}
