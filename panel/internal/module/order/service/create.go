package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// hasNUL 判断字符串是否含 NUL（PG 文本列存不了）。
func hasNUL(s string) bool {
	return strings.ContainsRune(s, 0)
}

// rejectNUL 含 NUL 时回 422，字段名对应请求 JSON。
func rejectNUL(field, s string) error {
	if hasNUL(s) {
		return errs.Invalid(map[string]string{field: "不能包含非法字符"})
	}
	return nil
}

// snapshotJSON 把套餐版本权益与配额编成订单行快照 JSON。
func snapshotJSON(v catalogsvc.Version) (ents, quotas string, err error) {
	type entSnap struct {
		Code  string          `json:"code"`
		Value json.RawMessage `json:"value"`
	}
	entOut := make([]entSnap, 0, len(v.Entitlements))
	for _, e := range v.Entitlements {
		entOut = append(entOut, entSnap{Code: e.Code, Value: e.Value})
	}
	type quotaSnap struct {
		Metric string `json:"metric"`
		Limit  *int64 `json:"limit"`
		Unit   string `json:"unit"`
		Period string `json:"period"`
	}
	quotaOut := make([]quotaSnap, 0, len(v.Quotas))
	for _, q := range v.Quotas {
		quotaOut = append(quotaOut, quotaSnap{
			Metric: q.Metric,
			Limit:  q.Limit,
			Unit:   q.Unit,
			Period: q.Period,
		})
	}
	var eb, qb []byte
	eb, err = json.Marshal(entOut)
	if err != nil {
		return "", "", err
	}
	qb, err = json.Marshal(quotaOut)
	if err != nil {
		return "", "", err
	}
	return string(eb), string(qb), nil
}

// pendingNewOrderConflict 检查同一套餐的待付新购单。
func pendingNewOrderConflict(ctx context.Context, s *Service, userID, planID uuid.UUID, planName string, now time.Time) error {
	pending, ok, err := s.repo.PendingNewOrderForPlan(ctx, userID, planID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	lapsed := !pending.ExpiresAt.After(now)
	msg := "你有一张还没付款的「" + planName + "」订单，继续付款或取消后再买"
	if lapsed {
		msg = "你有一张已超过付款期限的「" + planName + "」订单，取消后再买"
	}
	e := errs.Newf(errs.OrderPending, msg)
	e.Fields = map[string]string{"order_id": pending.ID.String()}
	if lapsed {
		e.Fields["lapsed"] = "true"
	}
	return e
}

// validateNewCopyLabel 新购备注名校验：起了名不能与本人另一份重名；
// 另买一份且不起名时，本人同套餐还有没起名、未停用的一份就要求先起名。
func validateNewCopyLabel(in NewOrderInput, planName string, us subsvc.UserSubscriptions, now time.Time) error {
	label, err := subsvc.NormalizeLabel(in.Label)
	if err != nil {
		return err
	}
	if label != "" {
		for _, sum := range us.Subscriptions {
			if sum.Label != nil && strings.EqualFold(*sum.Label, label) {
				return errs.Invalid(map[string]string{"label": "这个名字已经用在另一份上了，换一个吧"})
			}
		}
		return nil
	}
	if !in.NewCopy {
		return nil
	}
	for _, sum := range us.Subscriptions {
		if sum.PlanID != in.PlanID {
			continue
		}
		if sum.Label != nil {
			continue
		}
		if sum.StateAt(now) != subsvc.StateEnded {
			return errs.Invalid(map[string]string{
				"label": "不起名的话，App 里会有两个「" + planName + "」，分不清哪个是哪个，先给它起个名字",
			})
		}
	}
	return nil
}

// newOrderItemFromOffer 用下单快照组新购的订单行（版本取套餐当前发布版本）。
func newOrderItemFromOffer(offer catalogsvc.Offer, price catalogsvc.Price) (*model.Item, error) {
	ents, quotas, err := snapshotJSON(offer.Version)
	if err != nil {
		return nil, err
	}
	ver := offer.Version.Version
	interval := price.BillingInterval
	ic := price.IntervalCount
	planID := offer.Plan.ID
	verID := offer.Version.ID
	priceID := price.ID
	return &model.Item{
		PriceID:               &priceID,
		PlanID:                &planID,
		PlanVersionID:         &verID,
		SnapshotName:          offer.Plan.Name,
		SnapshotPlanVersion:   &ver,
		SnapshotInterval:      &interval,
		SnapshotIntervalCount: &ic,
		SnapshotEntitlements:  ents,
		SnapshotQuotas:        quotas,
		Quantity:              1,
		UnitAmount:            price.UnitAmount,
		LineAmount:            price.UnitAmount,
	}, nil
}

// renewalOrderItem 组续费的订单行：版本沿用订阅自己的，价格用这次选的价格档。
func renewalOrderItem(sub subsvc.Subscription, ver catalogsvc.Version, price catalogsvc.Price, planName string) (*model.Item, error) {
	ents, quotas, err := snapshotJSON(ver)
	if err != nil {
		return nil, err
	}
	vno := ver.Version
	interval := price.BillingInterval
	ic := price.IntervalCount
	planID := sub.PlanID
	verID := sub.PlanVersionID
	priceID := price.ID
	return &model.Item{
		PriceID:               &priceID,
		PlanID:                &planID,
		PlanVersionID:         &verID,
		SnapshotName:          planName,
		SnapshotPlanVersion:   &vno,
		SnapshotInterval:      &interval,
		SnapshotIntervalCount: &ic,
		SnapshotEntitlements:  ents,
		SnapshotQuotas:        quotas,
		Quantity:              1,
		UnitAmount:            price.UnitAmount,
		LineAmount:            price.UnitAmount,
	}, nil
}

// CreateNewOrder 门户新购。
func (s *Service) CreateNewOrder(ctx context.Context, in NewOrderInput) (Created, error) {
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
	if err := rejectNUL("label", in.Label); err != nil {
		return Created{}, err
	}

	var created Created
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.rejectDeleted(ctx, userID); err != nil {
			return err
		}
		now := s.clock()
		label, err := subsvc.NormalizeLabel(in.Label)
		if err != nil {
			return err
		}
		uid := userID
		offer, err := s.catalog.Offer(ctx, in.PriceID, catalogsvc.OfferRequest{
			Use:    catalogsvc.UseNew,
			UserID: &uid,
			PlanID: &in.PlanID,
			Lock:   true,
			Now:    now,
		})
		if err != nil {
			return err
		}
		if !in.NewCopy {
			if _, ok, err := s.subs.RenewableForPlan(ctx, userID, in.PlanID, false); err != nil {
				return err
			} else if ok {
				return ErrSamePlanUseRenewal
			}
		}
		us, err := s.subs.ByUser(ctx, userID)
		if err != nil {
			return err
		}
		if err := validateNewCopyLabel(in, offer.Plan.Name, us, now); err != nil {
			return err
		}
		if err := pendingNewOrderConflict(ctx, s, userID, in.PlanID, offer.Plan.Name, now); err != nil {
			return err
		}
		limit := offer.Plan.PurchaseLimitPerUser
		if err := s.checkPurchaseLimit(ctx, in.PlanID, userID, limit); err != nil {
			return err
		}
		item, err := newOrderItemFromOffer(offer, offer.Price)
		if err != nil {
			return err
		}
		var labelPtr *string
		if label != "" {
			labelPtr = &label
		}
		limited := limit != nil
		created, _, err = s.place(ctx, draft{
			Kind:         KindNew,
			UserID:       userID,
			Currency:     offer.Price.Currency,
			Subtotal:     offer.Price.UnitAmount,
			CouponPlanID: &in.PlanID,
			Label:        labelPtr,
			Item:         item,
			NewPurchase:  &newPurchase{PlanID: in.PlanID, Limited: limited},
			Checkout:     in.Checkout,
			Actor:        actor,
			AuditAction:  ActionOrderCreated,
			AuditData: map[string]any{
				"plan_version": offer.Version.Version,
				"new_copy":     in.NewCopy,
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

// CreateRenewalOrder 门户续费。
func (s *Service) CreateRenewalOrder(ctx context.Context, in RenewalOrderInput) (Created, error) {
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
		sub, err := s.subs.Lock(ctx, in.SubscriptionID, &userID)
		if err != nil {
			return err
		}
		created, _, err = s.placeRenewal(ctx, renewalDraft{
			Sub:      sub,
			PriceID:  in.PriceID,
			UserID:   userID,
			Checkout: in.Checkout,
			Actor:    actor,
			Audit:    ActionRenewalCreated,
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

// renewalDraft 是一张续费单的输入（门户与后台人工单共用）。调用方已锁住 Sub。
type renewalDraft struct {
	Sub subsvc.Subscription
	// PriceID 为 nil 时续订阅当前的价格档
	PriceID  *uuid.UUID
	UserID   uuid.UUID
	Checkout Checkout
	Manual   *manualDraft
	Actor    Actor
	// Audit 为空时不写审计（人工单自己写）
	Audit string
}

// placeRenewal 在调用方事务里建一张续费单：订阅未停用、没有未完结的续费或变更单，价格档属于订阅的套餐。
func (s *Service) placeRenewal(ctx context.Context, d renewalDraft) (Created, model.Order, error) {
	now := s.clock()
	sub := d.Sub
	if !sub.StateAt(now).AcceptsChange() {
		return Created{}, model.Order{}, subsvc.ErrRenewalWindowClosed
	}
	open, err := s.repo.OpenChangeOrderExists(ctx, sub.ID)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	if open {
		return Created{}, model.Order{}, ErrOpenChange
	}
	priceID := d.PriceID
	if priceID == nil {
		priceID = sub.Price.PriceID
	}
	if priceID == nil {
		return Created{}, model.Order{}, errs.Newf(errs.Conflict, "这条订阅没有关联价格，无法自动续费")
	}
	uid := d.UserID
	// 续费不锁套餐行（Lock: false）：价格行由冻结触发器钉死不可改，续费也不动库存与限购，
	// 锁了只会让同套餐的续费与新购在整段建单事务里排队，还多出一条与结算成环的路径，不值得。
	// 不锁的代价只在后台恰好同时关掉「允许续费」或下架价格的那一瞬间：这张单按改之前的状态建成，可接受。
	offer, err := s.catalog.Offer(ctx, *priceID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseRenewal,
		UserID: &uid,
		PlanID: &sub.PlanID,
		Manual: d.Manual != nil,
		Lock:   false,
		Now:    now,
	})
	if err != nil {
		return Created{}, model.Order{}, err
	}
	verMap, err := s.catalog.VersionsByIDs(ctx, []uuid.UUID{sub.PlanVersionID})
	if err != nil {
		return Created{}, model.Order{}, err
	}
	ver, ok := verMap[sub.PlanVersionID]
	if !ok {
		return Created{}, model.Order{}, catalogsvc.ErrNoPublishedVersion
	}
	item, err := renewalOrderItem(sub, ver, offer.Price, offer.Plan.Name)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	subID := sub.ID
	return s.place(ctx, draft{
		Kind:           KindRenewal,
		UserID:         d.UserID,
		Currency:       offer.Price.Currency,
		Subtotal:       offer.Price.UnitAmount,
		CouponPlanID:   &sub.PlanID,
		SubscriptionID: &subID,
		Item:           item,
		Checkout:       d.Checkout,
		Manual:         d.Manual,
		Actor:          d.Actor,
		AuditAction:    d.Audit,
		AuditData: map[string]any{
			"subscription_id": sub.ID.String(),
			"price_id":        (*priceID).String(),
		},
	})
}

// CreatePackOrder 门户流量包下单。
func (s *Service) CreatePackOrder(ctx context.Context, in PackOrderInput) (Created, error) {
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
		pack, err := s.catalog.PackOffer(ctx, in.PackID, true)
		if err != nil {
			return err
		}
		// 先锁目标订阅再动券与科目：与流量包单结算「订单 → 订阅 → 券 → 科目」同向（零元单当场履约时也要锁它）
		if _, err := s.subs.Lock(ctx, in.SubscriptionID, &userID); err != nil {
			return err
		}
		if err := s.subs.CheckPackTarget(ctx, userID, in.SubscriptionID); err != nil {
			return err
		}
		tb := pack.TrafficBytes
		packSnap := []map[string]any{{
			"metric": catalogsvc.MetricTraffic,
			"limit":  tb,
			"unit":   "bytes",
			"period": catalogsvc.PeriodTotal,
		}}
		qb, err := json.Marshal(packSnap)
		if err != nil {
			return err
		}
		packID := pack.ID
		subID := in.SubscriptionID
		item := &model.Item{
			TrafficPackID:        &packID,
			SnapshotName:         pack.Name,
			SnapshotTrafficBytes: &tb,
			SnapshotQuotas:       string(qb),
			SnapshotEntitlements: "[]",
			Quantity:             1,
			UnitAmount:           pack.UnitAmount,
			LineAmount:           pack.UnitAmount,
		}
		created, _, err = s.place(ctx, draft{
			Kind:           KindAddon,
			UserID:         userID,
			Currency:       pack.Currency,
			Subtotal:       pack.UnitAmount,
			CouponPlanID:   nil,
			SubscriptionID: &subID,
			Item:           item,
			Checkout:       in.Checkout,
			Actor:          actor,
			AuditAction:    ActionOrderCreated,
			AuditData: map[string]any{
				"pack_id":       pack.ID.String(),
				"traffic_bytes": tb,
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

// CreateTopup 门户充值。
func (s *Service) CreateTopup(ctx context.Context, in TopupInput) (TopupCreated, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return TopupCreated{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return TopupCreated{}, err
	}
	actor := actorFrom(p)

	cur := in.Currency
	if cur == "" {
		cur = money.CNY
	}
	if !cur.Valid() {
		return TopupCreated{}, errs.Invalid(map[string]string{"currency": "充值币种只支持 CNY 或 USD"})
	}
	cents, err := money.ToCents(in.Amount)
	if err != nil {
		return TopupCreated{}, errs.Invalid(map[string]string{"amount": "金额格式不正确"})
	}
	if cents < TopupMinCents {
		return TopupCreated{}, errs.Invalid(map[string]string{"amount": "充值金额太小"})
	}
	if cents > TopupMaxCents {
		return TopupCreated{}, errs.Invalid(map[string]string{"amount": "单次充值金额超出上限"})
	}

	var topup TopupCreated
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.rejectDeleted(ctx, userID); err != nil {
			return err
		}
		created, _, err := s.place(ctx, draft{
			Kind:        KindTopup,
			UserID:      userID,
			Currency:    cur,
			Subtotal:    in.Amount,
			Checkout:    Checkout{},
			Actor:       actor,
			AuditAction: ActionTopupCreated,
		})
		if err != nil {
			return err
		}
		topup = TopupCreated{
			OrderID:  created.OrderID,
			OrderNo:  created.OrderNo,
			Amount:   in.Amount,
			Currency: cur,
		}
		return s.completeCreate(ctx, created.OrderID, func() (idemsvc.Response, error) {
			if in.Reply == nil {
				return idemsvc.Response{}, nil
			}
			return in.Reply(topup)
		})
	})
	if err != nil {
		return TopupCreated{}, err
	}
	return topup, nil
}
