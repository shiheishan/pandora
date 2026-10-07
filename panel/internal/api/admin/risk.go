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
	includeReviewed := r.URL.Query().Get("include_reviewed") == "1"
	clusters, err := h.d.Ops.ListIPClusters(r.Context(), httpx.TenantIDFrom(r.Context()), includeReviewed)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 下面要把来源 IP 解成明文交出去：先留痕（审计台账 2.3 第 6 条），写不进去就不给
	if err := h.d.Ops.RecordSourceIPView(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, adminops.SourceIPViewIPClusters, "",
		map[string]any{"include_reviewed": includeReviewed, "clusters": len(clusters)}); err != nil {
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
	httpx.OK(w, ipClustersResponse{Clusters: out})
}

type ipClustersResponse struct {
	Clusters []ipClusterView `json:"clusters"`
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
	httpx.OK(w, reviewIPClusterResponse{Key: key, Decision: "normal",
		ExpiresAt: expires.Format(time.RFC3339)})
}

type reviewIPClusterResponse struct {
	Key       string `json:"key"`
	Decision  string `json:"decision"`
	ExpiresAt string `json:"expires_at"`
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
