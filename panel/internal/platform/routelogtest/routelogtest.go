// Package routelogtest 给三个网关的路由表做「日志里没有原始路径」的守卫。
//
// 订阅链接的令牌、Telegram 回调的 secret 都是路径段，日志只许写路由模板
// （httpx.RouteTemplate、访问日志的 route）。这里遍历网关的全部路由，把每个路径
// 参数与通配段换成独一无二的哨兵值各打一次请求，断言日志里一个哨兵都没有。
// 新加的带参数路由自动被覆盖，不用另外登记。
package routelogtest

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// Build 用给定的 logger 与 Valkey 客户端造出网关路由器。用不到 Valkey 的网关忽略 rdb。
type Build func(log *slog.Logger, rdb *redis.Client) http.Handler

// Result 是一次检查打过的路由与全部日志，调用方可再加断言。
type Result struct {
	Routes []string // 「方法 模板」
	Logs   string
}

// sentinelPrefix 是哨兵值的前缀：只含小写字母与数字，任何路径参数都能接受。
const sentinelPrefix = "rawpathsecret"

var paramRE = regexp.MustCompile(`\{[^}]*\}`)

// Option 调整检查。
type Option func(*options)

type options struct{ public map[string]bool }

// PublicParam 声明某个路径参数是公开标识（不是凭证），处理函数可以有意把它作为
// 独立字段写进日志（如 enrollment_id），这些参数填固定的普通值、不算哨兵。
// 默认每个参数都按秘密对待：漏写只会让守卫更严，不会放过泄漏。整条原始路径
// 无论如何都不许出现在日志里。
func PublicParam(names ...string) Option {
	return func(o *options) {
		for _, n := range names {
			o.public[n] = true
		}
	}
}

// Check 对 build 出的网关跑两遍（限流放行、限流拒绝），每遍打全部路由与几条不存在的
// 路径；任何一行日志里出现哨兵或整条原始路径即失败。
func Check(t *testing.T, build Build, opts ...Option) Result {
	t.Helper()
	o := options{public: map[string]bool{}}
	for _, f := range opts {
		f(&o)
	}
	var res Result
	for _, deny := range []bool{false, true} {
		logs := &lockedBuffer{}
		log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
		rdb.AddHook(limiterHook{deny: deny})
		router := build(log, rdb)
		routes, ok := router.(chi.Routes)
		if !ok {
			_ = rdb.Close()
			t.Fatalf("router %T is not a chi router", router)
		}

		n := 0
		sentinel := func() string {
			n++
			return fmt.Sprintf("%s%04d", sentinelPrefix, n)
		}
		var targets [][2]string
		res.Routes = res.Routes[:0]
		err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			path := paramRE.ReplaceAllStringFunc(route, func(p string) string {
				if o.public[strings.Trim(p, "{}")] {
					return "publicid"
				}
				return sentinel()
			})
			path = strings.ReplaceAll(path, "*", sentinel())
			targets = append(targets, [2]string{method, path})
			res.Routes = append(res.Routes, method+" "+route)
			return nil
		})
		if err != nil {
			_ = rdb.Close()
			t.Fatalf("walk routes: %v", err)
		}
		// 没匹配上任何路由的请求（抄错的订阅链接、多一段的回调地址）
		for _, p := range []string{"/" + sentinel(), "/" + sentinel() + "/" + sentinel() + "/" + sentinel(), "/v1/" + sentinel(), "/v1/webhooks/telegram/" + sentinel() + "/x"} {
			targets = append(targets, [2]string{http.MethodGet, p}, [2]string{http.MethodPost, p})
		}

		for _, tg := range targets {
			serve(router, tg[0], tg[1])
		}
		_ = rdb.Close()

		out := logs.String()
		if out == "" {
			t.Fatalf("deny=%v: no log lines at all; the guard would pass vacuously", deny)
		}
		if i := strings.Index(out, sentinelPrefix); i >= 0 {
			t.Fatalf("deny=%v: a path parameter reached the logs (write the route template instead):\n%s", deny, lineAt(out, i))
		}
		for _, tg := range targets {
			// 带哨兵的路径上面已经查过；没有参数的路由，原始路径就是模板本身
			if !strings.Contains(tg[1], "publicid") {
				continue
			}
			if i := strings.Index(out, tg[1]); i >= 0 {
				t.Fatalf("deny=%v: the raw path %s reached the logs:\n%s", deny, tg[1], lineAt(out, i))
			}
		}
		res.Logs += out
	}
	return res
}

func lineAt(out string, i int) string {
	line := out[strings.LastIndex(out[:i], "\n")+1:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return line
}

func serve(router http.Handler, method, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(method, path, nil).WithContext(ctx)
	req.RemoteAddr = "192.0.2.10:40000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
}

// limiterHook 截住全部 Valkey 命令、不连网络：限流脚本按 deny 回「放行」或「第一个维度超限」，
// 其余命令回空值。
type limiterHook struct{ deny bool }

func (limiterHook) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }
}

func (h limiterHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		if c, ok := cmd.(*redis.Cmd); ok {
			if h.deny {
				c.SetVal([]any{int64(1), int64(999), int64(60000)})
			} else {
				c.SetVal([]any{int64(0), int64(0)})
			}
			return nil
		}
		return redis.Nil
	}
}

func (limiterHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, cmds []redis.Cmder) error {
		for _, c := range cmds {
			c.SetErr(redis.Nil)
		}
		return nil
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
