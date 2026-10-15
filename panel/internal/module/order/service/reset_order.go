package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

var errOpenReset = errs.Newf(errs.Conflict, "这份订阅还有一张未完成的流量重置订单，请先支付或取消")

// hasLimitedTraffic 判断配额行里有没有限额的流量配额（不限流量的订阅不卖重置）。
func hasLimitedTraffic(quotas []subsvc.Quota) bool {
	for _, q := range quotas {
		if q.Metric == subsvc.MetricTraffic && q.Limit != nil {
			return true
		}
	}
	return false
}

// resetOffer 是一份订阅此刻买流量重置的报价快照。
type resetOffer struct {
	Sub      subsvc.Subscription
	PlanName string
	Version  catalogsvc.Version
	Currency money.Currency
	Amount   decimal.Decimal
}

// loadResetOffer 判断这份订阅此刻能不能买流量重置。能买回快照；不能买时 refusal 是给用户看的原因
// （不生效 subsvc.ErrNotLive；没有限额流量配额或套餐没配这个币种的重置价 ErrResetNotSold），系统错误走 err。
func (s *Service) loadResetOffer(ctx context.Context, sub subsvc.Subscription, now time.Time) (off resetOffer, refusal error, err error) {
	if sub.StateAt(now) != subsvc.StateLive {
		return resetOffer{}, subsvc.ErrNotLive, nil
	}
	quotas, err := s.subs.Quotas(ctx, sub.ID)
	if err != nil {
		return resetOffer{}, nil, err
	}
	if !hasLimitedTraffic(quotas) {
		return resetOffer{}, ErrResetNotSold, nil
	}
	cur := sub.Price.Currency
	amt, ok, err := s.catalog.ResetPrice(ctx, sub.PlanID, cur)
	if err != nil {
		return resetOffer{}, nil, err
	}
	if !ok {
		return resetOffer{}, ErrResetNotSold, nil
	}
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{sub.PlanID})
	if err != nil {
		return resetOffer{}, nil, err
	}
	plan, ok := plans[sub.PlanID]
	if !ok {
		return resetOffer{}, nil, catalogsvc.ErrNotFound
	}
	versions, err := s.catalog.VersionsByIDs(ctx, []uuid.UUID{sub.PlanVersionID})
	if err != nil {
		return resetOffer{}, nil, err
	}
	ver, ok := versions[sub.PlanVersionID]
	if !ok {
		return resetOffer{}, nil, catalogsvc.ErrNoPublishedVersion
	}
	return resetOffer{
		Sub:      sub,
		PlanName: plan.Name,
		Version:  ver,
		Currency: cur,
		Amount:   amt,
	}, nil, nil
}

// resetOrderItem 组流量重置的订单行：指向订阅当时的套餐与版本，没有价格档与周期；名字「流量重置 · <套餐名>」。
func resetOrderItem(off resetOffer) *model.Item {
	planID := off.Sub.PlanID
	verID := off.Sub.PlanVersionID
	vno := off.Version.Version
	return &model.Item{
		PlanID:               &planID,
		PlanVersionID:        &verID,
		SnapshotName:         "流量重置 · " + off.PlanName,
		SnapshotPlanVersion:  &vno,
		SnapshotEntitlements: "[]",
		SnapshotQuotas:       "[]",
		Quantity:             1,
		UnitAmount:           off.Amount,
		LineAmount:           off.Amount,
	}
}

// pendingResetConflict 检查这份订阅的待付流量重置单（回 order_pending，带 order_id，过了付款期限另带 lapsed）。
func pendingResetConflict(ctx context.Context, s *Service, subscriptionID uuid.UUID, now time.Time) error {
	pending, ok, err := s.repo.PendingResetOrderForSubscription(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	lapsed := !pending.ExpiresAt.After(now)
	msg := "你有一张还没付款的流量重置订单，继续付款或取消后再买"
	if lapsed {
		msg = "你有一张已超过付款期限的流量重置订单，取消后再买"
	}
	e := errs.Newf(errs.OrderPending, msg)
	e.Fields = map[string]string{"order_id": pending.ID.String()}
	if lapsed {
		e.Fields["lapsed"] = "true"
	}
	return e
}

// quoteResetLines 流量重置报价：一条；这份订阅不卖重置（不生效、没有限额流量配额、套餐没配这个币种的价）时回空切片。
func quoteResetLines(ctx context.Context, s *Service, userID uuid.UUID, in QuoteInput, now time.Time) ([]Quote, error) {
	if in.SubscriptionID == nil || *in.SubscriptionID == uuid.Nil {
		return nil, errs.Invalid(map[string]string{"subscription_id": "必填"})
	}
	sub, err := s.subs.Get(ctx, *in.SubscriptionID, &userID)
	if err != nil {
		return nil, err
	}
	off, refusal, err := s.loadResetOffer(ctx, sub, now)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return []Quote{}, nil
	}
	if err := pendingResetConflict(ctx, s, sub.ID, now); err != nil {
		return nil, err
	}
	planID := sub.PlanID
	subID := sub.ID
	q := Quote{
		SubscriptionID: &subID,
		PlanID:         &planID,
		Currency:       off.Currency,
		Subtotal:       off.Amount,
		Credit:         decimal.Zero,
		Refund:         decimal.Zero,
	}
	q.Discount = decimal.Zero
	if promosvc.NormalizeCode(in.CouponCode) != "" {
		msg := errs.As(promosvc.ErrCouponNotForReset).Message
		q.CouponError = &msg
	}
	q.Total = OrderTotal(q.Subtotal, decimal.Zero, decimal.Zero)
	return []Quote{q}, nil
}

// CreateResetOrder 门户买流量重置：锁订阅、复核能买、没有待付的重置单，走 place（可用余额，不用券、不打会员折扣；应付为 0 时当场履约清零）。
func (s *Service) CreateResetOrder(ctx context.Context, in ResetOrderInput) (Created, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return Created{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return Created{}, err
	}
	actor := actorFrom(p)
	if err := rejectNUL("coupon_code", in.CouponCode); err != nil {
		return Created{}, err
	}

	var created Created
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.rejectDeleted(ctx, userID); err != nil {
			return err
		}
		now := s.clock()
		sub, err := s.subs.Lock(ctx, in.SubscriptionID, &userID)
		if err != nil {
			return err
		}
		off, refusal, err := s.loadResetOffer(ctx, sub, now)
		if err != nil {
			return err
		}
		if refusal != nil {
			return refusal
		}
		if err := pendingResetConflict(ctx, s, sub.ID, now); err != nil {
			return err
		}
		planID := sub.PlanID
		subID := sub.ID
		created, _, err = s.place(ctx, draft{
			Kind:           KindTrafficReset,
			UserID:         userID,
			Currency:       off.Currency,
			Subtotal:       off.Amount,
			SubscriptionID: &subID,
			Item:           resetOrderItem(off),
			Checkout:       in.Checkout,
			Actor:          actor,
			AuditAction:    ActionOrderCreated,
			AuditData: map[string]any{
				"subscription_id": subID.String(),
				"plan_id":         planID.String(),
				"plan_version":    off.Version.Version,
			},
		})
		if err != nil {
			return err
		}
		return s.completeCreate(ctx, created.OrderID, func() (idemsvc.Response, error) {
			if in.Reply == nil {
				return idemsvc.Response{}, nil
			}
			return in.Reply(created)
		})
	})
	if err != nil {
		return Created{}, err
	}
	return created, nil
}

// resetPayable 判断一张待付的流量重置单此刻还收不收钱（结算时调，调用方已锁订阅）：订阅仍在生效中、仍有限额的流量配额、
// 配额周期起点没在建单后变过、套餐版本仍是建单时订单行上的那一版。不收时钱进挂账（CaseIneligibleSubscription）。
func (s *Service) resetPayable(ctx context.Context, o model.Order, sub subsvc.Subscription, now time.Time) (bool, error) {
	if sub.StateAt(now) != subsvc.StateLive {
		return false, nil
	}
	it, err := s.onlyItem(ctx, o)
	if err != nil {
		return false, err
	}
	if it.PlanVersionID == nil || *it.PlanVersionID != sub.PlanVersionID {
		return false, nil
	}
	quotas, err := s.subs.Quotas(ctx, sub.ID)
	if err != nil {
		return false, err
	}
	// 建单之后流量配额已滚到新周期（自动清零过），这笔再清就是让用户花钱清新周期刚用的一点；与换套餐单
	// sub.PeriodStart.After(o.CreatedAt) 同一思路，两个时刻都由应用时钟写入（订单 created_at 由 GORM 插入时取，
	// 配额 period_start 由 subscription 取 now）。不收时钱进挂账。
	for _, q := range quotas {
		if q.Metric == subsvc.MetricTraffic && q.PeriodStart.After(o.CreatedAt) {
			return false, nil
		}
	}
	return hasLimitedTraffic(quotas), nil
}
