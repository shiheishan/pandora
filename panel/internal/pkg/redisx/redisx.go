// Package redisx 构造 go-redis 客户端（不在这里 Ping）。
package redisx

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/redis/go-redis/v9"
)

// New 按配置解析 URL 并创建客户端。
func New(cfg config.Redis) (*redis.Client, error) {
	opt, err := Options(cfg)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opt), nil
}

// Options 按配置解析 URL 并套上池大小与超时；业务客户端与 asynq 共用这一份。
func Options(cfg config.Redis) (*redis.Options, error) {
	opt, err := redis.ParseURL(cfg.URL.Reveal())
	if err != nil {
		return nil, err
	}
	if cfg.PoolSize > 0 {
		opt.PoolSize = cfg.PoolSize
	}
	opt.ContextTimeoutEnabled = true
	opt.ReadTimeout = cfg.OpTimeout
	opt.WriteTimeout = cfg.OpTimeout
	opt.DialTimeout = cfg.OpTimeout
	opt.PoolTimeout = cfg.OpTimeout
	return opt, nil
}

// Ping 检查 Redis 是否可达。
func Ping(ctx context.Context, c *redis.Client) error {
	return c.Ping(ctx).Err()
}
