package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listLatePayments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	cases, total, pending, err := h.d.Billing.ListLatePayments(r.Context(),
		httpx.TenantIDFrom(r.Context()),
		billing.ListLatePaymentsInput{Status: q.Get("status"), Limit: limit, Offset: offset})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"cases": cases, "total": total, "pending_amount": pending,
	})
}

type applyLatePaymentReq struct {
	Reason string `json:"reason"`
}

func (h *handlers) applyLatePayment(w http.ResponseWriter, r *http.Request) {
	var req applyLatePaymentReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeBadRequest, "请求体无法解析"))
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	txnID, err := h.d.Billing.ApplyLatePaymentToBalance(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.ApplyLatePaymentInput{
			CaseID: chi.URLParam(r, "id"), ActorID: principal.UserID, Reason: req.Reason})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ledger_txn_id": txnID})
}
