package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	Addr          string   `yaml:"addr"`
	Password      string   `yaml:"password"`
	DB            int      `yaml:"db"`
	Addrs         []string `yaml:"addrs"`
	EnableTracing bool     `yaml:"enableTracing"`
}

type Client struct {
	rdb *redis.Client
}

func New(cfg Config) (*Client, func(), error) {
	opt := &redis.Options{
		Addr:            cfg.Addr,
		Password:        cfg.Password,
		DB:              cfg.DB,
		PoolSize:        100,              // max connections
		MinIdleConns:    10,               // minimum idle connections
		PoolTimeout:     4 * time.Second,  // wait timeout for getting a connection
		ConnMaxLifetime: 30 * time.Minute, // max connection age
		ConnMaxIdleTime: 10 * time.Minute, // max idle connection age
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
