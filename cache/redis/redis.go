// Package redis adapts github.com/redis/go-redis/v9 clients to SaaS cache.Cache.
//
// 包 redis 将 github.com/redis/go-redis/v9 客户端适配为 SaaS cache.Cache。
package redis

import (
	"context"
	"errors"
	"time"

	corecache "github.com/darkinno-tech/saas/cache"
	goredis "github.com/redis/go-redis/v9"
)

var _ corecache.Cache = (*Redis)(nil)

// ErrInvalidRedisConfig reports an invalid Redis adapter configuration.
// It aliases the core cache sentinel so callers can use errors.Is across the
// core and optional Redis modules.
var ErrInvalidRedisConfig = corecache.ErrInvalidRedisConfig

// Client is the subset of go-redis used by the cache adapter.
type Client interface {
	Get(ctx context.Context, key string) *goredis.StringCmd
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *goredis.StatusCmd
	Del(ctx context.Context, keys ...string) *goredis.IntCmd
	Close() error
}

// Pinger is implemented by go-redis clients that support PING health checks.
type Pinger interface {
	Ping(ctx context.Context) *goredis.StatusCmd
}

// Redis stores cache values through a go-redis client.
type Redis struct {
	client Client
}

// New creates a cache adapter from an existing go-redis client.
//
// The caller owns the client configuration. Close closes the provided client.
func New(client Client) (*Redis, error) {
	if client == nil {
		return nil, ErrInvalidRedisConfig
	}
	return &Redis{client: client}, nil
}

// NewFromOptions creates a Redis cache adapter from standalone client options.
func NewFromOptions(options *goredis.Options) (*Redis, error) {
	if options == nil {
		return nil, ErrInvalidRedisConfig
	}
	cloned := *options
	return New(goredis.NewClient(&cloned))
}

// NewFromClusterOptions creates a Redis cache adapter from cluster client options.
func NewFromClusterOptions(options *goredis.ClusterOptions) (*Redis, error) {
	if options == nil {
		return nil, ErrInvalidRedisConfig
	}
	cloned := *options
	return New(goredis.NewClusterClient(&cloned))
}

// NewFromURL creates a Redis cache adapter from a redis:// or rediss:// URL.
func NewFromURL(rawURL string) (*Redis, error) {
	if rawURL == "" {
		return nil, ErrInvalidRedisConfig
	}
	options, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, ErrInvalidRedisConfig
	}
	return NewFromOptions(options)
}

// Get returns a cache value.
func (cache *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	client, err := cache.redisClient()
	if err != nil {
		return nil, false, err
	}

	cmd := client.Get(ctx, key)
	if cmd == nil {
		return nil, false, ErrInvalidRedisConfig
	}
	value, err := cmd.Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return cloneBytes(value), true, nil
}

// Set stores a cache value.
func (cache *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := cache.redisClient()
	if err != nil {
		return err
	}

	cmd := client.Set(ctx, key, cloneBytes(value), ttl)
	if cmd == nil {
		return ErrInvalidRedisConfig
	}
	return cmd.Err()
}

// Delete removes a cache value.
func (cache *Redis) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := cache.redisClient()
	if err != nil {
		return err
	}

	cmd := client.Del(ctx, key)
	if cmd == nil {
		return ErrInvalidRedisConfig
	}
	return cmd.Err()
}

// Ping checks whether the underlying Redis client can reach the server.
func (cache *Redis) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := cache.redisClient()
	if err != nil {
		return err
	}
	pinger, ok := client.(Pinger)
	if !ok {
		return ErrInvalidRedisConfig
	}

	cmd := pinger.Ping(ctx)
	if cmd == nil {
		return ErrInvalidRedisConfig
	}
	return cmd.Err()
}

// Close closes the underlying Redis client.
func (cache *Redis) Close() error {
	client, err := cache.redisClient()
	if err != nil {
		return err
	}
	return client.Close()
}

func (cache *Redis) redisClient() (Client, error) {
	if cache == nil || cache.client == nil {
		return nil, ErrInvalidRedisConfig
	}
	return cache.client, nil
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}
