// [INPUT]: 依赖 domain/adminops 的 ListIPClusters / ReviewIPCluster / DisableIPClusterAccounts，依赖同包 profile.go 的 decryptIP 与 Deps.GeoIP 归属地，依赖 platform 的 geoip/httpx
// [OUTPUT]: 对外提供 ipClusters、reviewIPCluster、disableIPClusterAccounts 三个处理器与 clusterRisk 风险分级
// [POS]: api/admin 的风控聚类处理器（后台-09「风控」卡片）；明文 IP、归属地与风险等级在这一层补上，领域层只给密文与账号
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// clusterRisk 给聚类分级。机房出口挂着多个账号几乎只有一种解释（批量注册
// 或代理转售），所以不看账号数直接判高；住宅、教育网、移动网络下多人共用
// 出口是常态，只按账号数分级。
func clusterRisk(accounts int, kind geoip.NetworkKind) string {
	switch {
	case accounts >= 5 || kind == geoip.KindDatacenter:
		return "high"
	case accounts >= 3:
		return "mid"
	default:
		return "low"
	}
}

type ipClusterView struct {
	IP          string `json:"ip"`
	Geo         string `json:"geo"`
	NetworkKind string `json:"network_kind"`
	Risk        string `json:"risk"`
	adminops.IPCluster
}

// ipClusters 列出关联到多个账号的来源地址。
//
// 这是主动发现批量注册的入口：不必先怀疑某个人，直接看哪些 IP 下面
// 挂着一串账号。标记为正常且未过期的默认不列，?include_reviewed=1 时列出。
func (h *handlers) ipClusters(w http.ResponseWriter, r *http.Request) {
	clusters, err := h.d.Ops.ListIPClusters(r.Context(), httpx.TenantIDFrom(r.Context()),
		r.URL.Query().Get("include_reviewed") == "1")
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out := make([]ipClusterView, 0, len(clusters))
	for _, c := range clusters {
		v := ipClusterView{IP: h.decryptIP(c.IPEnc), IPCluster: c}
		loc := h.d.GeoIP.Lookup(v.IP)
		v.Geo, v.NetworkKind = loc.Display, string(loc.Kind)
		v.Risk = clusterRisk(c.Accounts, loc.Kind)
		out = append(out, v)
	}
	httpx.OK(w, map[string]any{"clusters": out})
}

func (h *handlers) reviewIPCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note string `json:"note"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	key := chi.URLParam(r, "key")
	expires, err := h.d.Ops.ReviewIPCluster(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, key, req.Note)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"key": key, "decision": "normal",
		"expires_at": expires.Format(time.RFC3339)})
}

func (h *handlers) disableIPClusterAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserIDs []string `json:"user_ids"`
		Reason  string   `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Ops.DisableIPClusterAccounts(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "key"), req.UserIDs, req.Reason)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
