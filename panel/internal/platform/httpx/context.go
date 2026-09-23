package httpx

import (
	"context"
	"net/http"
	"slices"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyPrincipal
	ctxKeyTenantID
)

//------------------------------------------------------------------------------
// 请求 ID（NFR-005：支付/订阅/节点问题可定位到跨服务链路）
//------------------------------------------------------------------------------

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

//------------------------------------------------------------------------------
// 租户
//------------------------------------------------------------------------------

func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID, id)
}

func TenantIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyTenantID).(string); ok {
		return v
	}
	return ""
}

//------------------------------------------------------------------------------
// 调用主体
//------------------------------------------------------------------------------

// Principal 是通过认证的调用方。
//
// Audience 是本主体被签发时所属的 API 域。中间件会核对它与当前网关是否一致 ——
// 这是 EXT-001「不同 API 的令牌不能交叉使用」的执行点。
type Principal struct {
	Kind      string // user / admin / device / node / api_token / anonymous
	Audience  string // public / admin / client / node
	UserID    string
	TenantID  string
	SessionID string
	DeviceID  string
	NodeID    string

	// Permissions 是已展开的权限码集合（含数据范围过滤后的结果）。
	Permissions []string
	// Scopes 用于 API Token（IAM-011）。
	Scopes []string

	// AuthMethods 记录本次认证达到的强度，供高风险动作判断是否需要重认证（SEC-009）。
	AuthMethods []string
	// ReauthedRecently 为 true 表示近期完成过重认证。
	ReauthedRecently bool
}

func (p *Principal) IsAnonymous() bool { return p == nil || p.Kind == "" || p.Kind == "anonymous" }

// Can 报告主体是否持有某权限码。默认拒绝：主体为空或权限未列出即为 false（IAM-009）。
func (p *Principal) Can(permission string) bool {
	if p == nil {
		return false
	}
	return slices.Contains(p.Permissions, permission)
}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalFrom 永不返回 nil，省去调用点的空判断。
func PrincipalFrom(ctx context.Context) *Principal {
	if v, ok := ctx.Value(ctxKeyPrincipal).(*Principal); ok && v != nil {
		return v
	}
	return &Principal{Kind: "anonymous"}
}

// ClientIP 取真实客户端 IP。
//
// 只信任反代注入的 X-Real-IP，不解析 X-Forwarded-For 链：
// XFF 可由客户端伪造并追加，若按「取第一个」解析，攻击者就能伪造 IP 绕过按 IP 的限流
// （SEC-002 要求攻击者不能通过轮换单一维度轻易绕过，前提是这一维度本身可信）。
// 边缘网关必须负责覆写 X-Real-IP。
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

// 来源信息随 context 走。
//
// 审计需要每条记录都带上来源 IP 与 UA，但审计点分散在十几个领域方法里，
// 那些方法只拿得到 context，拿不到 *http.Request。要求每个调用点
// 从 handler 一层层把 IP 传下去，只会得到「大部分传了、少数忘了」的结果 ——
// 而忘掉的那几条恰恰是事后要查的时候才发现缺的。
type ctxKeyClientIPT struct{}
type ctxKeyUserAgentT struct{}

var (
	ctxKeyClientIP  ctxKeyClientIPT
	ctxKeyUserAgent ctxKeyUserAgentT
)

// WithClientInfo 把来源信息放进 context，由中间件在请求入口调用一次。
func WithClientInfo(ctx context.Context, ip, ua string) context.Context {
	if ip != "" {
		ctx = context.WithValue(ctx, ctxKeyClientIP, ip)
	}
	if ua != "" {
		ctx = context.WithValue(ctx, ctxKeyUserAgent, ua)
	}
	return ctx
}

// ClientIPFrom 取来源 IP，不存在时返回空串。
func ClientIPFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyClientIP).(string)
	return v
}

// UserAgentFrom 取 User-Agent，不存在时返回空串。
func UserAgentFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUserAgent).(string)
	return v
}
