// [INPUT]: 依赖 domain/adminops 的 RevenueTimeseries / ListRevenueAdjustments / CreateRevenueAdjustment / ReverseRevenueAdjustment，依赖 platform/httpx
// [OUTPUT]: 对包内提供 revenueTimeseries、revenueAdjustments、createRevenueAdjustment、reverseRevenueAdjustment 四个处理器；成功响应为具名 DTO（*Response）
// [POS]: api/admin 的仪表盘收入趋势（带上一区间合计 previous_total）与收入调整（新建 / 冲销带 Idempotency-Key），路由在 router_dashboard.go

package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type revenueTimeseriesResponse struct {
	Currency      string                  `json:"currency"`
	Days          int                     `json:"days"`
	Points        []adminops.RevenuePoint `json:"points"`
	PreviousTotal int64                   `json:"previous_total"`
}

func (h *handlers) revenueTimeseries(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil {
		days = 30
	}
	rows, previous, err := h.d.Ops.RevenueTimeseries(r.Context(), httpx.TenantIDFrom(r.Context()), strings.ToUpper(r.URL.Query().Get("currency")), days)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, revenueTimeseriesResponse{Currency: strings.ToUpper(r.URL.Query().Get("currency")), Days: days,
		Points: rows, PreviousTotal: previous})
}

type revenueAdjustmentsResponse struct {
	Adjustments []adminops.RevenueAdjustment `json:"adjustments"`
}

func (h *handlers) revenueAdjustments(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Ops.ListRevenueAdjustments(r.Context(), httpx.TenantIDFrom(r.Context()), strings.ToUpper(r.URL.Query().Get("currency")))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, revenueAdjustmentsResponse{Adjustments: rows})
}

type revenueAdjustmentReq struct {
	Currency    string `json:"currency"`
	Amount      int64  `json:"amount"`
	Reason      string `json:"reason"`
	EffectiveOn string `json:"effective_on"`
}

func (h *handlers) createRevenueAdjustment(w http.ResponseWriter, r *http.Request) {
	var req revenueAdjustmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Ops.CreateRevenueAdjustment(r.Context(), httpx.TenantIDFrom(r.Context()), p.UserID, adminops.CreateRevenueAdjustmentInput{Currency: req.Currency, Amount: req.Amount, Reason: req.Reason, EffectiveOn: req.EffectiveOn, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

type reverseRevenueReq struct {
	Reason string `json:"reason"`
}

func (h *handlers) reverseRevenueAdjustment(w http.ResponseWriter, r *http.Request) {
	var req reverseRevenueReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Ops.ReverseRevenueAdjustment(r.Context(), httpx.TenantIDFrom(r.Context()), p.UserID, chi.URLParam(r, "id"), req.Reason, r.Header.Get("Idempotency-Key"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
