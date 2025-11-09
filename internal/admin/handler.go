package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const adminSessionCookie = "api_proxy_admin"

// MappingManager 映射管理器接口
type MappingManager interface {
	GetAllMappings() map[string]string
	GetMapping(ctx context.Context, prefix string) (string, error)
	AddMapping(ctx context.Context, prefix, target string) error
	UpdateMapping(ctx context.Context, prefix, target string) error
	DeleteMapping(ctx context.Context, prefix string) error
	ForceReload(ctx context.Context) error
	Count() int
	GetPrefixes() []string
	IsInitialized() bool
	GetVersion() int64
}

// Handler 管理接口处理器（DIP原则：依赖注入）
type Handler struct {
	mapper     MappingManager
	adminToken string
}

// NewHandler 创建管理接口处理器
func NewHandler(mapper MappingManager) *Handler {
	return &Handler{
		mapper:     mapper,
		adminToken: os.Getenv("ADMIN_TOKEN"), // 初始化时读取，避免每次请求都读取
	}
}

// authMiddleware Token认证中间件
func (h *Handler) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.adminToken == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Admin functionality is disabled (ADMIN_TOKEN not set)",
			})
			c.Abort()
			return
		}

		token := h.getSessionToken(c)
		if token == "" || token != h.adminToken {
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
func (h *Handler) handleGetAllMappings(c *gin.Context) {
	mappings := h.mapper.GetAllMappings()

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"count":    len(mappings),
		"mappings": mappings,
		"version":  h.mapper.GetVersion(),
	})
}

// handleGetPublicMappings 返回所有映射(公开访问,只读)
// 用于前端页面动态加载端点列表
func (h *Handler) handleGetPublicMappings(c *gin.Context) {
	mappings := h.mapper.GetAllMappings()

	// 转换为前端需要的格式: {"/prefix": "https://target"}
	publicMappings := make(map[string]string)
	for prefix, target := range mappings {
		publicMappings[prefix] = target
	}

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"count":    len(publicMappings),
		"mappings": publicMappings,
	})
}

// MappingRequest 映射请求体
type MappingRequest struct {
	Prefix string `json:"prefix" binding:"required"`
	Target string `json:"target" binding:"required"`
}

// handleAddMapping 添加新映射
func (h *Handler) handleAddMapping(c *gin.Context) {
	var req MappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	ctx := c.Request.Context()
	if err := h.mapper.AddMapping(ctx, req.Prefix, req.Target); err != nil {
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
func (h *Handler) handleUpdateMapping(c *gin.Context) {
	prefix, err := extractPrefixParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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
	if err := h.mapper.UpdateMapping(ctx, prefix, req.Target); err != nil {
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
func (h *Handler) handleDeleteMapping(c *gin.Context) {
	prefix, err := extractPrefixParam(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	if err := h.mapper.DeleteMapping(ctx, prefix); err != nil {
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

// handleForceReload 强制重新加载映射(用于多实例同步)
// handleForceReload 强制重新加载映射(用于多实例同步)
func (h *Handler) handleForceReload(c *gin.Context) {
	ctx := c.Request.Context()

	if err := h.mapper.ForceReload(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("Failed to reload mappings: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Mappings reloaded successfully",
		"version": h.mapper.GetVersion(),
	})
}

// handleAdminPage 管理页面
func (h *Handler) handleAdminPage(c *gin.Context) {
	c.File("web/templates/admin.html")
}

// handleAdminLogin 验证Token（用于前端登录）
func (h *Handler) handleAdminLogin(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body",
		})
		return
	}

	if h.adminToken == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Admin functionality is disabled",
		})
		return
	}

	if req.Token != h.adminToken {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid token",
		})
		return
	}

	h.setSessionCookie(c)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Authentication successful",
	})
}

func (h *Handler) handleAdminLogout(c *gin.Context) {
	h.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// SetupRoutes 设置管理路由
func (h *Handler) SetupRoutes(r *gin.Engine) {
	// 管理页面 (无需认证,页面内验证)
	r.GET("/admin", h.handleAdminPage)

	// 登录验证接口
	r.POST("/api/admin/login", h.handleAdminLogin)
	r.POST("/api/admin/logout", h.handleAdminLogout)

	// 公开只读映射API (无需认证,用于前端页面)
	r.GET("/api/public/mappings", h.handleGetPublicMappings)

	// 管理API (需要Token认证)
	adminAPI := r.Group("/api/mappings")
	adminAPI.Use(h.authMiddleware())
	{
		adminAPI.GET("", h.handleGetAllMappings)           // 获取所有映射
		adminAPI.POST("", h.handleAddMapping)              // 添加映射
		adminAPI.PUT("/*prefix", h.handleUpdateMapping)    // 更新映射
		adminAPI.DELETE("/*prefix", h.handleDeleteMapping) // 删除映射
		adminAPI.POST("/reload", h.handleForceReload)      // 强制重载映射
	}
}

func extractPrefixParam(c *gin.Context) (string, error) {
	prefix := strings.TrimSpace(c.Param("prefix"))
	if prefix == "" {
		return "", fmt.Errorf("prefix parameter is required")
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return prefix, nil
}

func (h *Handler) setSessionCookie(c *gin.Context) {
	value := url.QueryEscape(h.adminToken)
	cookie := &http.Cookie{
		Name:     adminSessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(12 * time.Hour),
		MaxAge:   int((12 * time.Hour).Seconds()),
	}
	http.SetCookie(c.Writer, cookie)
}

func (h *Handler) clearSessionCookie(c *gin.Context) {
	cookie := &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	}
	http.SetCookie(c.Writer, cookie)
}

func (h *Handler) getSessionToken(c *gin.Context) string {
	value, err := c.Cookie(adminSessionCookie)
	if err != nil {
		return ""
	}
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}
