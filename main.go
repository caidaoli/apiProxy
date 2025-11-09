// Go 版本的高性能 API 代理服务器
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"api-proxy/internal/admin"
	"api-proxy/internal/middleware"
	"api-proxy/internal/proxy"
	"api-proxy/internal/stats"
	"api-proxy/internal/storage"
)

func main() {
	// 加载 .env 文件
	if err := godotenv.Load(); err != nil {
		if err := godotenv.Load("deployments/config/.env.example"); err != nil {
			log.Println("⚠️  未找到 .env 文件,将使用系统环境变量")
		} else {
			log.Println("✅ 已加载 deployments/config/.env.example 示例配置")
		}
	} else {
		log.Println("✅ 已加载根目录 .env 文件")
	}

	// 设置生产模式
	gin.SetMode(gin.ReleaseMode)

	// 初始化Redis映射管理器
	ctx := context.Background()
	mappingManager, err := storage.NewMappingManager(ctx)
	if err != nil {
		log.Fatalf("❌ Failed to initialize mapping manager: %v\n"+
			"💡 Please ensure:\n"+
			"   1. Redis is running and accessible\n"+
			"   2. REDIS_ADDR environment variable is set correctly\n"+
			"   3. Redis contains initialized mappings (run init script if needed)\n", err)
	}
	defer mappingManager.Close()

	// 创建统计收集器
	statsCollector := stats.NewCollector(mappingManager.GetClient())
	defer statsCollector.Close()

	// 创建透明代理
	transparentProxy := proxy.NewTransparentProxy(mappingManager)

	// 创建路由
	r := gin.New()

	// 添加日志中间件
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("[%s] %s - \"%s %s %s\" %d %s %d %s \"%s\"\n",
			param.TimeStamp.Format("2006/01/02 - 15:04:05"),
			param.ClientIP,
			param.Method,
			param.Path,
			param.Request.Proto,
			param.StatusCode,
			param.Latency,
			param.BodySize,
			param.ErrorMessage,
			param.Request.UserAgent(),
		)
	}))

	// 添加恢复中间件
	r.Use(gin.Recovery())

	// 可选：添加统计中间件
	if os.Getenv("ENABLE_STATS") != "false" {
		statsMiddleware := middleware.NewStatsMiddleware(statsCollector)
		r.Use(statsMiddleware.Handler())
	}

	// 基础路由
	r.GET("/", handleIndex)
	r.GET("/index.html", handleIndex)
	r.GET("/robots.txt", handleRobotsTxt)
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.File("web/static/images/favicon.svg")
	})

	// 静态文件服务
	r.Static("/static", "./web/static")

	// 统计API路由
	r.GET("/stats", func(c *gin.Context) {
		stats := statsCollector.GetStats()
		requests := statsCollector.GetRequests()
		performance := statsCollector.GetPerformanceMetrics()

		c.JSON(200, gin.H{
			"total":          statsCollector.GetRequestCount(),
			"errors":         statsCollector.GetErrorCount(),
			"dropped_events": statsCollector.GetDroppedEvents(),
			"avg_response":   statsCollector.GetAverageResponseTime().String(),
			"endpoints":      stats,
			"requests":       requests,    // 新增:时间序列数据
			"performance":    performance, // 新增:性能指标
		})
	})

	// 管理路由（依赖注入，无全局变量）
	adminHandler := admin.NewHandler(mappingManager)
	adminHandler.SetupRoutes(r)

	// API代理路由 - 使用通配符动态匹配所有路径
	// 注意: 必须放在最后,避免覆盖其他路由
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		prefixes := mappingManager.GetPrefixes()
		if prefix, ok := findMatchingPrefix(path, prefixes); ok {
			remainingPath := remainingPathAfterPrefix(path, prefix)
			if err := transparentProxy.ProxyRequest(c.Writer, c.Request, prefix, remainingPath); err != nil {
				log.Printf("Proxy error for %s: %v", path, err)
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			return
		}

		// 没有匹配的映射
		c.JSON(404, gin.H{
			"error":   "No mapping found for this path",
			"path":    path,
			"hint":    "Use POST /api/mappings to add a mapping",
			"example": map[string]string{"prefix": "/api", "target": "https://api.example.com"},
		})
	})

	// 启动服务器
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	log.Printf("🚀 API代理服务器已启动 端口:%s", port)
	log.Printf("📊 访问 http://localhost:%s 查看统计信息", port)
	log.Printf("🔧 访问 http://localhost:%s/admin 管理API映射", port)

	if os.Getenv("ENABLE_STATS") != "false" {
		log.Printf("📈 统计功能: 已启用 (可通过 ENABLE_STATS=false 禁用)")
	}

	// 使用自定义HTTP服务器
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

	// 保存统计数据到Redis（可选）
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer saveCancel()
	if err := statsCollector.SaveToRedis(saveCtx); err != nil {
		log.Printf("❌ 关闭时保存统计数据失败: %v", err)
	} else {
		log.Println("📊 统计数据已保存到Redis")
	}

	// 优雅关闭HTTP服务器
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("服务器强制关闭:", err)
	}

	log.Println("服务器已关闭")
}

// handleIndex 处理首页
func handleIndex(c *gin.Context) {
	c.File("web/templates/index.html")
}

// handleRobotsTxt 处理robots.txt
func handleRobotsTxt(c *gin.Context) {
	c.Header("Content-Type", "text/plain")
	c.String(200, "User-agent: *\nDisallow: /\n")
}

// findMatchingPrefix 返回最先匹配 path 的前缀(假设传入按长度排序)
func findMatchingPrefix(path string, prefixes []string) (string, bool) {
	for _, prefix := range prefixes {
		if matchesPrefix(path, prefix) {
			return prefix, true
		}
	}
	return "", false
}

func matchesPrefix(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	if prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	if strings.HasSuffix(prefix, "/") {
		return true
	}
	return path[len(prefix)] == '/'
}

func remainingPathAfterPrefix(path, prefix string) string {
	if len(path) < len(prefix) {
		return ""
	}
	remainder := path[len(prefix):]
	if remainder != "" && remainder[0] != '/' {
		remainder = "/" + remainder
	}
	return remainder
}
