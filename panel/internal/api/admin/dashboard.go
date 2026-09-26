// [INPUT]: 依赖 domain/adminops 的仪表盘读模型，依赖 platform/httpx 的主体与响应
// [OUTPUT]: 对外提供 handlers 的 dashboardNodeTraffic / dashboardUserTraffic / dashboardNotificationBacklog / dashboardTasks
// [POS]: api/admin 的仪表盘处理器：流量排行、通知积压与「需要处理」汇总（按调用方权限逐项过滤）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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

// dashboardTasks 是「需要处理」卡片与侧栏徽标：路由只要 ops.dashboard.read，
// 每一项再按调用方各自的读权限过滤，没权限的项不出现。
func (h *handlers) dashboardTasks(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Ops.DashboardTasks(r.Context(), httpx.TenantIDFrom(r.Context()), p.Can)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
