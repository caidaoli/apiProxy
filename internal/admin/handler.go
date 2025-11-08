package admin

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// MappingManager 映射管理器接口
type MappingManager interface {
	GetAllMappings() map[string]string
	GetMapping(ctx context.Context, prefix string) (string, error)
	AddMapping(ctx context.Context, prefix, target string) error
	UpdateMapping(ctx context.Context, prefix, target string) error
	DeleteMapping(ctx context.Context, prefix string) error
	Count() int
	GetPrefixes() []string
	IsInitialized() bool
	GetVersion() int64
}

// 全局变量
var mappingManager MappingManager

// AdminAuthMiddleware Token认证中间件
func AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		expectedToken := os.Getenv("ADMIN_TOKEN")
		if expectedToken == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Admin functionality is disabled (ADMIN_TOKEN not set)",
			})
			c.Abort()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Missing Authorization header",
			})
			c.Abort()
			return
		}

		// 支持 "Bearer <token>" 或直接 "<token>"
		token := strings.TrimPrefix(authHeader, "Bearer ")
		token = strings.TrimSpace(token)

		if token != expectedToken {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid admin token",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// handleGetAllMappings 获取所有API映射
func handleGetAllMappings(c *gin.Context) {
	mappings := mappingManager.GetAllMappings()

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"count":    len(mappings),
		"mappings": mappings,
		"version":  mappingManager.GetVersion(),
	})
}

// MappingRequest 映射请求体
type MappingRequest struct {
	Prefix string `json:"prefix" binding:"required"`
	Target string `json:"target" binding:"required"`
}

// handleAddMapping 添加新映射
func handleAddMapping(c *gin.Context) {
	var req MappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	ctx := c.Request.Context()
	if err := mappingManager.AddMapping(ctx, req.Prefix, req.Target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Mapping added successfully",
		"mapping": gin.H{
			"prefix": req.Prefix,
			"target": req.Target,
		},
	})
}

// handleUpdateMapping 更新映射
func handleUpdateMapping(c *gin.Context) {
	prefix := "/" + c.Param("prefix")

	var req struct {
		Target string `json:"target" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	ctx := c.Request.Context()
	if err := mappingManager.UpdateMapping(ctx, prefix, req.Target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Mapping updated successfully",
		"mapping": gin.H{
			"prefix": prefix,
			"target": req.Target,
		},
	})
}

// handleDeleteMapping 删除映射
func handleDeleteMapping(c *gin.Context) {
	prefix := "/" + c.Param("prefix")

	ctx := c.Request.Context()
	if err := mappingManager.DeleteMapping(ctx, prefix); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Mapping deleted successfully",
		"prefix":  prefix,
	})
}

// handleAdminPage 管理页面
func handleAdminPage(c *gin.Context) {
	c.File("web/templates/admin.html")
}

// handleAdminLogin 验证Token（用于前端登录）
func handleAdminLogin(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body",
		})
		return
	}

	expectedToken := os.Getenv("ADMIN_TOKEN")
	if expectedToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Admin functionality is disabled",
		})
		return
	}

	if req.Token != expectedToken {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid token",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Authentication successful",
	})
}

// SetMappingManager 设置映射管理器
func SetMappingManager(mm MappingManager) {
	mappingManager = mm
}

// SetupRoutes 设置管理路由
func SetupRoutes(r *gin.Engine, mm MappingManager) {
	mappingManager = mm

	// 管理页面 (无需认证,页面内验证)
	r.GET("/admin", handleAdminPage)

	// 登录验证接口
	r.POST("/api/admin/login", handleAdminLogin)

	// 管理API (需要Token认证)
	adminAPI := r.Group("/api/mappings")
	adminAPI.Use(AdminAuthMiddleware())
	{
		adminAPI.GET("", handleGetAllMappings)           // 获取所有映射
		adminAPI.POST("", handleAddMapping)              // 添加映射
		adminAPI.PUT("/:prefix", handleUpdateMapping)    // 更新映射
		adminAPI.DELETE("/:prefix", handleDeleteMapping) // 删除映射
	}
}
