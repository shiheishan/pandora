package routebudget

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/roundtrip"
)

// Valkey 返回一个不连网络的 Valkey 客户端，给 PG18 预算测试数 Valkey 往返（CI 的
// PG18 作业里没有 Valkey）。钩子链与生产网关相同：外层是往返记账钩子
// （roundtrip.ValkeyHook），最后一环按命令回「放行」：
//   - 限流脚本（EVALSHA / EVAL）回 {0, 0}：没有维度超限；
//   - SET … NX 回成功（节点签名的 nonce 认领）；
//   - 其余命令回空值。
func Valkey(t testing.TB) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	rdb.AddHook(roundtrip.ValkeyHook{})
	rdb.AddHook(allowAll{})
	return rdb
}

type allowAll struct{}

func (allowAll) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }
}

func (allowAll) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error { answer(cmd); return nil }
}

func (allowAll) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			answer(cmd)
		}
		return nil
	}
}

func answer(cmd redis.Cmder) {
	switch c := cmd.(type) {
	case *redis.Cmd:
		if name := strings.ToLower(c.Name()); name == "evalsha" || name == "eval" {
			c.SetVal([]any{int64(0), int64(0)})
		}
	case *redis.BoolCmd:
		c.SetVal(true)
	case *redis.StatusCmd:
		c.SetVal("OK")
	case *redis.StringCmd:
		c.SetErr(redis.Nil)
	}
}
