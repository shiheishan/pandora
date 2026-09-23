package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) revenueTimeseries(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil {
		days = 30
	}
	rows, err := h.d.Ops.RevenueTimeseries(r.Context(), httpx.TenantIDFrom(r.Context()), strings.ToUpper(r.URL.Query().Get("currency")), days)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"currency": strings.ToUpper(r.URL.Query().Get("currency")), "days": days, "points": rows})
}

func (h *handlers) revenueAdjustments(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.Ops.ListRevenueAdjustments(r.Context(), httpx.TenantIDFrom(r.Context()), strings.ToUpper(r.URL.Query().Get("currency")))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"adjustments": rows})
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
