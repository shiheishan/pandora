package roundtrip

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// ValkeyHook 是 go-redis 的钩子：一条命令或一个管线（含 MULTI/EXEC 事务管线）
// 记一次 Valkey 往返，记在 ctx 上的计数器里。EVALSHA 撞上 NOSCRIPT 后退回 EVAL
// 是两条命令，记两次——那确实是两次往返。
//
// 每个网关进程在建好 Valkey 客户端后挂一次：rdb.AddHook(roundtrip.ValkeyHook{})。
// 挂两次会重复计数。
type ValkeyHook struct{}

// DialHook 不计数：建连接不是请求里的命令往返。
func (ValkeyHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook 记单条命令。
func (ValkeyHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if c := From(ctx); c != nil {
			c.AddKV()
		}
		return next(ctx, cmd)
	}
}

// ProcessPipelineHook 记一个管线：整批一次往返。
func (ValkeyHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if c := From(ctx); c != nil {
			c.AddKV()
		}
		return next(ctx, cmds)
	}
}
