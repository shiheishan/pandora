package userload

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// ---------------------------------------------------------------------------
// 订阅客户端 UA：每条打中 subscription.DetectFormat 的一个分支
// ---------------------------------------------------------------------------
//
// format 是 DetectFormat 应当给出的格式，测试逐条拿真函数核对；跑的时候按响应的
// Content-Type 再核一次（yaml=clash、json=singbox、text/plain=uri），对不上记 format_mismatch。
// weight 是粗略的客户端占比，只决定每个模拟用户固定用哪个客户端。

type uaProfile struct {
	ua     string
	client string
	format string
	weight int
}

var subscriptionUAs = []uaProfile{
	// Clash 内核系分支（clash / mihomo / stash 字样）
	{ua: "mihomo/1.19.3", client: "mihomo", format: "clash", weight: 4},
	{ua: "ClashMetaForAndroid/2.11.5.Meta", client: "clash-meta-android", format: "clash", weight: 4},
	{ua: "clash-verge/v2.2.3", client: "clash-verge", format: "clash", weight: 3},
	{ua: "Stash/2.4.7 Clash/1.9.0", client: "stash", format: "clash", weight: 1},
	// sing-box 内核系分支（sing-box / SFA / SFI / hiddify 字样）
	{ua: "sing-box 1.11.4", client: "sing-box", format: "singbox", weight: 2},
	{ua: "SFA/1.11.4 (1; sing-box 1.11.4; language zh_CN)", client: "sfa", format: "singbox", weight: 1},
	{ua: "HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", client: "hiddify", format: "singbox", weight: 1},
	// 只认 URI/base64 的分支（排在 clash 分支之前判定）
	{ua: "v2rayN/7.10.5", client: "v2rayn", format: "uri", weight: 3},
	{ua: "v2rayNG/1.9.30", client: "v2rayng", format: "uri", weight: 2},
	{ua: "Shadowrocket/2070 CFNetwork/1568.200.51 Darwin/24.1.0", client: "shadowrocket", format: "uri", weight: 3},
	{ua: "NekoBox/Android/1.3.4 (Prefer ClashMeta Format)", client: "nekobox", format: "uri", weight: 1},
}

// uaForUser 把第 i 个用户固定到一个客户端：同一个人不会今天 Clash 明天 Shadowrocket。
func uaForUser(i int) uaProfile {
	total := 0
	for _, p := range subscriptionUAs {
		total += p.weight
	}
	// 乘一个与权重总数互素的奇数打散相邻用户，避免前几百个用户全落在同一个客户端上
	n := (i*7919 + 13) % total
	for _, p := range subscriptionUAs {
		if n < p.weight {
			return p
		}
		n -= p.weight
	}
	return subscriptionUAs[0]
}

// contentTypeFormat 由响应的 Content-Type 反推面板实际渲染的格式（render.go 的三种出口）。
func contentTypeFormat(ct string) string {
	switch {
	case strings.Contains(ct, "yaml"):
		return "clash"
	case strings.Contains(ct, "json"):
		return "singbox"
	case strings.Contains(ct, "text/plain"):
		return "uri"
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// 读接口表：门户与后台页面打开时真实发出的 GET
// ---------------------------------------------------------------------------
//
// path 里的 {sub} / {id} / {q} 在发送前替换；tmpl 进报告。权重近似页面打开频率：
// 外框每页都发的（me、订阅、角标）高，二级页面的低。

type readSpec struct {
	path   string
	tmpl   string
	weight int
}

// portalReads 来自 frontend/src/portal（调用处见各行注释），全部在 api/public/router.go 的需登录组。
var portalReads = []readSpec{
	{path: "/v1/me", weight: 3},                            // portal/queries.ts useMe
	{path: "/v1/me/subscriptions", weight: 4},              // portal/queries.ts useSubscriptions
	{path: "/v1/me/balance", weight: 2},                    // portal/queries.ts useBalance
	{path: "/v1/me/notifications?limit=1", weight: 3},      // portal/queries.ts 外框未读角标
	{path: "/v1/me/commission", weight: 1},                 // portal/queries.ts useCommissionAvailable
	{path: "/v1/me/subscription-links", weight: 2},         // common/subscriptions.ts useSubscriptionLinks
	{path: "/v1/me/subscriptions/{sub}/nodes", weight: 1},  // common/subscriptions.ts 节点预览
	{path: "/v1/me/subscriptions/{sub}/usage", weight: 1},  // common/subscriptions.ts 按日用量
	{path: "/v1/me/announcements", weight: 2},              // common/announcements.ts
	{path: "/v1/orders?status=pending_payment", weight: 2}, // common/orders.ts usePendingOrders（总览）
	{path: "/v1/plans", weight: 1},                         // common/catalog.ts usePlans
	{path: "/v1/me/notifications?limit=100", weight: 1},    // messages/api.ts 收件箱
	{path: "/v1/orders?status=" + historyStatuses + "&limit=6&offset=0", // common/orders.ts 订单历史首页
		tmpl: "/v1/orders?status=history", weight: 1},
}

const historyStatuses = "paid,fulfilled,cancelled,expired,partially_refunded,refunded"

// adminReads 来自 frontend/src/admin（调用处见各行注释），全部在 api/admin 的 router_*.go，平台管理员都有权限。
var adminReads = []readSpec{
	{path: "/v1/me", weight: 1},                                                      // admin/me.ts
	{path: "/v1/dashboard/tasks", weight: 2},                                         // admin/tasks.ts 侧栏待办
	{path: "/v1/overview", weight: 2},                                                // dash/api.ts
	{path: "/v1/dashboard/traffic/nodes?range=24h&limit=5", weight: 1},               // dash/api.ts
	{path: "/v1/dashboard/traffic/users?range=24h&limit=5", weight: 1},               // dash/api.ts
	{path: "/v1/system/status", weight: 1},                                           // dash/api.ts
	{path: "/v1/users?limit=25&offset=0", weight: 3},                                 // users/api.ts 用户列表
	{path: "/v1/users?q={q}&limit=100&offset=0", tmpl: "/v1/users?q={q}", weight: 2}, // users/api.ts 按邮箱搜
	{path: "/v1/users/{id}", weight: 2},                                              // users/api.ts 用户抽屉
	{path: "/v1/orders?limit=25&offset=0", weight: 2},                                // billing/api.ts 订单列表
	{path: "/v1/nodes?limit=1000&include_retired=1", weight: 2},                      // nodes/queries.ts 节点表
	{path: "/v1/tickets?status=open,pending_user,pending_agent,escalated&limit=25&offset=0", // tickets/api.ts 工单队列
		tmpl: "/v1/tickets?status=open", weight: 1},
	{path: "/v1/devices", weight: 1},     // users/api.ts 设备策略
	{path: "/v1/ip-clusters", weight: 1}, // security/queries.ts 风控 IP 聚类
}

// templateOf 是路径的报告名：显式 tmpl 优先，否则就是 path 本身（占位符原样保留）。
func (s readSpec) templateOf() string {
	if s.tmpl != "" {
		return s.tmpl
	}
	return s.path
}

type picker struct {
	specs []readSpec
	total int
}

func newPicker(specs []readSpec) picker {
	p := picker{specs: specs}
	for _, s := range specs {
		p.total += s.weight
	}
	return p
}

func (p picker) pick() readSpec {
	n := rand.IntN(p.total)
	for _, s := range p.specs {
		if n < s.weight {
			return s
		}
		n -= s.weight
	}
	return p.specs[0]
}

// ---------------------------------------------------------------------------
// 模拟用户与后台会话
// ---------------------------------------------------------------------------

// actor 是一个模拟用户：来源 IP 与订阅客户端在整个运行里固定，门户令牌登录一次后复用。
type actor struct {
	u     ltkit.ManifestUser
	ua    uaProfile
	token atomic.Pointer[string]
	subID atomic.Pointer[string]
}

func (a *actor) currentToken() string {
	if p := a.token.Load(); p != nil {
		return *p
	}
	return ""
}

func (a *actor) setToken(t string) { a.token.Store(&t) }

// dropToken 只在令牌还是出错时那一枚时清掉，不误伤并发登录刚换上的新令牌。
func (a *actor) dropToken(t string) {
	if p := a.token.Load(); p != nil && *p == t {
		a.token.CompareAndSwap(p, nil)
	}
}

// adminActor 是一个后台操作员：一个固定来源 IP 一条会话。后台按 IP 每分钟 240 次限流，
// 要压更高的后台速率就多给几个 IP（-admin-ips），相当于多个运营同时在线。
type adminActor struct {
	ip    string
	token string
}

// ---------------------------------------------------------------------------
// 四类流量的单次动作
// ---------------------------------------------------------------------------

type traffic struct {
	c      *client
	rec    *ltkit.Recorder
	pub    *gateway
	adm    *gateway
	prefix string
	users  []*actor // 全体用户：订阅拉取轮转
	pool   []*actor // 门户活跃用户：登录与页面读取
	admins []*adminActor
	pass   string
	portal picker
	admin  picker

	subNext   atomic.Uint64
	loginNext atomic.Uint64
}

// pullSubscription 模拟客户端定时更新订阅：全体用户轮转，不带登录态，UA 是这个用户固定的客户端。
func (t *traffic) pullSubscription(ctx context.Context) {
	a := t.users[int(t.subNext.Add(1)-1)%len(t.users)]
	want := a.ua.format
	t.c.do(ctx, t.rec, request{
		gw: t.pub, method: http.MethodGet,
		path: "/" + t.prefix + "/" + url.PathEscape(a.u.SubscribeToken),
		tmpl: "/{prefix}/{token} [" + want + "]",
		ip:   a.u.RealIP, ua: a.ua.ua,
		flag: func(resp response) string {
			switch {
			case resp.status == http.StatusNotFound:
				// 认证不过一律回同一个伪装 404：前缀或令牌不对、订阅过期都在这里
				return "decoy_404"
			case resp.status == http.StatusOK && contentTypeFormat(resp.header.Get("Content-Type")) != want:
				return "format_mismatch"
			}
			return ""
		},
	})
}

// portalRead 模拟已登录用户翻页面：从活跃池里随机挑一个有令牌的人，按权重挑一个读接口。
// 返回 false 表示这一拍没有可用会话（全池都还没登录上），由调度方记为丢弃。
func (t *traffic) portalRead(ctx context.Context) bool {
	a := t.pickSession()
	if a == nil {
		return false
	}
	tok := a.currentToken()
	s := t.portal.pick()
	path, tmpl := s.path, s.templateOf()
	if strings.Contains(path, "{sub}") {
		sub := a.subID.Load()
		if sub == nil {
			// 没有订阅的用户打开订阅页只会发列表请求
			path, tmpl = "/v1/me/subscriptions", "/v1/me/subscriptions"
		} else {
			path = strings.ReplaceAll(path, "{sub}", url.PathEscape(*sub))
			tmpl = strings.ReplaceAll(tmpl, "{sub}", "{id}")
		}
	}
	resp := t.c.do(ctx, t.rec, request{
		gw: t.pub, method: http.MethodGet, path: path, tmpl: tmpl,
		ip: a.u.RealIP, ua: browserUA, token: tok,
	})
	if resp.status == http.StatusUnauthorized {
		// 会话被踢或过期：清掉令牌，等登录那一类把它登回来
		a.dropToken(tok)
	}
	return true
}

func (t *traffic) pickSession() *actor {
	for range 8 {
		a := t.pool[rand.IntN(len(t.pool))]
		if a.currentToken() != "" {
			return a
		}
	}
	for _, a := range t.pool {
		if a.currentToken() != "" {
			return a
		}
	}
	return nil
}

// relogin 模拟用户重新登录（换设备、清了浏览器）：活跃池轮转，新令牌替换旧令牌。
// 旧会话不登出——真实用户也很少点登出，会话表照常增长。
func (t *traffic) relogin(ctx context.Context) {
	a := t.pool[int(t.loginNext.Add(1)-1)%len(t.pool)]
	if tok, _ := t.c.login(ctx, t.rec, t.pub, credentials{a.u.Email, t.pass}, a.u.RealIP); tok != "" {
		a.setToken(tok)
	}
}

// adminRead 模拟运营翻后台：随机一个操作员，按权重挑一个列表或详情接口。
func (t *traffic) adminRead(ctx context.Context) {
	op := t.admins[rand.IntN(len(t.admins))]
	s := t.admin.pick()
	u := t.users[rand.IntN(len(t.users))]
	path := strings.ReplaceAll(s.path, "{id}", url.PathEscape(u.u.ID))
	path = strings.ReplaceAll(path, "{q}", url.QueryEscape(u.u.Email))
	t.c.do(ctx, t.rec, request{
		gw: t.adm, method: http.MethodGet, path: path, tmpl: s.templateOf(),
		ip: op.ip, ua: browserUA, token: op.token,
	})
}

// firstSubscriptionID 从 GET /v1/me/subscriptions 的响应里取第一条订阅的 id。
func firstSubscriptionID(body []byte) string {
	var out struct {
		Subscriptions []struct {
			ID string `json:"id"`
		} `json:"subscriptions"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Subscriptions) == 0 {
		return ""
	}
	return out.Subscriptions[0].ID
}

// prefixFromLinks 从 GET /v1/me/subscription-links 的链接里取租户订阅前缀：
// 链接形如 <PublicBaseURL>/<prefix>/<token>，取倒数第二段。
func prefixFromLinks(body []byte) string {
	var out struct {
		Links []struct {
			URL string `json:"url"`
		} `json:"links"`
	}
	if json.Unmarshal(body, &out) != nil {
		return ""
	}
	for _, l := range out.Links {
		u, err := url.Parse(l.URL)
		if err != nil {
			continue
		}
		seg := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(seg) >= 2 && seg[len(seg)-2] != "" {
			return seg[len(seg)-2]
		}
	}
	return ""
}
