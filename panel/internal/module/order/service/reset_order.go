package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

var errOpenReset = errs.Newf(errs.Conflict, "这份订阅还有一张未完成的流量重置订单，请先支付或取消")

// resetBasis 取流量重置的判断依据：限额流量配额行里周期最长的那一行（没有周期末的 total 行最长，其余按
// period_end − period_start 比，一样长取排在前面的）。没有限额流量配额回 false（不限流量的订阅不卖重置）。
// 卖不卖（loadResetOffer）与付款到账时还收不收（resetPayable）的跨周期判断都只看这一行：同一份同时有日限额与
// 周期限额时，日行每天滚动不该让买「清空本期流量」的单进挂账。履约 ResetTraffic 照旧清全部流量配额行。
func resetBasis(quotas []subsvc.Quota) (subsvc.Quota, bool) {
	var best subsvc.Quota
	found := false
	for _, q := range quotas {
		if q.Metric != subsvc.MetricTraffic || q.Limit == nil {
			continue
		}
		if !found || longerPeriod(q, best) {
			best, found = q, true
		}
	}
	return best, found
}

// longerPeriod 判断配额行 a 的周期是否比 b 长（没有周期末算无限长）。
func longerPeriod(a, b subsvc.Quota) bool {
	if a.PeriodEnd == nil || b.PeriodEnd == nil {
		return a.PeriodEnd == nil && b.PeriodEnd != nil
	}
	return a.PeriodEnd.Sub(a.PeriodStart) > b.PeriodEnd.Sub(b.PeriodStart)
}

// periodEnded 判断配额行这一期已经结束、还在等滚动任务（10 分钟一次）把它推到下一期：这段间隙里清的是已结束那一期
// 的用量（滚动本来就会清零），新一期的起点又会写成边界值而不是滚动时刻，靠比对起点挡不住，所以按周期末判断。
func periodEnded(q subsvc.Quota, now time.Time) bool {
	return q.PeriodEnd != nil && !q.PeriodEnd.After(now)
}

// resetOffer 是一份订阅此刻买流量重置的报价快照。
type resetOffer struct {
	Sub      subsvc.Subscription
	PlanName string
	Version  catalogsvc.Version
	Kind     string
	Amount   decimal.Decimal
}

// loadResetOffer 判断这份订阅此刻能不能买流量重置。能买回快照；不能买时 refusal 是给用户看的原因
// （不生效 subsvc.ErrNotLive；没有限额流量配额或套餐没配重置价 ErrResetNotSold；作依据的配额行这一期已结束、
// 还没滚到下一期 ErrResetPeriodSwitching；重置次数已用完 ErrResetLimitReached / ErrResetLimitReachedOneTime），系统错误走 err。
func (s *Service) loadResetOffer(ctx context.Context, sub subsvc.Subscription, now time.Time) (off resetOffer, refusal error, err error) {
	if sub.StateAt(now) != subsvc.StateLive {
		return resetOffer{}, subsvc.ErrNotLive, nil
	}
	quotas, err := s.subs.Quotas(ctx, sub.ID)
	if err != nil {
		return resetOffer{}, nil, err
	}
	basis, ok := resetBasis(quotas)
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
	if plan.ResetPrice == nil {
		return resetOffer{}, ErrResetNotSold, nil
	}
	amt := *plan.ResetPrice
	if periodEnded(basis, now) {
		return resetOffer{}, ErrResetPeriodSwitching, nil
	}
	if plan.ResetLimit != nil {
		used, err := s.subs.ResetsUsed(ctx, sub.ID)
		if err != nil {
			return resetOffer{}, nil, err
		}
		if used >= *plan.ResetLimit {
			if plan.Kind == catalogsvc.KindOneTime {
				return resetOffer{}, ErrResetLimitReachedOneTime, nil
			}
			return resetOffer{}, ErrResetLimitReached, nil
		}
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
		Kind:     plan.Kind,
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

// quoteResetLines 流量重置报价：一条；这份订阅不卖重置（不生效、没有限额流量配额、套餐没配重置价）时回空切片。
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
	if errors.Is(refusal, ErrResetPeriodSwitching) || errors.Is(refusal, ErrResetLimitReached) || errors.Is(refusal, ErrResetLimitReachedOneTime) {
		// 这份卖重置，只是此刻在两期之间或次数已满：回原因让门户据此提示，而不是当作不卖
		return nil, refusal
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

// resetPayable 判断一张待付的流量重置单此刻还收不收钱（结算时调，调用方已锁订阅）：订阅仍在生效中、套餐版本仍是建单时
// 订单行上的那一版、仍有限额的流量配额，且作依据的那一行（resetBasis）这一期没结束、起点没在建单后变过。
// 不收时钱进挂账（CaseIneligibleSubscription）。
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
	basis, ok := resetBasis(quotas)
	if !ok {
		return false, nil
	}
	// 跨周期两种情形都不收（设边界为 T，滚动任务在 T 之后至多约 10 分钟才把行推到下一期，新一期起点写 T）：
	//   - 这一期已结束、还没滚（付款落在 T 到滚动之间）：清的是已结束那一期的用量，滚动本来就会清零；
	//   - 起点晚于建单时刻（建单在 T 之前、付款在滚动之后）：已自动清零过，再清就是让用户花钱清新一期刚用的一点。
	// 建单落在 T 到滚动之间的，loadResetOffer 已按「这一期已结束」拒卖。两个时刻都是应用时钟（订单 created_at 由 GORM
	// 插入时取，配额起止由 subscription 按应用时钟算出的边界写入），与换套餐单 sub.PeriodStart.After(o.CreatedAt) 同一思路。
	if periodEnded(basis, now) || basis.PeriodStart.After(o.CreatedAt) {
		return false, nil
	}
	return true, nil
}
