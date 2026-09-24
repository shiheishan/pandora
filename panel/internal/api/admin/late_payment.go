// [INPUT]: 依赖 domain/billing 的 ListLatePayments 与 ApplyLatePaymentToBalance，依赖 platform/httpx
// [OUTPUT]: 对包内提供 listLatePayments、applyLatePayment 两个处理器
// [POS]: api/admin 的挂账 tab：列表按币种返回待处理合计（pending_amounts，旧 pending_amount 过渡保留一版），转入余额走 router.go 的重认证与幂等链
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
		"cases": cases, "total": total, "pending_amounts": pending,
		"pending_amount": legacyPendingAmount(pending),
	})
}

// legacyPendingAmount 是旧字段 pending_amount 的原值：各币种直接相加，没有单位。
// 契约要求保留一个版本供过渡，新前端只读 pending_amounts；下个版本删掉。
func legacyPendingAmount(byCurrency map[string]int64) int64 {
	var sum int64
	for _, amount := range byCurrency {
		sum += amount
	}
	return sum
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
