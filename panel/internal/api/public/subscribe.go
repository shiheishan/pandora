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
	"log/slog"
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
	// 来源地址与限流、审计同一口径（httpx.ClientIP）：只认 nginx 覆写的 X-Real-IP，
	// 不采信客户端能任意填写的 X-Forwarded-For。这个 IP 进订阅拉取日志，风控靠它
	// 按来源关联同一条链接的拉取
	ip := httpx.ClientIP(r)
	family := subscription.UAFamily(ua)
	format := subscription.DetectFormat(ua, subscribeTarget(r))

	// 客户端常给链接加个扩展名让自己认得出格式，去掉再比对
	token := rawToken
	for _, ext := range []string{".yaml", ".yml", ".json", ".txt", ".conf"} {
		token = strings.TrimSuffix(token, ext)
	}

	// 全部读取：前缀（进程内缓存，前缀不对连库都不碰）、凭据与用量（一个只读事务）、
	// 节点（按套餐版本与用户组缓存）
	pull, err := h.d.Subscription.LoadPull(ctx, tenantID, prefix, token)
	if errors.Is(err, subscription.ErrNotFound) {
		// 前缀或令牌不对、凭据失效：扫描器的主要流量。按来源采样落库，不是每次都写
		if dropped := h.d.Subscription.RecordUnauthenticated(ctx, tenantID, ip, ua, family); dropped > 0 {
			h.d.Log.Warn("订阅未认证失败超出采样额度，上一窗口未落库", "dropped", dropped)
		}
		writeDecoy(w)
		return
	}
	if err != nil {
		// 库错误、超时、数据缺失：对外照样回伪装页（一个 500 页面同样能告诉探测者
		// 这里有东西），对内必须留下 ERROR，否则会被当成「令牌不存在」而隐形。
		// 日志不带令牌与请求路径
		attrs := []any{slog.String("request_id", httpx.RequestIDFrom(ctx)), "err", err}
		if pull != nil && pull.Cred != nil {
			attrs = append(attrs, "subscription", pull.Cred.SubscriptionID)
			h.d.Subscription.Log(ctx, tenantID, pull.Cred.ID, pull.Cred.SubscriptionID,
				"error", string(format), ip, ua, family, 0, 0)
		}
		h.d.Log.Error("订阅拉取读库失败", attrs...)
		writeDecoy(w)
		return
	}
	cred := pull.Cred

	body, contentType, count := subscription.Render(format, pull.Nodes, cred.ProxyUUID)

	// 所有读取完成后再原子检查限流、写成功日志并记下拉取次数；失败请求不占额度。
	if err := h.d.Subscription.RecordSuccessfulFetch(ctx, tenantID, cred.ID, cred.SubscriptionID,
		string(format), ip, ua, family, count, len(body), cred.RateLimit); err != nil {
		if errors.Is(err, subscription.ErrRateLimited) {
			h.d.Subscription.Log(ctx, tenantID, cred.ID, cred.SubscriptionID,
				"rate_limited", string(format), ip, ua, family, count, 0)
			w.Header().Set("Retry-After", "3600")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		h.d.Log.Error("订阅限流计数失败", slog.String("request_id", httpx.RequestIDFrom(ctx)),
			"subscription", cred.SubscriptionID, "err", err)
		writeDecoy(w)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// 这个头是客户端显示「已用 / 总量 / 到期」的唯一来源，
	// 各家客户端都认它，格式不能改
	w.Header().Set("Subscription-Userinfo", formatUserinfo(pull.Usage))
	// 让客户端知道多久回来拉一次，避免有人几秒钟拉一回
	w.Header().Set("Profile-Update-Interval", "12")
	// 订阅内容随节点状态变化，不能被任何中间层缓存
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
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

//-----------------------------------------------------------------------------
// 面板侧：查看与轮换订阅链接
//-----------------------------------------------------------------------------

// meSubscriptionLinks 返回当前用户的订阅链接。
//
// 链接原文是从数据库里的密文解出来的（见 subscription.ListLinks）。
// 顺带回传近 24 小时的不同来源数：这条数字明显偏高，基本就意味着
// 链接被分享出去了 —— 把判断依据摆在用户自己眼前，比我们替他猜要好。
func (h *handlers) meSubscriptionLinks(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	type linksResponse struct {
		Links []view `json:"links"`
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
	httpx.OK(w, linksResponse{Links: out})
}

// meSubscriptionNodes 只返回面板展示所需的非敏感节点摘要。
// 连接地址、端口与协议配置留在订阅分发边界内，不能经 JSON 泄露给页面。
func (h *handlers) meSubscriptionNodes(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	type nodesResponse struct {
		Count int        `json:"count"`
		Nodes []nodeView `json:"nodes"`
	}
	out := make([]nodeView, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, nodeView{
			Name: node.Name, Protocol: node.Protocol, TrafficRate: node.TrafficRate,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.OK(w, nodesResponse{Count: len(out), Nodes: out})
}

type rotateLinkResponse struct {
	URL string `json:"url"`
}

// rotateSubscriptionLink 换一条新链接，旧的立即失效。
func (h *handlers) rotateSubscriptionLink(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	httpx.OK(w, rotateLinkResponse{
		URL: h.d.Cfg.PublicBaseURL + "/" + prefix + "/" + token,
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
