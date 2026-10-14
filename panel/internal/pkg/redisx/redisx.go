// Package redisx 构造 go-redis 客户端（不在这里 Ping）。
package redisx

import (
	"context"
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/redis/go-redis/v9"
)

// statsHook 按 ctx 里的 reqstats 计数，pipeline 按命令条数算；没有计数器时什么也不做。
// go-redis 9.23 起新连接的握手（HELLO、AUTH、CLIENT SETINFO 等）也走 hook 链，用的是触发建连那次请求的 ctx；
// 这里不计握手，只计业务命令，否则每请求条数会随连接池是否新建连接而跳动（建连看连接池指标）。
type statsHook struct{}

func (statsHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (statsHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if !handshakeCmd(cmd) {
			reqstats.From(ctx).AddRedis(1)
		}
		return next(ctx, cmd)
	}
}

func (statsHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		n := 0
		for _, cmd := range cmds {
			if !handshakeCmd(cmd) {
				n++
			}
		}
		reqstats.From(ctx).AddRedis(n)
		return next(ctx, cmds)
	}
}

// handshakeCmd 判断是不是 go-redis 建连时自己发的握手命令（业务代码不发这些）。
func handshakeCmd(cmd redis.Cmder) bool {
	switch cmd.Name() {
	case "hello", "auth", "select", "readonly":
		return true
	case "client":
		args := cmd.Args()
		if len(args) < 2 {
			return false
		}
		sub, _ := args[1].(string)
		switch strings.ToLower(sub) {
		case "setinfo", "setname", "tracking", "maint_notifications":
			return true
		}
	}
	return false
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
