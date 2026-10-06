// [INPUT]: 依赖 domain/billing 的 ListTrafficResets / TrafficResetStats / ManualResetTraffic，依赖 platform/httpx
// [OUTPUT]: 对包内提供 listTrafficResets、trafficResetStats、userTrafficResetHistory、manualResetTraffic 四个处理器；成功响应为具名 DTO（*Response）
// [POS]: api/admin 的流量重置日志、统计与人工重置，用户详情页的重置历史按用户过滤取最近 50 条，路由在 router_users.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type listTrafficResetsResponse struct {
	Logs  []billing.ResetLog `json:"logs"`
	Total int64              `json:"total"`
}

func (h *handlers) listTrafficResets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, total, err := h.d.Billing.ListTrafficResets(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.ListResetLogsInput{
			UserID: q.Get("user_id"), Reason: q.Get("reason"),
			Limit: limit, Offset: offset,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listTrafficResetsResponse{Logs: rows, Total: total})
}

func (h *handlers) trafficResetStats(w http.ResponseWriter, r *http.Request) {
	st, err := h.d.Billing.TrafficResetStats(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, st)
}

type userTrafficResetHistoryResponse struct {
	Logs  []billing.ResetLog `json:"logs"`
	Total int64              `json:"total"`
}

// userTrafficResetHistory 是按用户过滤的日志，供用户详情页直接调用。
func (h *handlers) userTrafficResetHistory(w http.ResponseWriter, r *http.Request) {
	rows, total, err := h.d.Billing.ListTrafficResets(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.ListResetLogsInput{
			UserID: chi.URLParam(r, "id"), Limit: 50,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, userTrafficResetHistoryResponse{Logs: rows, Total: total})
}

type manualResetReq struct {
	Note string `json:"note"`
}

type manualResetTrafficResponse struct {
	FreedBytes int64 `json:"freed_bytes"`
	Reset      bool  `json:"reset"`
}

func (h *handlers) manualResetTraffic(w http.ResponseWriter, r *http.Request) {
	var req manualResetReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	freed, err := h.d.Billing.ManualResetTraffic(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.ManualResetInput{
			UserID: chi.URLParam(r, "id"), ActorID: p.UserID, Note: req.Note,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, manualResetTrafficResponse{Reset: true, FreedBytes: freed})
}
