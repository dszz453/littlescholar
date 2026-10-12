package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/littlescholar/core/internal/config"
	"github.com/redis/go-redis/v9"
)

// Client 对 Redis 的薄封装（缓存层）
//
// 设计原则：Redis 是**可选**的加速层，不是必需组件。
// 未启用或连接失败时，Client 处于 disabled 状态，
// 所有方法降级为「未命中」，由调用方回退到数据库查询。
// 这样部署时即使不起 Redis，系统仍能正常工作，只是没有缓存收益。
type Client struct {
	rdb      *redis.Client
	disabled bool
}

// New 创建客户端。enabled=false 或连接失败时返回降级实例（不返回错误）
func New(cfg config.RedisConfig) *Client {
	if !cfg.Enabled {
		return &Client{disabled: true}
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr(),
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     20,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		// 连不上不算致命错误，降级即可
		return &Client{disabled: true}
	}
	return &Client{rdb: rdb}
}

// Enabled 是否真正可用
func (c *Client) Enabled() bool {
	return c != nil && !c.disabled && c.rdb != nil
}

// Get 读缓存。未启用或未命中时返回 ("", false)
func (c *Client) Get(ctx context.Context, key string) (string, bool) {
	if !c.Enabled() {
		return "", false
	}
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return val, true
}

// Set 写缓存。失败静默忽略（缓存写失败不应影响主流程）
func (c *Client) Set(ctx context.Context, key, val string, ttl time.Duration) {
	if !c.Enabled() {
		return
	}
	c.rdb.Set(ctx, key, val, ttl)
}

// DelPrefix 按前缀删除（用于授权变更时失效）
// 使用 SCAN 而非 KEYS，避免在大 key 空间下阻塞。
//
// 已知权衡：SCAN + DEL 非原子操作，理论上存在竞态——
// 「失效」与「并发请求回填旧值」可能交错，导致脏数据存活到 TTL 到期。
// 本系统接受该风险，原因：
//  1. 授权变更是管理员手动触发的极低频操作，并发窗口以毫秒计
//  2. 最坏后果是可见目录范围短暂偏差，TTL(5min) 后自愈，不会损坏数据
//  3. 真正需要强一致性的播放鉴权走 CanAccessCategory（实时查库，不经缓存）
// 若将来授权变更为高频操作，需引入版本号或双删延迟机制。
func (c *Client) DelPrefix(ctx context.Context, prefix string) {
	if !c.Enabled() {
		return
	}
	var cursor uint64
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			c.rdb.Del(ctx, keys...)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
}

// Close 关闭连接
func (c *Client) Close() error {
	if !c.Enabled() {
		return nil
	}
	return c.rdb.Close()
}

// KeyPermUser 用户可访问目录集合的缓存键
func KeyPermUser(userID uint64) string {
	return fmt.Sprintf("ls:perm:user:%d", userID)
}

// KeyPermUserPrefix 用于按用户前缀批量失效
func KeyPermUserPrefix() string {
	return "ls:perm:user:"
}
