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
	KeyMappings        = "apiproxy:mappings"
	KeyVersion         = "apiproxy:version"
	KeyMappingsVersion = "apiproxy:mappings:version" // 映射版本号
	KeyMappingsChannel = "apiproxy:mappings:updates" // Pub/Sub通道

	// 缓存配置
	CacheTTL     = 30 * time.Second
	ReloadPeriod = 10 * time.Second
)

// MappingManager 管理API映射的核心结构
type MappingManager struct {
	client *redis.Client

	// 使用 map + RWMutex 代替 sync.Map(读多写少场景更高效)
	mu    sync.RWMutex
	cache map[string]string

	// 使用原子操作保护的字段
	version     atomic.Int64
	lastReload  atomic.Int64 // Unix时间戳
	initialized atomic.Bool

	// Goroutine控制
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Pub/Sub订阅
	pubsub *redis.PubSub
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
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
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
		client:   client,
		cache:    make(map[string]string),
		stopChan: make(chan struct{}),
	}
	manager.lastReload.Store(time.Now().Unix())

	// 首次加载映射
	if err := manager.reloadMappings(ctx); err != nil {
		return nil, fmt.Errorf("failed to load initial mappings: %w", err)
	}

	manager.initialized.Store(true)

	// 订阅Redis Pub/Sub通道
	manager.pubsub = client.Subscribe(ctx, KeyMappingsChannel)

	// 启动后台协程
	manager.wg.Add(2)
	go manager.backgroundReloader()
	go manager.pubsubListener()

	log.Printf("✅ MappingManager initialized: %d mappings loaded from Redis", manager.Count())

	return manager, nil
}

// reloadMappings 从Redis重新加载所有映射到缓存
func (m *MappingManager) reloadMappings(ctx context.Context) error {
	// 先检查Redis版本号（不需要锁，快速检查）
	remoteVersion, err := m.client.Get(ctx, KeyMappingsVersion).Int64()
	if err != nil && err != redis.Nil {
		return err
	}

	// 如果版本号没变，直接返回（避免不必要的加载）
	currentVersion := m.version.Load()
	if remoteVersion > 0 && remoteVersion == currentVersion {
		m.lastReload.Store(time.Now().Unix())
		return nil
	}

	// 版本号变了，获取锁并重载
	m.mu.Lock()
	defer m.mu.Unlock()

	// 从Redis获取所有映射
	mappings, err := m.client.HGetAll(ctx, KeyMappings).Result()
	if err != nil {
		return err
	}

	// 如果Redis为空,记录警告但允许启动(可通过管理API动态添加)
	if len(mappings) == 0 {
		log.Println("⚠️  No mappings found in Redis. Use /admin API to add mappings.")
		log.Println("💡 Example: POST /admin/mappings with {\"prefix\":\"/api\",\"target\":\"https://api.example.com\"}")
		m.lastReload.Store(time.Now().Unix())
		return nil
	}

	// 双重检查（避免竞态条件）
	if remoteVersion > 0 && remoteVersion == m.version.Load() {
		return nil
	}

	// 创建新缓存（避免在持锁期间逐个删除）
	newCache := make(map[string]string, len(mappings))
	for prefix, target := range mappings {
		newCache[prefix] = target
	}

	// 一次性替换缓存
	m.cache = newCache

	// 更新版本号
	if remoteVersion > 0 {
		m.version.Store(remoteVersion)
	} else {
		// 如果Redis中没有版本号，使用本地版本号并写入Redis
		m.version.Add(1)
		m.client.Set(ctx, KeyMappingsVersion, m.version.Load(), 0)
	}
	m.lastReload.Store(time.Now().Unix())

	log.Printf("📦 Reloaded %d mappings from Redis (version: %d)", len(mappings), m.version.Load())

	return nil
}

// backgroundReloader 后台定期重载映射
func (m *MappingManager) backgroundReloader() {
	defer m.wg.Done()

	ticker := time.NewTicker(ReloadPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			log.Println("🛑 Background reloader stopped")
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.reloadMappings(ctx); err != nil {
				log.Printf("⚠️  Background reload failed: %v", err)
			}
			cancel()
		}
	}
}

// pubsubListener 监听Redis Pub/Sub消息,实现多实例缓存同步
func (m *MappingManager) pubsubListener() {
	defer m.wg.Done()

	ch := m.pubsub.Channel()

	for {
		select {
		case <-m.stopChan:
			log.Println("🛑 Pub/Sub listener stopped")
			return
		case msg := <-ch:
			if msg == nil {
				continue
			}

			log.Printf("📨 Received Pub/Sub message: %s", msg.Payload)

			// 触发重载
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := m.reloadMappings(ctx); err != nil {
				log.Printf("⚠️  Failed to reload after Pub/Sub notification: %v", err)
			} else {
				log.Printf("✅ Cache synchronized via Pub/Sub")
			}
			cancel()
		}
	}
}

// GetMapping 获取指定前缀的目标URL
func (m *MappingManager) GetMapping(ctx context.Context, prefix string) (string, error) {
	// 从缓存读取（读锁保护）
	m.mu.RLock()
	target, ok := m.cache[prefix]
	m.mu.RUnlock()

	if ok {
		return target, nil
	}

	// 缓存未命中,从Redis读取
	target, err := m.client.HGet(ctx, KeyMappings, prefix).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("mapping not found for prefix: %s", prefix)
	}
	if err != nil {
		return "", err
	}

	// 更新缓存（写锁保护）
	m.mu.Lock()
	m.cache[prefix] = target
	m.mu.Unlock()

	return target, nil
}

// GetAllMappings 获取所有映射
func (m *MappingManager) GetAllMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 复制map避免外部修改
	result := make(map[string]string, len(m.cache))
	for k, v := range m.cache {
		result[k] = v
	}

	return result
}

// ForceReload 强制从Redis重新加载映射,忽略版本号检查
// 用于多实例部署时手动触发缓存同步
func (m *MappingManager) ForceReload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 从Redis获取所有映射
	mappings, err := m.client.HGetAll(ctx, KeyMappings).Result()
	if err != nil {
		return err
	}

	// 创建新缓存
	newCache := make(map[string]string, len(mappings))
	for prefix, target := range mappings {
		newCache[prefix] = target
	}

	// 替换缓存
	m.cache = newCache

	// 同步Redis版本号
	remoteVersion, err := m.client.Get(ctx, KeyMappingsVersion).Int64()
	if err != nil && err != redis.Nil {
		log.Printf("⚠️  Failed to get remote version: %v", err)
	}
	if remoteVersion > 0 {
		m.version.Store(remoteVersion)
	}

	m.lastReload.Store(time.Now().Unix())

	log.Printf("🔄 Force reloaded %d mappings from Redis (version: %d)", len(mappings), m.version.Load())

	return nil
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

	// 增加Redis版本号
	newVersion, err := m.client.Incr(ctx, KeyMappingsVersion).Result()
	if err != nil {
		log.Printf("⚠️  Failed to increment version: %v", err)
	}

	// 更新缓存和本地版本号(写锁保护)
	m.mu.Lock()
	m.cache[prefix] = target
	m.mu.Unlock()

	if newVersion > 0 {
		m.version.Store(newVersion)
	} else {
		m.version.Add(1)
	}

	// 发布Pub/Sub通知其他实例
	if err := m.client.Publish(ctx, KeyMappingsChannel, "mapping_added").Err(); err != nil {
		log.Printf("⚠️  Failed to publish Pub/Sub notification: %v", err)
	}

	log.Printf("[AUDIT] Added mapping: %s -> %s (version: %d)", prefix, target, m.version.Load())

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

	// 增加Redis版本号
	newVersion, err := m.client.Incr(ctx, KeyMappingsVersion).Result()
	if err != nil {
		log.Printf("⚠️  Failed to increment version: %v", err)
	}

	// 更新缓存和本地版本号(写锁保护)
	m.mu.Lock()
	m.cache[prefix] = target
	m.mu.Unlock()

	if newVersion > 0 {
		m.version.Store(newVersion)
	} else {
		m.version.Add(1)
	}

	// 发布Pub/Sub通知其他实例
	if err := m.client.Publish(ctx, KeyMappingsChannel, "mapping_updated").Err(); err != nil {
		log.Printf("⚠️  Failed to publish Pub/Sub notification: %v", err)
	}

	log.Printf("[AUDIT] Updated mapping: %s -> %s (version: %d)", prefix, target, m.version.Load())

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

	// 增加Redis版本号
	newVersion, err := m.client.Incr(ctx, KeyMappingsVersion).Result()
	if err != nil {
		log.Printf("⚠️  Failed to increment version: %v", err)
	}

	// 从缓存删除并更新本地版本号(写锁保护)
	m.mu.Lock()
	delete(m.cache, prefix)
	m.mu.Unlock()

	if newVersion > 0 {
		m.version.Store(newVersion)
	} else {
		m.version.Add(1)
	}

	// 发布Pub/Sub通知其他实例
	if err := m.client.Publish(ctx, KeyMappingsChannel, "mapping_deleted").Err(); err != nil {
		log.Printf("⚠️  Failed to publish Pub/Sub notification: %v", err)
	}

	log.Printf("[AUDIT] Deleted mapping: %s (version: %d)", prefix, m.version.Load())

	return nil
}

// Count 返回映射数量
func (m *MappingManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.cache)
}

// GetPrefixes 获取所有前缀列表
func (m *MappingManager) GetPrefixes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	prefixes := make([]string, 0, len(m.cache))
	for prefix := range m.cache {
		prefixes = append(prefixes, prefix)
	}

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

// GetClient 返回Redis客户端（用于其他模块复用连接）
func (m *MappingManager) GetClient() *redis.Client {
	return m.client
}

// Close 关闭Redis连接并停止后台goroutine
func (m *MappingManager) Close() error {
	// 通知后台goroutine停止
	close(m.stopChan)

	// 等待后台goroutine退出
	m.wg.Wait()

	// 关闭Pub/Sub订阅
	if m.pubsub != nil {
		if err := m.pubsub.Close(); err != nil {
			log.Printf("⚠️  Failed to close Pub/Sub: %v", err)
		}
	}

	// 关闭Redis连接
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
