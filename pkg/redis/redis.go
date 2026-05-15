package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	Addr          string `yaml:"addr"`
	Password      string `yaml:"password"`
	DB            int    `yaml:"db"`
	EnableTracing bool   `yaml:"enableTracing"`
	// Pool parameters (zero-value falls back to sensible defaults)
	PoolSize       int `yaml:"poolSize"`
	MinIdleConns   int `yaml:"minIdleConns"`
	PoolTimeoutSec int `yaml:"poolTimeoutSec"`
	ConnMaxLifeSec int `yaml:"connMaxLifetimeSec"`
	ConnMaxIdleSec int `yaml:"connMaxIdleTimeSec"`
}

func (c *Config) setDefaults() {
	if c.PoolSize <= 0 {
		c.PoolSize = 100
	}
	if c.MinIdleConns <= 0 {
		c.MinIdleConns = 10
	}
	if c.PoolTimeoutSec <= 0 {
		c.PoolTimeoutSec = 4
	}
	if c.ConnMaxLifeSec <= 0 {
		c.ConnMaxLifeSec = 1800 // 30 min
	}
	if c.ConnMaxIdleSec <= 0 {
		c.ConnMaxIdleSec = 600 // 10 min
	}
}

type Client struct {
	rdb *redis.Client
}

func New(cfg Config) (*Client, func(), error) {
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
		_ = rdb.Close() // P2-19: close client on ping failure to prevent leak
		return nil, nil, err
	}

	cleanup := func() { _ = rdb.Close() }
	return &Client{rdb: rdb}, cleanup, nil
}

// RawClient returns the underlying *redis.Client.
// Use this when you need access to the full go-redis API
// (e.g., custom commands that Client does not wrap).
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

// SAdd adds members to a Redis SET.
func (c *Client) SAdd(ctx context.Context, key string, members ...interface{}) error {
	return c.rdb.SAdd(ctx, key, members...).Err()
}

// SRem removes members from a Redis SET.
func (c *Client) SRem(ctx context.Context, key string, members ...interface{}) error {
	return c.rdb.SRem(ctx, key, members...).Err()
}

// SIsMember checks if a member exists in a Redis SET.
func (c *Client) SIsMember(ctx context.Context, key string, member interface{}) (bool, error) {
	return c.rdb.SIsMember(ctx, key, member).Result()
}

// Ping checks Redis connectivity. Returns true if Redis is reachable.
func (c *Client) Ping(ctx context.Context) bool {
	return c.rdb.Ping(ctx).Err() == nil
}

// Expire sets a TTL on an existing key.
func (c *Client) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// SAddExpire atomically adds members to a SET and sets its TTL.
// Uses a Redis TxPipeline (MULTI/EXEC) so both commands execute atomically,
// preventing the race where a server crash between SAdd and Expire
// leaves the SET without a TTL (tokens would never expire).
func (c *Client) SAddExpire(ctx context.Context, key string, ttl time.Duration, members ...interface{}) error {
	pipe := c.rdb.TxPipeline()
	pipe.SAdd(ctx, key, members...)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// SMembers returns all members of a Redis SET.
func (c *Client) SMembers(ctx context.Context, key string) ([]string, error) {
	return c.rdb.SMembers(ctx, key).Result()
}

// Incr increments the integer value of a key by 1.
func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	return c.rdb.Incr(ctx, key).Result()
}

// GetDel atomically gets the value of a key and deletes it.
// Requires Redis 6.2+.
func (c *Client) GetDel(ctx context.Context, key string) (string, error) {
	return c.rdb.GetDel(ctx, key).Result()
}

// NewScript creates a Redis Lua script that can be executed atomically.
func NewScript(src string) *Script {
	return &Script{s: redis.NewScript(src)}
}

// Script wraps a redis.Script for atomic Lua execution.
type Script struct {
	s *redis.Script
}

// Run executes the Lua script. keys are KEYS[], args are ARGV[].
func (sc *Script) Run(ctx context.Context, c *Client, keys []string, args ...interface{}) (interface{}, error) {
	return sc.s.Run(ctx, c.rdb, keys, args...).Result()
}
