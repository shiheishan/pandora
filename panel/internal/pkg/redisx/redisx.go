// Package redisx 构造 go-redis 客户端（不在这里 Ping）。
package redisx

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/redis/go-redis/v9"
)

// statsHook 按 ctx 里的 reqstats 计数，pipeline 按命令条数算；没有计数器时什么也不做。
type statsHook struct{}

func (statsHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (statsHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		reqstats.From(ctx).AddRedis(1)
		return next(ctx, cmd)
	}
}

func (statsHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		reqstats.From(ctx).AddRedis(len(cmds))
		return next(ctx, cmds)
	}
}

// New 按配置解析 URL 并创建客户端。
func New(cfg config.Redis) (*redis.Client, error) {
	opt, err := Options(cfg)
	if err != nil {
		return nil, err
	}
	c := redis.NewClient(opt)
	c.AddHook(statsHook{})
	return c, nil
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

// 确保 statsHook 实现了 redis.Hook 接口
var _ redis.Hook = statsHook{}
