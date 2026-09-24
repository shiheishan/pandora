// [INPUT]: 依赖 platform/httpx 的错误模型与 Principal，依赖 go-redis 的限流计数
// [OUTPUT]: 对外提供 RequestID、Recovery、DomainGuard、RequirePermission、RequireRecentReauth、RateLimit 与 Limit、超时与公共链装配等 http 中间件
// [POS]: middleware 的横切中间件集合，挂在 api 路由之前；auth.go 负责令牌认证与租户注入，idempotency.go 负责幂等键，三者组成网关的公共链
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package middleware 汇集四域网关共用的横切关注点。
//
// 覆盖 PRD：
//
//	EXT-001 四域令牌隔离       DomainGuard
//	IAM-009 默认拒绝的权限校验  RequirePermission
//	DATA-002 租户注入          Tenant
//	SEC-002 多维限流           RateLimit
//	SEC-006 统一错误           Recovery + httpx
//	SEC-009 高风险重认证        RequireRecentReauth
//	NFR-005 请求链路追踪        RequestID
package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 请求 ID
//------------------------------------------------------------------------------

// RequestID 为每个请求分配可追踪的 ID。
//
// 客户端传入的 X-Request-ID 只在格式合法时才沿用，否则重新生成 ——
// 否则攻击者可以注入超长或带控制字符的值污染日志。
// ClientInfo 把来源 IP 与 User-Agent 放进 context。
//
// 必须排在链路最前面：后面每一层（限流、鉴权、业务、审计）都可能要用，
// 而排在鉴权之后的话，鉴权失败那条审计记录就没有来源信息 ——
// 那恰恰是最值得记下来的一类事件。
//
// UA 截断到 512 字节：正常客户端的 UA 不会超过两百字符，
// 超长的要么是探测工具在灌数据，要么是攻击载荷，没有留全的价值。
func ClientInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.UserAgent()
		if len(ua) > 512 {
			ua = ua[:512]
		}
		next.ServeHTTP(w, r.WithContext(
			httpx.WithClientInfo(r.Context(), httpx.ClientIP(r), ua)))
	})
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if _, err := uuid.Parse(id); err != nil {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), id)))
	})
}

//------------------------------------------------------------------------------
// 恢复与安全响应头
//------------------------------------------------------------------------------

// Recovery 捕获 panic，返回统一错误而非 Go 的堆栈页面（SEC-006）。
func Recovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("处理请求时发生 panic",
						slog.Any("panic", rec),
						slog.String("request_id", httpx.RequestIDFrom(r.Context())),
						slog.String("path", r.URL.Path))
					httpx.Fail(w, r, log,
						httpx.Internal(fmt.Errorf("panic: %v", rec)))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders 设置与 API 相关的安全响应头。
// 注意这里不设 CSP —— API 不返回 HTML，CSP 属于前端资源服务器的职责。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// API 响应一律不缓存：订阅状态、余额、配额都是敏感且易变的
		h.Set("Cache-Control", "no-store")
		h.Del("Server")
		next.ServeHTTP(w, r)
	})
}

//------------------------------------------------------------------------------
// 租户
//------------------------------------------------------------------------------

// DefaultTenantID 对应迁移 00010 里种下的默认租户。
// 附录 A：首个商用版本单租户运行，但全链路已按多租户建模。
const DefaultTenantID = "00000000-0000-7000-8000-000000000001"

// Tenant 解析当前请求所属租户。
//
// 首版按单租户运行，恒定注入默认租户；将来接入自定义域名时，
// 只需在此处按 Host 查表，下游与数据库的租户隔离逻辑一行都不用改。
func Tenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(httpx.WithTenantID(r.Context(), DefaultTenantID)))
	})
}

//------------------------------------------------------------------------------
// EXT-001 四域隔离
//------------------------------------------------------------------------------

// DomainGuard 拒绝为其他域签发的主体。
//
// 令牌的签名密钥本就按域分离，正常情况下跨域令牌在验签阶段就失败。
// 这一层是纵深防御：万一将来有人图省事复用了密钥，这里仍会挡住。
func DomainGuard(domain string, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := httpx.PrincipalFrom(r.Context())
			if !p.IsAnonymous() && p.Audience != domain {
				log.Warn("检测到跨域令牌使用",
					slog.String("token_audience", p.Audience),
					slog.String("gateway_domain", domain),
					slog.String("request_id", httpx.RequestIDFrom(r.Context())))
				httpx.Fail(w, r, log,
					httpx.New(httpx.CodeUnauthorized, "凭据无效"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

//------------------------------------------------------------------------------
// IAM-009 权限
//------------------------------------------------------------------------------

// RequireAuth 要求已认证主体。
func RequireAuth(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if httpx.PrincipalFrom(r.Context()).IsAnonymous() {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequirePermission 实现 IAM-009 的「默认拒绝」。
//
// 返回 404 而非 403 是刻意的：403 会告诉调用方「这个资源确实存在，只是你没权限」，
// 那正是 SEC-006 不允许泄露的对象存在性。
func RequirePermission(permission string, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := httpx.PrincipalFrom(r.Context())
			if !p.Can(permission) {
				log.Info("权限不足",
					slog.String("required", permission),
					slog.String("principal_kind", p.Kind),
					slog.String("user_id", p.UserID),
					slog.String("request_id", httpx.RequestIDFrom(r.Context())))
				httpx.Fail(w, r, log, httpx.NotFoundOrForbidden())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireRecentReauth 用于高风险动作（SEC-009）。
// 拒绝时回 403 reauth_required 而不是通用 forbidden：前端靠这个码弹「重新验证身份」，
// 拿到新令牌后以原 Idempotency-Key 重放；它挂在 Idempotency 之前，所以拒绝不消耗幂等键。
func RequireRecentReauth(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !httpx.PrincipalFrom(r.Context()).ReauthedRecently {
				httpx.Fail(w, r, log,
					httpx.New(httpx.CodeReauthRequired, "此操作需要重新验证身份"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

//------------------------------------------------------------------------------
// SEC-002 多维限流
//------------------------------------------------------------------------------

// Limit 是一个限流维度。
type Limit struct {
	Name   string
	Window time.Duration
	Max    int
	// KeyFn 返回该维度的计数键；返回空串表示本次请求不适用此维度。
	KeyFn func(*http.Request) string
}

// RateLimit 按多个维度联合限流。
//
// SEC-002 验收要求「攻击者不能通过轮换单一维度轻易绕过」，
// 因此这里对每个维度独立计数，任一维度超限即拒绝 ——
// 换 IP 挡不住账号维度，换账号挡不住 IP 段维度。
//
// Redis 不可用时选择放行而非拒绝：限流是防滥用手段，
// 让它的故障演变成全站不可用是本末倒置（NFR-002 舱壁思想）。
func RateLimit(rdb *redis.Client, log *slog.Logger, limits ...Limit) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			for _, l := range limits {
				suffix := l.KeyFn(r)
				if suffix == "" {
					continue
				}
				key := fmt.Sprintf("rl:%s:%s:%d", l.Name, suffix,
					time.Now().UnixNano()/int64(l.Window))

				n, err := rdb.Incr(ctx, key).Result()
				if err != nil {
					log.Warn("限流计数失败，本次放行",
						slog.String("dimension", l.Name),
						slog.String("error", err.Error()))
					continue
				}
				if n == 1 {
					// 只在首次创建时设过期，避免每次请求都刷新窗口导致永不过期
					rdb.Expire(ctx, key, l.Window+time.Second)
				}
				if n > int64(l.Max) {
					log.Info("触发限流",
						slog.String("dimension", l.Name),
						slog.Int64("count", n),
						slog.Int("max", l.Max),
						slog.String("request_id", httpx.RequestIDFrom(ctx)))
					httpx.Fail(w, r, log,
						httpx.New(httpx.CodeRateLimited, "请求过于频繁，请稍后再试"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- 常用维度 ---

// RateLimitStrict is for security-critical anonymous mutations. Any Redis
// counter or expiry failure returns a neutral 503 instead of bypassing controls.
func RateLimitStrict(rdb *redis.Client, log *slog.Logger, limits ...Limit) func(http.Handler) http.Handler {
	const incrementAndExpire = `
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return n`
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			for _, l := range limits {
				suffix := l.KeyFn(r)
				if suffix == "" {
					continue
				}
				if rdb == nil {
					httpx.Fail(w, r, log, httpx.New(httpx.CodeUnavailable,
						"服务暂时不可用，请稍后重试"))
					return
				}
				key := fmt.Sprintf("rl:%s:%s:%d", l.Name, suffix,
					time.Now().UnixNano()/int64(l.Window))
				n, err := rdb.Eval(ctx, incrementAndExpire, []string{key},
					(l.Window + time.Second).Milliseconds()).Int64()
				if err != nil {
					log.Warn("strict rate-limit counter failed",
						slog.String("dimension", l.Name), slog.String("error", err.Error()))
					httpx.Fail(w, r, log, httpx.New(httpx.CodeUnavailable,
						"服务暂时不可用，请稍后重试"))
					return
				}
				if n > int64(l.Max) {
					httpx.Fail(w, r, log,
						httpx.New(httpx.CodeRateLimited, "请求过于频繁，请稍后再试"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ByIP(name string, window time.Duration, max int) Limit {
	return Limit{Name: name, Window: window, Max: max,
		KeyFn: func(r *http.Request) string { return httpx.ClientIP(r) }}
}

// ByIPPrefix 按 /24（IPv4）或 /48（IPv6）网段限流。
// 单纯按 IP 限流对拥有整段地址的攻击者无效（SEC-002 提到的网段维度）。
func ByIPPrefix(name string, window time.Duration, max int) Limit {
	return Limit{Name: name, Window: window, Max: max, KeyFn: func(r *http.Request) string {
		ip := httpx.ClientIP(r)
		if strings.Contains(ip, ":") { // IPv6 → /48
			parts := strings.Split(ip, ":")
			if len(parts) >= 3 {
				return strings.Join(parts[:3], ":") + "::/48"
			}
			return ip
		}
		parts := strings.Split(ip, ".") // IPv4 → /24
		if len(parts) == 4 {
			return strings.Join(parts[:3], ".") + ".0/24"
		}
		return ip
	}}
}

func ByAccount(name string, window time.Duration, max int) Limit {
	return Limit{Name: name, Window: window, Max: max, KeyFn: func(r *http.Request) string {
		return httpx.PrincipalFrom(r.Context()).UserID
	}}
}

func ByRoute(name string, window time.Duration, max int) Limit {
	return Limit{Name: name, Window: window, Max: max, KeyFn: func(r *http.Request) string {
		// 用冒号而非空格分隔：限流键会出现在 redis-cli --scan 的输出里，
		// 含空格的键会被 shell 的词分割拆开，导致运维脚本 DEL 到错误的键名。
		return r.Method + ":" + r.URL.Path + "|" + httpx.ClientIP(r)
	}}
}

func ByTenant(name string, window time.Duration, max int) Limit {
	return Limit{Name: name, Window: window, Max: max, KeyFn: func(r *http.Request) string {
		return httpx.TenantIDFrom(r.Context())
	}}
}

// ByJSONFieldHash adds a subject limiter without putting email, token or invite
// material into Redis keys. The request body is restored before the handler runs.
func ByJSONFieldHash(
	name, field string, window time.Duration, max int, normalize func(string) string,
) Limit {
	return Limit{Name: name, Window: window, Max: max, KeyFn: func(r *http.Request) string {
		if r.Body == nil {
			return ""
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil {
			return ""
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if len(body) > 1<<20 {
			return ""
		}
		var values map[string]json.RawMessage
		if json.Unmarshal(body, &values) != nil {
			return ""
		}
		var value string
		if json.Unmarshal(values[field], &value) != nil {
			return ""
		}
		if normalize != nil {
			value = normalize(value)
		}
		if value == "" {
			return ""
		}
		sum := sha256.Sum256([]byte(value))
		return fmt.Sprintf("%x", sum[:16])
	}}
}

//------------------------------------------------------------------------------
// 超时
//------------------------------------------------------------------------------

// Timeout 给每个请求加上下文超时（NFR-002：所有外部调用定义超时）。
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 流式端点必须豁免。
			//
			// 这个超时是给「请求-响应」式接口兜底的：一个卡住的查询不该
			// 一直占着连接。但 SSE 的正常形态就是长时间挂着不返回，
			// 给它设超时等于规定它每 d 秒必须断一次 ——
			// 实测就是这么坏的：连接稳定地在 25 秒断开，
			// 客户端不停重连，而日志里一切正常，看不出任何异常。
			//
			// 判断用路径而不是 Accept 头：不是所有客户端都会带那个头，
			// 而漏判的后果（长连接被切断）比误判严重得多。
			if isStreamingPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isStreamingPath 判断是不是需要长时间保持的流式端点。
// 这里原本是一份写死的路径名单，加新端点必须回来改它。节点事件流上线时
// 没人知道有这份名单，于是连接稳定地在 20 秒断开——正好撞上它自己 20 秒的
// 心跳周期，看上去像是心跳把连接写坏了，两端日志还都正常。查了很久。
//
// 改成约定：路径最后一段是 stream 或 events 的端点，一律当长连接。名单会
// 忘记更新，命名约定不会——新加的流式端点只要照着命名，这里不用动。
//
// 代价是可能误判一个恰好叫 events 的普通接口。那个方向的后果只是它失去
// 超时兜底，比长连接被定期切断轻得多；timeout_streaming_test.go 里也钉了
// 一组反例，防止约定放得太宽。
func isStreamingPath(path string) bool {
	segment := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		segment = path[i+1:]
	}
	if segment == "stream" || segment == "events" {
		return true
	}
	// /v1/events/{topic} 这类子路径。
	return strings.Contains(path, "/events/")
}
