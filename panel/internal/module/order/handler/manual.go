package handler

// 后台人工开单：预览「这单落到哪一份」与建单（JSON 形状对照旧 api/admin/manual_order.go 与 purchase.Placement）。

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// manualTargetReq 是选定的落点（预览给出的选项之一）。
type manualTargetReq struct {
	Kind           string `json:"kind"`
	SubscriptionID string `json:"subscription_id"`
}

// manualOrderRequest 是 POST /orders/manual。settlement 为空按 grant；reference 只有 offline 必填；
// target 在选项只有一个时可以不传。
type manualOrderRequest struct {
	UserID     string           `json:"user_id"`
	PlanID     string           `json:"plan_id"`
	PriceID    string           `json:"price_id"`
	Reason     string           `json:"reason"`
	Settlement string           `json:"settlement"`
	Reference  string           `json:"reference"`
	Target     *manualTargetReq `json:"target,omitempty"`
}

// manualPreviewRequest 是 POST /orders/manual/preview；price_id、entry_subscription_id 可空。
type manualPreviewRequest struct {
	UserID              string `json:"user_id"`
	PlanID              string `json:"plan_id"`
	PriceID             string `json:"price_id"`
	EntrySubscriptionID string `json:"entry_subscription_id"`
}

// placementResponse 是一个落点选项（旧 ManualPlacement：purchase.Placement 加 due、below_minimum）。
type placementResponse struct {
	Key            string     `json:"key"`
	Kind           string     `json:"kind"`
	SubscriptionID string     `json:"subscription_id,omitempty"`
	Expired        bool       `json:"expired,omitempty"`
	Badge          string     `json:"badge,omitempty"`
	Label          string     `json:"label,omitempty"`
	PlanID         string     `json:"plan_id,omitempty"`
	PlanName       string     `json:"plan_name,omitempty"`
	State          string     `json:"state,omitempty"`
	PeriodEnd      *time.Time `json:"period_end,omitempty"`
	NewPeriodEnd   *time.Time `json:"new_period_end,omitempty"`
	Credit         int64      `json:"credit,omitempty"`
	Currency       string     `json:"currency,omitempty"`
	GiftDaysLost   int        `json:"gift_days_lost,omitempty"`
	TrafficUsed    int64      `json:"traffic_used,omitempty"`
	TrafficCap     int64      `json:"traffic_cap,omitempty"`
	PackRemaining  int64      `json:"pack_remaining,omitempty"`
	Due            int64      `json:"due"`
	BelowMinimum   bool       `json:"below_minimum"`
	// UnavailableReason 非空表示这一项开不了，内容与建单会回的错误相同
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// manualPreviewResponse 是开单预览（default_key 为空表示不预选）。
type manualPreviewResponse struct {
	Options    []placementResponse `json:"options"`
	DefaultKey string              `json:"default_key"`
	MinPayment int64               `json:"min_payment"`
}

func manualPreviewFrom(p service.ManualPreview) (manualPreviewResponse, error) {
	var cc cents
	out := manualPreviewResponse{
		Options:    make([]placementResponse, 0, len(p.Options)),
		DefaultKey: p.DefaultKey,
		MinPayment: cc.of(p.MinPayment),
	}
	for _, o := range p.Options {
		out.Options = append(out.Options, placementResponse{
			Key:               o.Key,
			Kind:              o.Kind,
			SubscriptionID:    idOrEmpty(o.SubscriptionID),
			Expired:           o.Expired,
			Badge:             o.Badge,
			Label:             o.Label,
			PlanID:            idOrEmpty(o.PlanID),
			PlanName:          o.PlanName,
			State:             string(o.State),
			PeriodEnd:         o.PeriodEnd,
			NewPeriodEnd:      o.NewPeriodEnd,
			Credit:            cc.of(o.Credit),
			Currency:          string(o.Currency),
			GiftDaysLost:      o.GiftDaysLost,
			TrafficUsed:       o.TrafficUsed,
			TrafficCap:        o.TrafficCap,
			PackRemaining:     o.PackRemaining,
			Due:               cc.of(o.Due),
			BelowMinimum:      o.BelowMinimum,
			UnavailableReason: o.Unavailable,
		})
	}
	return out, cc.err
}

// manualTarget 把请求里的落点转成 service 的 Choice；没给或 kind 为空时为 nil。
func manualTarget(t *manualTargetReq) (*service.Choice, error) {
	if t == nil || strings.TrimSpace(t.Kind) == "" {
		return nil, nil
	}
	ch := &service.Choice{Kind: strings.TrimSpace(t.Kind)}
	if raw := strings.TrimSpace(t.SubscriptionID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, errs.Invalid(map[string]string{"target": "这个用法现在不能用了，请刷新后再选"})
		}
		ch.SubscriptionID = &id
	}
	return ch, nil
}

// previewManualOrder 开单页「这单落到哪一份」：选项、每项会发生什么与默认值。只读。
func (h *Handler) previewManualOrder(c *gin.Context) {
	var req manualPreviewRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	userID, err := parseID("user_id", req.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	planID, err := parseID("plan_id", req.PlanID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	priceID, err := optID("price_id", req.PriceID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	entry, err := optID("entry_subscription_id", req.EntrySubscriptionID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.ManualPreview(c.Request.Context(), service.ManualPreviewInput{
		UserID:              userID,
		PlanID:              planID,
		PriceID:             priceID,
		EntrySubscriptionID: entry,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := manualPreviewFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// createManualOrder 替用户开单：赠送（缺省，当场履约）、待用户支付，或线下已收款（带凭证号，当场结清）。
// 201 响应与幂等重放同一个转换（落成换套餐单时多 proration_credit、balance_refund 两项）。
func (h *Handler) createManualOrder(c *gin.Context) {
	var req manualOrderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	for k, v := range map[string]string{"user_id": req.UserID, "plan_id": req.PlanID, "price_id": req.PriceID} {
		if strings.TrimSpace(v) == "" {
			fields[k] = "必填"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	userID, err := parseID("user_id", req.UserID)
	if err != nil {
		httpx.Fail(c, err)
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
	target, err := manualTarget(req.Target)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreateManualOrder(c.Request.Context(), service.ManualOrderInput{
		UserID:     userID,
		PlanID:     planID,
		PriceID:    priceID,
		Reason:     req.Reason,
		Settlement: req.Settlement,
		Reference:  req.Reference,
		Target:     target,
		Reply:      createdReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeCreated(c, created)
}
