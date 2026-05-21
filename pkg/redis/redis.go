package redis

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	Addr          string `yaml:"addr"`
	Password      string `yaml:"password"`
	DB            int    `yaml:"db"`
	EnableTracing bool   `yaml:"enableTracing"`
	// 连接池参数（零值将回退到合理的默认值）
	PoolSize       int `yaml:"poolSize"`
	MinIdleConns   int `yaml:"minIdleConns"`
	PoolTimeoutSec int `yaml:"poolTimeoutSec"`
	ConnMaxLifeSec int `yaml:"connMaxLifetimeSec"`
	ConnMaxIdleSec int `yaml:"connMaxIdleTimeSec"`
}

func (c *Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("redis addr is required")
	}
	// R5-P2-13: 移除负数检查 — setDefaults() 将负数转为默认值，
	// New() 先调 Validate 再调 setDefaults，此处只检查必填字段
	return nil
}

func (c *Config) setDefaults() {
	if c.PoolSize <= 0 {
		c.PoolSize = 500
	}
	if c.MinIdleConns <= 0 {
		c.MinIdleConns = 50
	}
	if c.PoolTimeoutSec <= 0 {
		c.PoolTimeoutSec = 4
	}
	if c.ConnMaxLifeSec <= 0 {
		c.ConnMaxLifeSec = 1800 // 30 分钟
	}
	if c.ConnMaxIdleSec <= 0 {
		c.ConnMaxIdleSec = 600 // 10 分钟
	}
}

type Client struct {
	rdb *redis.Client
}

func New(cfg Config) (*Client, func(), error) {
	// R5-P2-10: 先校验再设默认值，提供清晰的错误信息（如 "redis addr is required"）
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	cfg.setDefaults()
	opt := &redis.Options{
		Addr:            cfg.Addr,
		Password:        cfg.Password,
		DB:              cfg.DB,
		PoolSize:        cfg.PoolSize,
		MinIdleConns:    cfg.MinIdleConns,
		PoolTimeout:     time.Duration(cfg.PoolTimeoutSec) * time.Second,
		ConnMaxLifetime: time.Duration(cfg.ConnMaxLifeSec) * time.Second,
		ConnMaxIdleTime: time.Duration(cfg.ConnMaxIdleSec) * time.Second,
	}
	rdb := redis.NewClient(opt)

	if cfg.EnableTracing {
		if err := redisotel.InstrumentTracing(rdb); err != nil {
			_ = rdb.Close()
			return nil, nil, err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close() // P2-19: ping 失败时关闭客户端以防止泄漏
		return nil, nil, err
	}

	cleanup := func() { _ = rdb.Close() }
	return &Client{rdb: rdb}, cleanup, nil
}

// RawClient 返回底层的 *redis.Client。
// 当需要访问完整的 go-redis API 时使用
// （例如 Client 未封装的自定义命令）。
func (c *Client) RawClient() *redis.Client {
	return c.rdb
}

func (c *Client) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

func (c *Client) Get(ctx context.Context, key string) (string, error) {
	return c.rdb.Get(ctx, key).Result()
}

func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

func (c *Client) Exists(ctx context.Context, keys ...string) (int64, error) {
	return c.rdb.Exists(ctx, keys...).Result()
}

// SAdd 向 Redis SET 添加成员。
func (c *Client) SAdd(ctx context.Context, key string, members ...interface{}) error {
	return c.rdb.SAdd(ctx, key, members...).Err()
}

// SRem 从 Redis SET 移除成员。
func (c *Client) SRem(ctx context.Context, key string, members ...interface{}) error {
	return c.rdb.SRem(ctx, key, members...).Err()
}

// SIsMember 检查成员是否存在于 Redis SET 中。
func (c *Client) SIsMember(ctx context.Context, key string, member interface{}) (bool, error) {
	return c.rdb.SIsMember(ctx, key, member).Result()
}

// Ping 检查 Redis 连通性。如果 Redis 可达则返回 true。
func (c *Client) Ping(ctx context.Context) bool {
	return c.rdb.Ping(ctx).Err() == nil
}

// PingErr 检查 Redis 连通性，返回具体错误。
// 供 pkg/health.RedisPinger 接口使用，使健康检查能记录错误详情。
func (c *Client) PingErr(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Expire 为已有 key 设置 TTL。
func (c *Client) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// SAddExpire 原子地向 SET 添加成员并设置其 TTL。
// 使用 Redis TxPipeline (MULTI/EXEC) 使两条命令原子执行，
// 防止 SAdd 和 Expire 之间服务器崩溃导致 SET 没有 TTL 的竞态条件
// （token 将永不过期）。
func (c *Client) SAddExpire(ctx context.Context, key string, ttl time.Duration, members ...interface{}) error {
	pipe := c.rdb.TxPipeline()
	pipe.SAdd(ctx, key, members...)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// SMembers 返回 Redis SET 的所有成员。
func (c *Client) SMembers(ctx context.Context, key string) ([]string, error) {
	return c.rdb.SMembers(ctx, key).Result()
}

// Incr 将 key 的整数值加 1。
func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	return c.rdb.Incr(ctx, key).Result()
}

// GetDel 原子地获取 key 的值并删除它。
// 需要 Redis 6.2+。
func (c *Client) GetDel(ctx context.Context, key string) (string, error) {
	return c.rdb.GetDel(ctx, key).Result()
}

// NewScript 创建一个可原子执行的 Redis Lua 脚本。
// P3-08: 安全性 — 仅使用硬编码的脚本字符串，绝不使用用户输入。
// Lua 脚本拥有完整的 Redis 访问权限；如果来自外部配置需验证源码。
func NewScript(src string) *Script {
	return &Script{s: redis.NewScript(src)}
}

// Script 封装 redis.Script 用于原子 Lua 执行。
type Script struct {
	s *redis.Script
}

// Run 执行 Lua 脚本。keys 对应 KEYS[]，args 对应 ARGV[]。
func (sc *Script) Run(ctx context.Context, c *Client, keys []string, args ...interface{}) (interface{}, error) {
	return sc.s.Run(ctx, c.rdb, keys, args...).Result()
}

// RunScript 通过源码字符串原子执行 Lua 脚本。
// 使用 sync.Map 缓存 Script 对象，避免重复创建。
// 注意：此缓存仅用于硬编码的常量 Lua 脚本（如限流、令牌轮换等），
// 不应用于动态用户输入。缓存永不清理，因为脚本数量是固定的。
func (c *Client) RunScript(ctx context.Context, src string, keys []string, args ...interface{}) (interface{}, error) {
	// P2-16: 类型断言加 comma-ok，防止 sync.Map 中存储非法类型导致 panic
	scriptI, _ := scriptCache.LoadOrStore(src, redis.NewScript(src))
	script, ok := scriptI.(*redis.Script)
	if !ok {
		scriptCache.Delete(src) // 清除非法条目
		script = redis.NewScript(src)
		scriptCache.Store(src, script)
	}
	return script.Run(ctx, c.rdb, keys, args...).Result()
}

// scriptCache 缓存硬编码的常量 Lua 脚本对象。
// 仅用于 RunScript 方法中固定的脚本字符串，不用于动态脚本。
var scriptCache sync.Map
