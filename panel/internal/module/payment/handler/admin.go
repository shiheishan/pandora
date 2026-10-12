package handler

import (
	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// listProviders 列出后台支付渠道卡片。
func (h *Handler) listProviders(c *gin.Context) {
	rows, err := h.svc.ListProviders(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	providers := make([]providerResponse, 0, len(rows))
	for _, row := range rows {
		p, err := providerFrom(row)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		providers = append(providers, p)
	}
	httpx.OK(c, listProvidersResponse{Providers: providers})
}

// toggleProvider 启停渠道与是否接受新单。
func (h *Handler) toggleProvider(c *gin.Context) {
	var req toggleProviderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.ToggleProvider(c.Request.Context(), c.Param("code"), req.Enabled, req.AcceptingNew); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toggleProviderResponse{OK: true})
}

// createProvider 新建支付渠道。
func (h *Handler) createProvider(c *gin.Context) {
	var req createProviderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.CreateProvider(c.Request.Context(), service.CreateProviderInput{
		Code: req.Code, Adapter: req.Adapter, ProviderSettings: req.settings(),
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, providerWriteFrom(res))
}

// updateProvider 编辑支付渠道配置。
func (h *Handler) updateProvider(c *gin.Context) {
	var req providerSettingsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.UpdateProvider(c.Request.Context(), c.Param("code"), req.settings())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, providerWriteFrom(res))
}

// listLatePayments 分页列出挂账案。
func (h *Handler) listLatePayments(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, 25, 100)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	list, err := h.svc.ListLatePayments(c.Request.Context(), c.Query("status"), limit, offset)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	cases := make([]lateCaseResponse, 0, len(list.Cases))
	for _, row := range list.Cases {
		lc, err := lateCaseFrom(row)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		cases = append(cases, lc)
	}
	pending, err := centsByCurrency(list.Pending)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, listLatePaymentsResponse{
		Cases: cases, Total: list.Total, PendingAmounts: pending,
	})
}

// applyLatePayment 将挂账金额转入用户余额。
func (h *Handler) applyLatePayment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req applyLatePaymentRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	txnID, err := h.svc.ApplyLatePayment(c.Request.Context(), id, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, applyLatePaymentResponse{LedgerTxnID: txnID.String()})
}
