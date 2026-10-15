package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 后台套餐的流量重置价格（00046 plan_reset_prices）：GET 读、PUT 整组替换；金额走分。

type resetPriceDTO struct {
	Currency   string `json:"currency"`
	UnitAmount int64  `json:"unit_amount"` // 分
}

type resetPricesRequest struct {
	ResetPrices []resetPriceDTO `json:"reset_prices"`
}

type resetPricesResponse struct {
	ResetPrices []resetPriceDTO `json:"reset_prices"` // 永不为 null
}

func resetPriceDTOFrom(p service.ResetPrice) resetPriceDTO {
	return resetPriceDTO{
		Currency:   string(p.Currency),
		UnitAmount: money.MustCents(p.UnitAmount),
	}
}

func resetPricesResponseFrom(rows []service.ResetPrice) resetPricesResponse {
	out := make([]resetPriceDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, resetPriceDTOFrom(row))
	}
	return resetPricesResponse{ResetPrices: out}
}

func resetPricesInputFrom(req resetPricesRequest) []service.ResetPrice {
	rows := req.ResetPrices
	if rows == nil {
		rows = []resetPriceDTO{}
	}
	out := make([]service.ResetPrice, 0, len(rows))
	for _, row := range rows {
		// 币种原样交给 service 校验，非法值回 422 并带字段位置
		out = append(out, service.ResetPrice{
			Currency:   money.Currency(row.Currency),
			UnitAmount: decimal.New(row.UnitAmount, -2),
		})
	}
	return out
}

func (h *Handler) getPlanResetPrices(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	rows, err := h.svc.PlanResetPrices(c.Request.Context(), planID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resetPricesResponseFrom(rows))
}

func (h *Handler) putPlanResetPrices(c *gin.Context) {
	planID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req resetPricesRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := resetPricesInputFrom(req)
	rows, err := h.svc.ReplacePlanResetPrices(c.Request.Context(), planID, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resetPricesResponseFrom(rows))
}
