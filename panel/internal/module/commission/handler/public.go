package handler

import (
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// userID 取门户登录主体；没有主体回 401。
func userID(c *gin.Context) (uuid.UUID, bool) {
	p, ok := authn.FromGin(c)
	if !ok || p.UserID == uuid.Nil {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return uuid.Nil, false
	}
	return p.UserID, true
}

// myCommission 是门户「我的佣金」：汇总、佣金明细、提现记录、转余额记录。
func (h *Handler) myCommission(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	mc, err := h.svc.MyCommission(c.Request.Context(), uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := myCommissionFrom(mc)
	if err != nil {
		httpx.Fail(c, errs.Wrap(errs.Internal, err))
		return
	}
	httpx.OK(c, resp)
}

// requestWithdrawal 发起一笔提现申请。
func (h *Handler) requestWithdrawal(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	var req withdrawalRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if req.Amount <= 0 {
		fields["amount"] = "提现金额必须大于 0"
	}
	if strings.TrimSpace(req.PayoutDetail) == "" {
		fields["payout_detail"] = "请填写收款方式"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	id, err := h.svc.RequestWithdrawal(c.Request.Context(), uid, money.FromCents(req.Amount), req.PayoutDetail)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, withdrawalIDResponse{ID: id.String()})
}

// transferToBalance 把可提现佣金转成账户余额。
func (h *Handler) transferToBalance(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	var req transferRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Amount <= 0 {
		httpx.Fail(c, errs.Invalid(map[string]string{"amount": "转入金额必须大于 0"}))
		return
	}
	txnID, err := h.svc.TransferToBalance(c.Request.Context(), uid, money.FromCents(req.Amount))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, transferResponse{LedgerTxnID: txnID.String(), Amount: req.Amount})
}
