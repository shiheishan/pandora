package admin

import (
	"net/http"
	"strconv"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func dashboardTrafficQuery(r *http.Request) (adminops.DashboardTrafficQuery, error) {
	q := r.URL.Query()
	out := adminops.DashboardTrafficQuery{
		Range:      q.Get("range"),
		SnapshotAt: q.Get("snapshot_at"),
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return out, httpx.New(httpx.CodeValidationFailed, "limit 只能是 5、10 或 20")
		}
		out.Limit = limit
	}
	return out, nil
}

func (h *handlers) dashboardNodeTraffic(w http.ResponseWriter, r *http.Request) {
	in, err := dashboardTrafficQuery(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Ops.DashboardNodeTraffic(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) dashboardUserTraffic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("identity") {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "identity 参数已停用，用户排行固定返回脱敏身份"))
		return
	}
	in, err := dashboardTrafficQuery(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Ops.DashboardUserTraffic(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) dashboardNotificationBacklog(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Ops.DashboardNotificationBacklog(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
