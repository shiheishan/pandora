package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// overview 是分销的整体情况与当前参数。
func (h *Handler) overview(c *gin.Context) {
	o, err := h.svc.AdminOverview(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := overviewFrom(o)
	if err != nil {
		httpx.Fail(c, errs.Wrap(errs.Internal, err))
		return
	}
	httpx.OK(c, resp)
}

// listWithdrawals 返回最近 200 笔提现（可按状态筛），收款信息已解封，管理员据此打款。
func (h *Handler) listWithdrawals(c *gin.Context) {
	var q listWithdrawalsQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := h.svc.AdminWithdrawals(c.Request.Context(), q.Status)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]adminWithdrawalResponse, 0, len(rows))
	for _, r := range rows {
		row, err := adminWithdrawalFrom(r)
		if err != nil {
			httpx.Fail(c, errs.Wrap(errs.Internal, err))
			return
		}
		out = append(out, row)
	}
	httpx.OK(c, listWithdrawalsResponse{Withdrawals: out})
}

// pathID 解析路径里的 :id；不是 UUID 回 404。
func pathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}

// reviewWithdrawal 批准或驳回一笔提现。
func (h *Handler) reviewWithdrawal(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req reviewRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Action != "approve" && req.Action != "reject" {
		httpx.Fail(c, errs.Invalid(map[string]string{"action": "操作只能是 approve 或 reject"}))
		return
	}
	status, err := h.svc.ReviewWithdrawal(c.Request.Context(), id, req.Action == "approve", req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, reviewResponse{Status: status})
}

// markWithdrawalPaid 记录一笔已批准的提现已实际打款（唯一动账本的一步）。
func (h *Handler) markWithdrawalPaid(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req markPaidRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.MarkWithdrawalPaid(c.Request.Context(), id, req.PayoutReference); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, markPaidResponse{Status: "paid"})
}

// setConfig 改分销参数（比例、冻结天数、最低提现额、计佣范围），给出的字段才改。
func (h *Handler) setConfig(c *gin.Context) {
	var req configRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetConfig(c.Request.Context(), req.configToUpdate()); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

// listReviewEntries 是待复核佣金列表（review=required，按计提时间倒序键集翻页）。
func (h *Handler) listReviewEntries(c *gin.Context) {
	var q listEntriesQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	if q.Review != "required" {
		httpx.Fail(c, errs.Invalid(map[string]string{"review": "目前只支持 review=required"}))
		return
	}
	page, err := h.svc.AdminReviewEntries(c.Request.Context(), q.Cursor, q.Limit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := reviewEntriesFrom(page)
	if err != nil {
		httpx.Fail(c, errs.Wrap(errs.Internal, err))
		return
	}
	httpx.OK(c, resp)
}

// reviewEntry 通过或作废一条待复核的佣金。
func (h *Handler) reviewEntry(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req reviewRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Action != "approve" && req.Action != "void" {
		httpx.Fail(c, errs.Invalid(map[string]string{"action": "操作只能是 approve 或 void"}))
		return
	}
	status, err := h.svc.ReviewEntry(c.Request.Context(), id, req.Action == "approve", req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, reviewResponse{Status: status})
}
