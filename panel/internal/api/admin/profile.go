package admin

// 用户画像与风控统计。
//
// 三个问题要能回答：
//   这个账号都干了什么          → 行为时间线
//   还有谁和他从同一个地方来     → 关联账号
//   整体趋势有没有异常           → 时序统计
//
// 前两个要解密来源 IP，第三个只用哈希聚合就够 —— 画趋势图不需要知道
// 具体是哪个 IP，少解一次密就少一次泄露面。

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// decryptIP 还原一条审计记录里的来源地址。
// 解不开时返回空串而不是报错：早于加密上线的记录本来就没有密文。
func (h *handlers) decryptIP(enc []byte) string {
	return h.decryptWith(enc, "audit")
}

// decryptWith 按指定的 AAD 解密。
//
// 每张表用自己的 AAD：审计事件是 "audit"，订阅拉取日志是 "subfetch"。
// 用错 AAD 会解密失败而不是给出错值 —— 这正是要的效果，
// 密文被跨表挪动时能立刻发现，而不是安静地显示成另一个人的 IP。
func (h *handlers) decryptWith(enc []byte, aad string) string {
	if len(enc) == 0 || h.d.Envelope == nil {
		return ""
	}
	plain, err := h.d.Envelope.Open(enc, []byte(aad))
	if err != nil {
		return ""
	}
	return string(plain)
}

// userProfileEvent 是画像里的一条行为，IP 已解密。
type userProfileEvent struct {
	Action  string    `json:"action"`
	Outcome string    `json:"outcome"`
	IP      string    `json:"ip"`
	UA      string    `json:"ua"`
	Domain  string    `json:"domain"`
	At      time.Time `json:"at"`
}

// userProfileIP 是按来源 IP 归并的统计。
type userProfileIP struct {
	IP    string    `json:"ip"`
	Count int       `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	// Accounts 是同一 IP 下的其它账号数。大于 1 就值得看一眼 ——
	// 但共用出口 IP 在学校、公司、家庭网络里是常态，这只是线索。
	Accounts int `json:"accounts"`
}

// userProfileFetch 是一次订阅拉取，IP 与 UA 已解密。
type userProfileFetch struct {
	IP     string    `json:"ip"`
	UA     string    `json:"ua"`
	Family string    `json:"family"`
	Result string    `json:"result"`
	Format string    `json:"format"`
	At     time.Time `json:"at"`
}

// userProfileResponse 是 GET v1/users/{id}/profile 的响应；四个列表都非 nil。
type userProfileResponse struct {
	Events         []userProfileEvent  `json:"events"`
	IPs            []userProfileIP     `json:"ips"`
	Related        []adminops.UserPeer `json:"related"`
	Fetches        []userProfileFetch  `json:"fetches"`
	FetchSources7d int                 `json:"fetch_sources_7d"`
	RegisteredIP   string              `json:"registered_ip"`
}

// userProfile 返回一个用户的行为画像。
func (h *handlers) userProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	userID := chi.URLParam(r, "id")

	activity, err := h.d.Ops.UserActivity(r.Context(), tenantID, userID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	registeredIP := h.decryptIP(activity.RegisteredIPEnc)
	events := make([]userProfileEvent, 0, len(activity.Events))
	for _, e := range activity.Events {
		events = append(events, userProfileEvent{Action: e.Action, Outcome: e.Outcome, IP: h.decryptIP(e.SourceIPEnc),
			UA: e.UA, Domain: e.Domain, At: e.At})
	}
	ips := make([]userProfileIP, 0, len(activity.IPs))
	related := map[string]bool{}
	for _, st := range activity.IPs {
		for _, a := range st.AccountIDs {
			if a != userID {
				related[a] = true
			}
		}
		ips = append(ips, userProfileIP{IP: h.decryptIP(st.SourceIPEnc), Count: st.Count,
			First: st.First, Last: st.Last, Accounts: st.Accounts})
	}

	// 关联账号补上邮箱，光给 UUID 没法判断；读失败时照旧只给已读到的部分
	peers := []adminops.UserPeer{}
	if len(related) > 0 {
		list := make([]string, 0, len(related))
		for k := range related {
			list = append(list, k)
		}
		peers, _ = h.d.Ops.UserPeers(r.Context(), tenantID, list)
	}

	// 订阅拉取记录单独查一遍（判断链接是否被分享出去的证据），读失败同样不拦整个画像
	rawFetches, fetchSources, _ := h.d.Ops.UserFetches(r.Context(), tenantID, userID)
	fetches := make([]userProfileFetch, 0, len(rawFetches))
	for _, f := range rawFetches {
		fetches = append(fetches, userProfileFetch{IP: h.decryptWith(f.IPEnc, "subfetch"), UA: h.decryptWith(f.UAEnc, "subfetch"),
			Family: f.Family, Result: f.Result, Format: f.Format, At: f.At})
	}

	httpx.OK(w, userProfileResponse{
		Events: events, IPs: ips, Related: peers,
		Fetches: fetches, FetchSources7d: fetchSources,
		RegisteredIP: registeredIP,
	})
}

// statsTimeseries 返回按天聚合的行为统计，用于趋势图（只用哈希与计数，不解密）。
func (h *handlers) statsTimeseries(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	days := 14
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v >= 1 && v <= 90 {
		days = v
	}
	out, err := h.d.Ops.ActivityTimeseries(r.Context(), tenantID, days)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, statsTimeseriesResponse{Points: out})
}

type statsTimeseriesResponse struct {
	Points []adminops.TimeseriesPoint `json:"points"`
}
