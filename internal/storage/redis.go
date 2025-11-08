package storage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// Redis键名
	KeyMappings = "apiproxy:mappings"
	KeyVersion  = "apiproxy:version"

	// 缓存配置
	CacheTTL     = 30 * time.Second
	ReloadPeriod = 10 * time.Second
)

// MappingManager 管理API映射的核心结构
type MappingManager struct {
	client      *redis.Client
	cache       sync.Map // prefix -> target URL 的本地缓存
	version     atomic.Int64
	lastReload  time.Time
	mu          sync.RWMutex
	initialized atomic.Bool
}

// parseRedisURL 解析Redis URL格式
// 支持格式:
//   - redis://[username]:password@host:port/db  (标准Redis)
//   - rediss://[username]:password@host:port/db (Redis over TLS)
//
// 示例:
//   - redis://:mypassword@localhost:6379/0
//   - rediss://:mypassword@secure-redis.example.com:6380/0
func parseRedisURL(redisURL string) (*redis.Options, error) {
	// 默认配置
	opts := &redis.Options{
		Addr:         "localhost:6379",
		Password:     "",
		DB:           0,
		DialTimeout:  30 * time.Second, // 增加到30秒，适应云服务
		ReadTimeout:  10 * time.Second, // 增加到10秒
		WriteTimeout: 10 * time.Second, // 增加到10秒
		PoolSize:     10,
		MinIdleConns: 2,
	}

	if redisURL == "" {
		return opts, nil
	}

	// 解析URL
	parsedURL, err := url.Parse(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL format: %w", err)
	}

	// 检查协议 (支持 redis:// 和 rediss://)
	if parsedURL.Scheme != "redis" && parsedURL.Scheme != "rediss" {
		return nil, fmt.Errorf("invalid Redis URL scheme: %s (expected 'redis' or 'rediss')", parsedURL.Scheme)
	}

	// 如果是 rediss:// 协议,启用TLS
	if parsedURL.Scheme == "rediss" {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	// 解析主机和端口
	if parsedURL.Host != "" {
		opts.Addr = parsedURL.Host
	}

	// 解析密码
	if parsedURL.User != nil {
		if password, ok := parsedURL.User.Password(); ok {
			opts.Password = password
		}
	}

	// 解析数据库编号
	if parsedURL.Path != "" && parsedURL.Path != "/" {
		dbStr := strings.TrimPrefix(parsedURL.Path, "/")
		if db, err := strconv.Atoi(dbStr); err == nil {
			opts.DB = db
		}
	}

	return opts, nil
}

// NewMappingManager 创建并初始化映射管理器
func NewMappingManager(ctx context.Context) (*MappingManager, error) {
	// 读取Redis URL
	redisURL := os.Getenv("API_PROXY_REDIS_URL")
	if redisURL == "" {
		return nil, fmt.Errorf("API_PROXY_REDIS_URL environment variable is required\n" +
			"Example: API_PROXY_REDIS_URL=redis://:password@localhost:6379/0")
	}

	// 解析Redis配置
	opts, err := parseRedisURL(redisURL)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)

	// 测试连接
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("Redis connection failed: %w", err)
	}

	manager := &MappingManager{
		client:     client,
		lastReload: time.Now(),
	}

	// 首次加载映射
	if err := manager.reloadMappings(ctx); err != nil {
		return nil, fmt.Errorf("failed to load initial mappings: %w", err)
	}

	manager.initialized.Store(true)

	// 启动后台重载协程
	go manager.backgroundReloader()

	log.Printf("✅ MappingManager initialized: %d mappings loaded from Redis", manager.Count())

	return manager, nil
}

// reloadMappings 从Redis重新加载所有映射到缓存
func (m *MappingManager) reloadMappings(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 从Redis获取所有映射
	mappings, err := m.client.HGetAll(ctx, KeyMappings).Result()
	if err != nil {
		return err
	}

	// 如果Redis为空,返回错误(需要先初始化)
	if len(mappings) == 0 {
		return errors.New("no mappings found in Redis, please run init script first")
	}

	// 检查是否有变化
	hasChanges := false
	currentCount := 0
	m.cache.Range(func(key, value interface{}) bool {
		currentCount++
		prefix := key.(string)
		currentTarget := value.(string)

		// 检查是否被删除或修改
		if newTarget, exists := mappings[prefix]; !exists || newTarget != currentTarget {
			hasChanges = true
			return false // 提前退出
		}
		return true
	})

	// 检查是否有新增
	if !hasChanges && len(mappings) != currentCount {
		hasChanges = true
	}

	// 如果没有变化，跳过更新
	if !hasChanges {
		m.lastReload = time.Now()
		return nil
	}

	// 清空旧缓存
	m.cache.Range(func(key, value interface{}) bool {
		m.cache.Delete(key)
		return true
	})

	// 加载新映射到缓存
	for prefix, target := range mappings {
		m.cache.Store(prefix, target)
	}

	// 只有在有变化时才更新版本号
	m.version.Add(1)
	m.lastReload = time.Now()

	log.Printf("📦 Reloaded %d mappings from Redis (version: %d)", len(mappings), m.version.Load())

	return nil
}

// backgroundReloader 后台定期重载映射
func (m *MappingManager) backgroundReloader() {
	ticker := time.NewTicker(ReloadPeriod)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := m.reloadMappings(ctx); err != nil {
			log.Printf("⚠️  Background reload failed: %v", err)
		}
		cancel()
	}
}

// GetMapping 获取指定前缀的目标URL
func (m *MappingManager) GetMapping(ctx context.Context, prefix string) (string, error) {
	// 从缓存读取
	if target, ok := m.cache.Load(prefix); ok {
		return target.(string), nil
	}

	// 缓存未命中,从Redis读取
	target, err := m.client.HGet(ctx, KeyMappings, prefix).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("mapping not found for prefix: %s", prefix)
	}
	if err != nil {
		return "", err
	}

	// 更新缓存
	m.cache.Store(prefix, target)

	return target, nil
}

// GetAllMappings 获取所有映射
func (m *MappingManager) GetAllMappings() map[string]string {
	result := make(map[string]string)

	m.cache.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(string)
		return true
	})

	return result
}

// AddMapping 添加新的API映射
func (m *MappingManager) AddMapping(ctx context.Context, prefix, target string) error {
	// 验证输入
	if err := validateMapping(prefix, target); err != nil {
		return err
	}

	// 检查是否已存在
	exists, err := m.client.HExists(ctx, KeyMappings, prefix).Result()
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("mapping already exists for prefix: %s", prefix)
	}

	// 添加到Redis
	if err := m.client.HSet(ctx, KeyMappings, prefix, target).Err(); err != nil {
		return err
	}

	// 更新缓存
	m.cache.Store(prefix, target)
	m.version.Add(1)

	log.Printf("[AUDIT] Added mapping: %s -> %s", prefix, target)

	return nil
}

// UpdateMapping 更新现有映射
func (m *MappingManager) UpdateMapping(ctx context.Context, prefix, target string) error {
	// 验证输入
	if err := validateMapping(prefix, target); err != nil {
		return err
	}

	// 检查是否存在
	exists, err := m.client.HExists(ctx, KeyMappings, prefix).Result()
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("mapping not found for prefix: %s", prefix)
	}

	// 更新Redis
	if err := m.client.HSet(ctx, KeyMappings, prefix, target).Err(); err != nil {
		return err
	}

	// 更新缓存
	m.cache.Store(prefix, target)
	m.version.Add(1)

	log.Printf("[AUDIT] Updated mapping: %s -> %s", prefix, target)

	return nil
}

// DeleteMapping 删除映射
func (m *MappingManager) DeleteMapping(ctx context.Context, prefix string) error {
	// 检查是否存在
	exists, err := m.client.HExists(ctx, KeyMappings, prefix).Result()
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("mapping not found for prefix: %s", prefix)
	}

	// 从Redis删除
	if err := m.client.HDel(ctx, KeyMappings, prefix).Err(); err != nil {
		return err
	}

	// 从缓存删除
	m.cache.Delete(prefix)
	m.version.Add(1)

	log.Printf("[AUDIT] Deleted mapping: %s", prefix)

	return nil
}

// Count 返回映射数量
func (m *MappingManager) Count() int {
	count := 0
	m.cache.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// GetPrefixes 获取所有前缀列表
func (m *MappingManager) GetPrefixes() []string {
	var prefixes []string

	m.cache.Range(func(key, value interface{}) bool {
		prefixes = append(prefixes, key.(string))
		return true
	})

	return prefixes
}

// IsInitialized 检查是否已初始化
func (m *MappingManager) IsInitialized() bool {
	return m.initialized.Load()
}

// GetVersion 获取当前版本号
func (m *MappingManager) GetVersion() int64 {
	return m.version.Load()
}

// Close 关闭Redis连接
func (m *MappingManager) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// validateMapping 验证映射的有效性
func validateMapping(prefix, target string) error {
	// 验证前缀格式
	if prefix == "" {
		return errors.New("prefix cannot be empty")
	}

	if !strings.HasPrefix(prefix, "/") {
		return errors.New("prefix must start with /")
	}

	if strings.Contains(prefix, " ") {
		return errors.New("prefix cannot contain spaces")
	}

	// 验证目标URL
	if target == "" {
		return errors.New("target URL cannot be empty")
	}

	parsedURL, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return errors.New("target URL must use http or https scheme")
	}

	if parsedURL.Host == "" {
		return errors.New("target URL must have a valid host")
	}

	return nil
}
