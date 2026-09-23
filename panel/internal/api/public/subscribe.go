package public

// 订阅分发端点。
//
// 这是全站唯一一个「无需登录、来自公网、任何人都能访问」的业务接口，
// 因此它的失败路径比成功路径更需要设计：任何认证不通过的请求，
// 拿到的响应都必须与「这个路径压根不存在」无法区分。
//
// 具体来说，下面这些情况回的是同一个响应：路径前缀不对、token 不存在、
// token 已吊销、订阅已过期。它们内部当然有区别，但把区别透出去
// 等于告诉探测者「你猜对了一半」—— 猜中前缀、或者猜中一个真实 token。

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// decoyPage 是所有失败请求看到的东西。
//
// 刻意做成一个最普通不过的静态站 404：没有任何自定义响应头，
// 没有品牌信息，也没有「订阅不存在」这类会暴露用途的措辞。
const decoyPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>
`

func (h *handlers) subscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := httpx.TenantIDFrom(ctx)
	prefix := chi.URLParam(r, "prefix")
	rawToken := chi.URLParam(r, "token")

	ua := r.UserAgent()
	ip := clientIP(r)
	family := subscription.UAFamily(ua)

	// 客户端常给链接加个扩展名让自己认得出格式，去掉再比对
	token := rawToken
	for _, ext := range []string{".yaml", ".yml", ".json", ".txt", ".conf"} {
		token = strings.TrimSuffix(token, ext)
	}

	// 前缀不对：连查库都不做。扫描器的绝大多数请求止步于此，
	// 不让它们产生任何数据库负载
	ok, err := h.d.Subscription.MatchPrefix(ctx, tenantID, prefix)
	if err != nil || !ok {
		h.d.Subscription.Log(ctx, tenantID, "", "", "not_found", "", ip, ua, family, 0, 0)
		writeDecoy(w)
		return
	}

	cred, err := h.d.Subscription.Authenticate(ctx, tenantID, token)
	if err != nil {
		h.d.Subscription.Log(ctx, tenantID, "", "", "not_found", "", ip, ua, family, 0, 0)
		writeDecoy(w)
		return
	}

	nodes, err := h.d.Subscription.ListNodes(ctx, tenantID, cred)
	if err != nil {
		h.d.Log.Error("订阅取节点失败", "subscription", cred.SubscriptionID, "err", err)
		// 内部错误同样回伪装页：一个 500 页面同样能告诉探测者这里有东西
		h.d.Subscription.Log(ctx, tenantID, cred.ID, cred.SubscriptionID,
			"error", "", ip, ua, family, 0, 0)
		writeDecoy(w)
		return
	}

	format := subscription.DetectFormat(ua, subscribeTarget(r))
	body, contentType, count := subscription.Render(format, nodes, cred.ProxyUUID)

	usage, err := h.d.Subscription.LoadUsage(ctx, tenantID, cred)
	if err != nil {
		h.d.Log.Error("订阅取用量失败", "subscription", cred.SubscriptionID, "err", err)
		h.d.Subscription.Log(ctx, tenantID, cred.ID, cred.SubscriptionID,
			"error", string(format), ip, ua, family, count, 0)
		writeDecoy(w)
		return
	}
	// 所有读取完成后再原子检查限流并写成功计数；失败请求不占额度。
	if err := h.d.Subscription.RecordSuccessfulFetch(ctx, tenantID, cred.ID, cred.SubscriptionID,
		string(format), ip, ua, family, count, len(body), cred.RateLimit); err != nil {
		if errors.Is(err, subscription.ErrRateLimited) {
			h.d.Subscription.Log(ctx, tenantID, cred.ID, cred.SubscriptionID,
				"rate_limited", string(format), ip, ua, family, count, 0)
			w.Header().Set("Retry-After", "3600")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		h.d.Log.Error("订阅限流计数失败", "subscription", cred.SubscriptionID, "err", err)
		writeDecoy(w)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// 这个头是客户端显示「已用 / 总量 / 到期」的唯一来源，
	// 各家客户端都认它，格式不能改
	w.Header().Set("Subscription-Userinfo", formatUserinfo(usage))
	// 让客户端知道多久回来拉一次，避免有人几秒钟拉一回
	w.Header().Set("Profile-Update-Interval", "12")
	// 订阅内容随节点状态变化，不能被任何中间层缓存
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)

	h.d.Subscription.TouchCredential(ctx, tenantID, cred.ID, ip)
}

func writeDecoy(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(decoyPage))
}

// formatUserinfo 生成 Subscription-Userinfo 头。
// 格式是各家客户端约定俗成的：upload=; download=; total=; expire=
func formatUserinfo(u subscription.Usage) string {
	var b strings.Builder
	b.WriteString("upload=" + strconv.FormatInt(u.Upload, 10))
	b.WriteString("; download=" + strconv.FormatInt(u.Download, 10))
	b.WriteString("; total=" + strconv.FormatInt(u.Total, 10))
	if u.Expire > 0 {
		b.WriteString("; expire=" + strconv.FormatInt(u.Expire, 10))
	}
	return b.String()
}

// clientIP 取真实来源地址。
//
// 面板跑在 nginx 后面，RemoteAddr 永远是回环地址，
// 不看转发头的话审计里所有请求都来自同一个「IP」，多来源检测直接失效。
func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// 取最左边那个：链路上每一跳都会往右追加，
		// 最左边才是最初的客户端
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

//-----------------------------------------------------------------------------
// 面板侧：查看与轮换订阅链接
//-----------------------------------------------------------------------------

// meSubscriptionLinks 返回当前用户的订阅链接。
//
// 链接原文是从数据库里的密文解出来的（见 subscription.ListLinks）。
// 顺带回传近 24 小时的不同来源数：这条数字明显偏高，基本就意味着
// 链接被分享出去了 —— 把判断依据摆在用户自己眼前，比我们替他猜要好。
func (h *handlers) meSubscriptionLinks(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}

	links, err := h.d.Subscription.ListLinks(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	type view struct {
		SubscriptionID  string `json:"subscription_id"`
		URL             string `json:"url"`
		ExpiresAt       any    `json:"expires_at"`
		FetchCount      int64  `json:"fetch_count"`
		LastFetchedAt   any    `json:"last_fetched_at"`
		DistinctSources int    `json:"distinct_sources_24h"`
	}
	out := make([]view, 0, len(links))
	for _, l := range links {
		out = append(out, view{
			SubscriptionID:  l.SubscriptionID,
			URL:             h.d.Cfg.PublicBaseURL + "/" + l.PathPrefix + "/" + l.Token,
			ExpiresAt:       l.ExpiresAt,
			FetchCount:      l.FetchCount,
			LastFetchedAt:   l.LastFetchedAt,
			DistinctSources: l.DistinctSources,
		})
	}
	httpx.OK(w, map[string]any{"links": out})
}

// meSubscriptionNodes 只返回面板展示所需的非敏感节点摘要。
// 连接地址、端口与协议配置留在订阅分发边界内，不能经 JSON 泄露给页面。
func (h *handlers) meSubscriptionNodes(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	nodes, err := h.d.Subscription.ListOwnedNodePreviews(
		r.Context(), p.TenantID, p.UserID, chi.URLParam(r, "id"),
	)
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	type nodeView struct {
		Name        string  `json:"name"`
		Protocol    string  `json:"protocol"`
		TrafficRate float64 `json:"traffic_rate"`
	}
	out := make([]nodeView, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, nodeView{
			Name: node.Name, Protocol: node.Protocol, TrafficRate: node.TrafficRate,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.OK(w, map[string]any{"count": len(out), "nodes": out})
}

// rotateSubscriptionLink 换一条新链接，旧的立即失效。
func (h *handlers) rotateSubscriptionLink(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	subID := chi.URLParam(r, "id")

	token, err := h.d.Subscription.Rotate(r.Context(), p.TenantID, p.UserID, subID)
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	prefix, _ := h.d.Subscription.PathPrefix(r.Context(), p.TenantID)
	h.d.Log.Info("用户轮换订阅链接", "subscription", subID, "user", p.UserID)
	httpx.OK(w, map[string]any{
		"url": h.d.Cfg.PublicBaseURL + "/" + prefix + "/" + token,
	})
}

// subscribeTarget 读显式指定的订阅格式。
//
// 正常路径上没人会用它——UA 就够了，用户拿到的是一个到处能用的链接。
// 它是给两种情况留的：在浏览器里想看某个具体格式，以及某个客户端的
// UA 我们还没认出来、需要临时绕过。
//
// 认三个参数名。target 是我们自己的，flag 是 Xboard / V2board 那边的
// 叫法，从别的面板迁过来的人手上的链接就带着它；type 是有些教程里的
// 写法。名字不认识就当没传，回落到 UA 判断——那个结果通常也是对的。
func subscribeTarget(r *http.Request) string {
	q := r.URL.Query()
	for _, key := range []string{"target", "flag", "type"} {
		if v := strings.TrimSpace(q.Get(key)); v != "" {
			return v
		}
	}
	return ""
}
