package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
	httpx.OK(w, map[string]any{"logs": rows, "total": total})
}

func (h *handlers) trafficResetStats(w http.ResponseWriter, r *http.Request) {
	st, err := h.d.Billing.TrafficResetStats(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, st)
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
	httpx.OK(w, map[string]any{"logs": rows, "total": total})
}

type manualResetReq struct {
	Note string `json:"note"`
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
	httpx.OK(w, map[string]any{"reset": true, "freed_bytes": freed})
}
