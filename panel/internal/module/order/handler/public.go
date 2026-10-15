package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
)

// writeCreated 写建单的 201 响应（与幂等重放同一个转换）。
func writeCreated(c *gin.Context, created service.Created) {
	out, err := orderCreatedFrom(created)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, out)
}

// createOrder 门户新购。
func (h *Handler) createOrder(c *gin.Context) {
	var req createOrderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if strings.TrimSpace(req.PlanID) == "" {
		fields["plan_id"] = "必填"
	}
	if strings.TrimSpace(req.PriceID) == "" {
		fields["price_id"] = "必填"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	planID, err := parseID("plan_id", req.PlanID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	priceID, err := parseID("price_id", req.PriceID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreateNewOrder(c.Request.Context(), service.NewOrderInput{
		PlanID:   planID,
		PriceID:  priceID,
		NewCopy:  req.NewCopy,
		Label:    req.Label,
		Checkout: req.checkout(),
		Reply:    createdReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeCreated(c, created)
}

// createRenewal 门户续费：续哪一份由路径给出，price_id 为空续当前价格档。
func (h *Handler) createRenewal(c *gin.Context) {
	subID, ok := pathID(c)
	if !ok {
		return
	}
	var req createRenewalRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	priceID, err := optID("price_id", req.PriceID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreateRenewalOrder(c.Request.Context(), service.RenewalOrderInput{
		SubscriptionID: subID,
		PriceID:        priceID,
		Checkout:       req.checkout(),
		Reply:          createdReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeCreated(c, created)
}

// createPlanChange 门户换套餐：换哪一份由路径给出，plan_id 与 price_id 必填；金额与 as_of、expect 来自 action=change 的报价。
func (h *Handler) createPlanChange(c *gin.Context) {
	subID, ok := pathID(c)
	if !ok {
		return
	}
	var req createPlanChangeRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if strings.TrimSpace(req.PlanID) == "" {
		fields["plan_id"] = "必填"
	}
	if strings.TrimSpace(req.PriceID) == "" {
		fields["price_id"] = "必填"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	planID, err := parseID("plan_id", req.PlanID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	priceID, err := parseID("price_id", req.PriceID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreatePlanChangeOrder(c.Request.Context(), service.PlanChangeOrderInput{
		SubscriptionID: subID,
		PlanID:         planID,
		PriceID:        priceID,
		Checkout:       req.checkout(),
		Reply:          createdReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeCreated(c, created)
}

// createPackOrder 门户流量包下单：必须选加到哪一份。
func (h *Handler) createPackOrder(c *gin.Context) {
	var req createPackOrderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if strings.TrimSpace(req.PackID) == "" {
		fields["pack_id"] = "必填"
	}
	if strings.TrimSpace(req.SubscriptionID) == "" {
		fields["subscription_id"] = "请选择加到哪一份"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	packID, err := parseID("pack_id", req.PackID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	subID, err := parseID("subscription_id", req.SubscriptionID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreatePackOrder(c.Request.Context(), service.PackOrderInput{
		PackID:         packID,
		SubscriptionID: subID,
		Checkout:       req.checkout(),
		Reply:          createdReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeCreated(c, created)
}

// createTopup 门户充值（200）。
func (h *Handler) createTopup(c *gin.Context) {
	var req createTopupRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := rejectNUL(map[string]string{"currency": req.Currency}); err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreateTopup(c.Request.Context(), service.TopupInput{
		Amount:   money.FromCents(req.Amount),
		Currency: money.Currency(strings.TrimSpace(req.Currency)),
		Reply:    topupReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := topupCreatedFrom(created)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// quote 门户统一报价：不落库、不要幂等键。
func (h *Handler) quote(c *gin.Context) {
	var req quoteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := service.QuoteInput{
		Action:     strings.TrimSpace(req.Action),
		CouponCode: req.CouponCode,
		NewCopy:    req.NewCopy,
	}
	var err error
	if in.SubscriptionID, err = optID("subscription_id", req.SubscriptionID); err != nil {
		httpx.Fail(c, err)
		return
	}
	if in.PlanID, err = optID("plan_id", req.PlanID); err != nil {
		httpx.Fail(c, err)
		return
	}
	if in.PackID, err = optID("pack_id", req.PackID); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.Quote(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := quoteFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// pay 门户发起支付，返回收银台跳转信息（201）。
func (h *Handler) pay(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if h.lim != nil {
		p, ok := authn.From(ctx)
		if !ok {
			httpx.Fail(c, errs.New(errs.Unauthorized))
			return
		}
		if err := h.lim.Allow(ctx, ratelimit.PerMinute("order_pay", PayPerMinute), p.UserID.String()); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	var req payRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if strings.TrimSpace(req.Provider) == "" {
		httpx.Fail(c, errs.Invalid(map[string]string{"provider": "必填"}))
		return
	}
	res, err := h.svc.Pay(ctx, service.PayInput{
		OrderID:   id,
		Provider:  strings.TrimSpace(req.Provider),
		Method:    strings.TrimSpace(req.Method),
		ReturnURL: req.ReturnURL,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := payFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, out)
}

// cancel 门户取消自己的待付订单（无请求体；重复取消回成功）。
func (h *Handler) cancel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	res, err := h.svc.Cancel(c.Request.Context(), service.CancelInput{OrderID: id})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, releasedFrom(res))
}

// query 门户「我已支付，刷新状态」：向渠道查单，查到已付就补记。
func (h *Handler) query(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if h.lim != nil {
		p, ok := authn.From(ctx)
		if !ok {
			httpx.Fail(c, errs.New(errs.Unauthorized))
			return
		}
		if err := h.lim.Allow(ctx, ratelimit.PerMinute("order_query", QueryPerMinute), p.UserID.String()); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	res, err := h.svc.Query(ctx, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, queryFrom(res))
}

// myOrders 门户「我的订单」：status 逗号分隔多值。
func (h *Handler) myOrders(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, portalListDefault, listMax)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.MyOrders(c.Request.Context(), service.ListFilter{
		Statuses: splitStatuses(c.Query("status")),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := myOrdersFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// myOrder 门户订单详情。
func (h *Handler) myOrder(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	res, err := h.svc.MyOrder(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := myOrderDetailFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}
