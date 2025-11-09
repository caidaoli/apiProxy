// Go 版本的高性能 API 代理服务器
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"api-proxy/internal/admin"
	"api-proxy/internal/proxy"
	"api-proxy/internal/stats"
	"api-proxy/internal/storage"
)

func main() {
	// 加载 .env 文件
	// 优先加载根目录的.env，如果不存在则尝试deployments/config/.env.example
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

	// 初始化统计系统（复用Redis连接）
	statsCollector := stats.NewCollector(mappingManager.GetClient())

	// 创建代理处理器
	proxyHandler := proxy.NewHandler(
		mappingManager,
		statsCollector,
		statsCollector,
		statsCollector.GetErrorCount(),
		statsCollector.GetRequestCount(),
	)

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
	r.GET("/stats", statsCollector.HandleStats)

	// 管理路由
	admin.SetupRoutes(r, mappingManager)

	// API代理路由 - 动态注册所有映射
	prefixes := mappingManager.GetPrefixes()
	for _, prefix := range prefixes {
		r.Any(prefix+"/*path", proxyHandler.HandleAPIProxy)
	}

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
	log.Printf("🔧 访问 http://localhost:%s/admin 管理API映射", port)

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

	// 保存统计数据到Redis
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer saveCancel() // ✅ 修复: 确保context资源释放,即使发生panic
	if err := statsCollector.SaveToRedis(saveCtx); err != nil {
		log.Printf("❌ 关闭时保存统计数据失败: %v", err)
	} else {
		log.Println("💾 统计数据已保存到Redis")
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
