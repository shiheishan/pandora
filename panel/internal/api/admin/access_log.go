package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 全站访问明细。
//
// 数据一直在采集，只是分散在两张表里、而且没人把它们放在一起看过：
// audit_events 记登录注册这类账号动作，subscription_fetch_log 记订阅拉取。
// 风控要判断的问题（这个账号今天从几个地方登录过、这个 IP 碰过哪些账号）
// 横跨两者，分开看就得来回对时间戳。
//
// # 明文 IP 从哪来
//
// 两张表都存了信封加密的 IP（见 00025）。密文躺在库里，密钥在别处，
// 后台展示时才解开。拖库的人拿不到 IP，管理员拿得到——这是既有设计，
// 这里只是照着用。
//
// # 归属地为什么现算不存
//
// IP 归属会变：今天的家宽段明年可能划给机房。存快照意味着历史记录里
// 留着一份越来越不准的判断，而风控恰恰要按「现在这个 IP 是什么」来看。
// 解析是纯内存计算，带缓存后开销可以忽略。

type accessLogItem struct {
	Category    string    `json:"category"`
	Action      string    `json:"action,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	UserEmail   string    `json:"user_email,omitempty"`
	IP          string    `json:"ip,omitempty"`
	Geo         string    `json:"geo,omitempty"`
	NetworkKind string    `json:"network_kind,omitempty"`
	UserAgent   string    `json:"user_agent,omitempty"`
	Outcome     string    `json:"outcome,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// accessLogList 返回按时间倒序的访问明细。
func (h *handlers) accessLogList(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	q := r.URL.Query()

	limit := 50
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("limit"))); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("offset"))); err == nil && v > 0 {
		offset = v
	}
	// category 决定查哪张表。空值表示两张都要。
	category := strings.ToLower(strings.TrimSpace(q.Get("category")))
	include, exclude, known := auditCategoryFilter(category)
	if !known && category != "subscribe" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"category": "不认识的分类"}))
		return
	}
	// other 的 include 为空、exclude 非空；「不过滤」是两者都为空
	filterByCategory := category != "" && category != "subscribe"
	// outcome：审计的四种结果之一，或 error（= 非 success）。订阅拉取只有 ok 与
	// 各种失败，ok 算 success、其余算 error；按 failure / denied / partial 筛时
	// 订阅日志整路不查
	outcome := strings.ToLower(strings.TrimSpace(q.Get("outcome")))
	switch outcome {
	case "", "success", "failure", "denied", "partial", "error":
	default:
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"outcome": "只支持 success、failure、denied、partial 或 error"}))
		return
	}
	includeFetches := outcome == "" || outcome == "success" || outcome == "error"

	// 按 IP 筛选只能走哈希。IP 在库里是密文，SQL 里没法比较，但同一个
	// HMAC 盐算出的哈希是稳定的，拿它做等值匹配既能走索引又不用解密全表。
	// 代价是只支持精确匹配，查不了网段——那需要明文，跟加密存储不兼容。
	// 两张表的哈希盐不同，得各算各的。审计表用主密钥 + 规范化，订阅表用
	// 派生盐 + 原样哈希——这是写入端定的，查询端只能跟着。用错的症状是
	// 「筛选永远查不到东西」且不报错。
	var auditIPHash, fetchIPHash any
	if v := strings.TrimSpace(q.Get("ip")); v != "" {
		auditIPHash = crypto.HashIdentifier(h.d.Cfg.MasterKey, v)
		fetchIPHash = crypto.HashRaw(crypto.SubscriptionAuditSalt(h.d.Cfg.MasterKey), v)
	}
	// 按账号筛选。传 UUID 走 actor_id，传别的当邮箱前缀模糊匹配——
	// 运营手里通常只有邮箱。
	userFilter := strings.TrimSpace(q.Get("user"))
	var actorID, emailLike any
	if userFilter != "" {
		if _, err := uuid.Parse(userFilter); err == nil {
			actorID = userFilter
		} else {
			emailLike = "%" + strings.ToLower(userFilter) + "%"
		}
	}

	audits, fetches, err := h.d.Ops.ListAccessLog(r.Context(), tenantID, adminops.AccessLogQuery{
		QueryAudit:       category == "" || category != "subscribe",
		QueryFetches:     (category == "" || category == "subscribe") && includeFetches,
		FilterByCategory: filterByCategory,
		Include:          nonNilStrings(include),
		Exclude:          nonNilStrings(exclude),
		AuditIPHash:      auditIPHash,
		FetchIPHash:      fetchIPHash,
		ActorID:          actorID,
		EmailLike:        emailLike,
		Outcome:          outcome,
		// 两张表结构不同，先各自取够 limit+offset 条，在应用层归并
		Limit: limit + offset,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	// 下面要把来源 IP 解成明文交出去：先留痕（审计台账 2.3 第 6 条），写不进去就不给
	filter := map[string]any{"limit": limit, "offset": offset}
	for k, v := range map[string]string{"category": category, "outcome": outcome,
		"ip": strings.TrimSpace(q.Get("ip")), "user": userFilter} {
		if v != "" {
			filter[k] = v
		}
	}
	if err := h.d.Ops.RecordSourceIPView(r.Context(), tenantID, httpx.PrincipalFrom(r.Context()).UserID,
		adminops.SourceIPViewAccessLog, "", filter); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	items := make([]accessLogItem, 0, limit)
	for _, a := range audits {
		it := accessLogItem{Action: a.Action, Outcome: a.Outcome, UserAgent: a.UserAgent,
			UserEmail: a.UserEmail, OccurredAt: a.OccurredAt}
		if a.ActorID != nil {
			it.UserID = *a.ActorID
		}
		it.Category = categoryFromAction(it.Action)
		it.IP = h.decryptIP(a.SourceIPEnc)
		items = append(items, it)
	}
	for _, f := range fetches {
		it := accessLogItem{Outcome: f.Result, UserEmail: f.UserEmail, OccurredAt: f.FetchedAt}
		if f.UserID != nil {
			it.UserID = *f.UserID
		}
		it.Category = "subscribe"
		it.Action = "subscription.fetch"
		// 订阅日志用自己的 AAD："subfetch"。用错 AAD 会解密失败而
		// 不是给出错值，密文被跨表挪动时能立刻发现。
		it.IP = h.decryptWith(f.IPEnc, "subfetch")
		it.UserAgent = h.decryptWith(f.UAEnc, "subfetch")
		items = append(items, it)
	}

	// 两路结果按时间归并后再切页。
	sortByOccurredDesc(items)
	// 注意别写成 offset >= len(items)：结果为空时 0 >= 0 也成立，会把空
	// 切片置成 nil，JSON 里就成了 "items": null 而不是 []。
	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset > 0 {
		items = items[:0]
	}
	if len(items) > limit {
		items = items[:limit]
	}

	// 归属地在切页之后才解析：只解这一页要展示的，翻页翻得再深也不会
	// 因为解析开销变慢。
	for i := range items {
		loc := h.d.GeoIP.Lookup(items[i].IP)
		items[i].Geo = loc.Display
		items[i].NetworkKind = string(loc.Kind)
	}

	httpx.OK(w, accessLogListResponse{Items: items})
}

type accessLogListResponse struct {
	Items []accessLogItem `json:"items"`
}

// accessCategoryRules 是审计动作到展示分类的唯一映射，按顺序匹配前缀，先命中者胜。
//
// 展示归类（categoryFromAction）和按分类筛选（auditCategoryFilter）都从这里
// 来：原先筛选只认 login/register，选「管理端」「订单」等分类时审计表根本
// 不过滤（缺陷 14）。两处各写一份迟早又会对不上。
//
// admin 排在 payment 前面：payment_provider.* 也以 payment 开头，原先 payment
// 先匹配，后台改支付渠道的动作全被归成了用户支付。
var accessCategoryRules = []struct {
	category string
	prefixes []string
}{
	{"login", []string{"user.login"}},
	{"register", []string{"user.registered"}},
	{"reset_password", []string{"user.password", "user.reset"}},
	{"order", []string{"order."}},
	// 管理侧动作：节点状态变更、路由组（00096）、后台引导、渠道开关这些。它们和用户行为
	// 混在一张表里，但风控看的是两回事，分开标出来才不会互相淹没。
	{"admin", []string{"node.", "route_group.", "adminctl.", "server.", "plan.", "payment_provider."}},
	{"payment", []string{"payment"}},
	{"ticket", []string{"ticket."}},
}

// categoryFromAction 把 action 归到展示用的分类。
// 认不出来的一律归到 other，而不是硬塞进某一类——后台看到 other 会去
// 补映射，塞错分类则永远不会有人发现。
func categoryFromAction(action string) string {
	for _, rule := range accessCategoryRules {
		for _, p := range rule.prefixes {
			if strings.HasPrefix(action, p) {
				return rule.category
			}
		}
	}
	return "other"
}

// auditCategoryFilter 把分类翻成 SQL 用的两组前缀：action 要命中 include
// 之一、且不命中 exclude 之一（排在它前面的规则，保持「先命中者胜」）。
// other 没有 include，只排除全部规则。空分类返回 ok 且两组都为 nil，表示不过滤；
// subscribe 不走审计表，调用方另行处理；不认识的分类返回 ok=false。
func auditCategoryFilter(category string) (include, exclude []string, ok bool) {
	if category == "" {
		return nil, nil, true
	}
	var earlier []string
	for _, rule := range accessCategoryRules {
		if rule.category == category {
			return rule.prefixes, earlier, true
		}
		earlier = append(earlier, rule.prefixes...)
	}
	if category == "other" {
		return nil, earlier, true
	}
	return nil, nil, false
}

// nonNilStrings 让空切片以空数组而不是 NULL 传进 SQL：unnest(NULL) 与
// cardinality(NULL) 的语义和空数组不同，统一成空数组省得每处都判 NULL。
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// sortByOccurredDesc 按时间倒序。数据量是 limit+offset 级别（最多几百条），
// 插入排序足够，不值得引入额外依赖。
func sortByOccurredDesc(items []accessLogItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].OccurredAt.After(items[j-1].OccurredAt); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
