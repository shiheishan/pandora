package service

// 后台人工开单（对照旧 manual_order.go、plan_change_manual.go）：
//
//	落点   由管理员选（预览给出的选项之一）：renew 在那一份上开续费单，change 在那一份上开换套餐单（剩余价值与门户同口径，
//	       赠送单新价算 0 元、剩余价值全额退进余额），new 另开一份
//	结算   grant 赠送（缺省，全额减免当场履约）、pending 待用户支付（低于最低额回 422）、
//	       offline 线下已收款（建单后在同一事务里经 payment.RecordOffline 结清）
//	审计   order.manual_created，与订单同一事务：没有审计的人工单等于没人负责
//
// 人工单不用券、不用余额；不受门户「同套餐只续不新开」与「同套餐只能有一张待付新购单」限制，限购与库存照常。

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// checkManualReason 校验开单原因：去掉首尾空白后 5 到 500 个字（库里 CHECK 只按字节兜底）。
func checkManualReason(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(r); n < reasonMinRunes || n > reasonMaxRunes || hasNUL(r) {
		return "", errs.Invalid(map[string]string{
			"reason": "请写清开单原因，5 到 500 个字。这条会进审计，是日后对账的唯一依据"})
	}
	return r, nil
}

// checkSettlement 规整结算方式（空按 grant）；offline 必须带线下凭证号（去掉首尾空白，最多 128 字）。
func checkSettlement(settlement, reference string) (string, string, error) {
	switch settlement {
	case "":
		return SettlementGrant, "", nil
	case SettlementGrant, SettlementPending:
		return settlement, "", nil
	case SettlementOffline:
		ref := strings.TrimSpace(reference)
		if ref == "" || utf8.RuneCountInString(ref) > referenceMaxRunes || hasNUL(ref) {
			return "", "", errs.Invalid(map[string]string{
				"reference": "请填写线下凭证号（银行流水号、收据编号等），最多 128 字"})
		}
		return settlement, ref, nil
	case "balance":
		return "", "", errs.Invalid(map[string]string{
			"settlement": "暂不支持从余额扣除；需要时请先调账，再用赠送开单"})
	default:
		return "", "", errs.Invalid(map[string]string{"settlement": "结算方式只能是 grant、pending 或 offline"})
	}
}

// requireUser 确认用户存在；不存在回 404。
func (s *Service) requireUser(ctx context.Context, userID uuid.UUID) error {
	emails, err := s.users.Emails(ctx, []uuid.UUID{userID})
	if err != nil {
		return err
	}
	if _, ok := emails[userID]; !ok {
		return ErrNotFound
	}
	return nil
}

// previewPrice 是预览用的价格档：给了且属于这个套餐就用它（ok 为真，报应付）；
// 周期取它，没给取套餐最早建的在售价格，都没有按一个月（对照旧 offerPeriod）。
func previewPrice(plan catalogsvc.PlanDetail, priceID *uuid.UUID) (catalogsvc.Price, string, int, bool) {
	if priceID != nil {
		for _, pr := range plan.Prices {
			if pr.ID == *priceID {
				return pr, pr.BillingInterval, pr.IntervalCount, true
			}
		}
		return catalogsvc.Price{}, "month", 1, false
	}
	var first *catalogsvc.Price
	for i := range plan.Prices {
		pr := &plan.Prices[i]
		if pr.Status != catalogsvc.PriceActive {
			continue
		}
		if first == nil || pr.CreatedAt.Before(first.CreatedAt) {
			first = pr
		}
	}
	if first == nil {
		return catalogsvc.Price{}, "month", 1, false
	}
	return catalogsvc.Price{}, first.BillingInterval, first.IntervalCount, false
}

// ManualPreview 后台开单前看「这单落到哪一份」：规则同 planPlacements，从订阅行进来时预选那一份。只读。
// change 选项按每份订阅读一次折算基数（候选最多 maxCandidates 份，后台低频，与旧代码同）。
func (s *Service) ManualPreview(ctx context.Context, in ManualPreviewInput) (ManualPreview, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return ManualPreview{}, err
	}
	if err := s.requireUser(ctx, in.UserID); err != nil {
		return ManualPreview{}, err
	}
	plan, err := s.catalog.GetPlan(ctx, in.PlanID)
	if err != nil {
		return ManualPreview{}, err
	}
	price, interval, count, priced := previewPrice(plan, in.PriceID)
	out := ManualPreview{Options: []Placement{}, MinPayment: decimal.Zero}
	if priced {
		if out.MinPayment, err = s.payments.MinPayment(ctx, price.Currency); err != nil {
			return ManualPreview{}, err
		}
	}
	us, err := s.subs.ByUser(ctx, in.UserID)
	if err != nil {
		return ManualPreview{}, err
	}
	cands := candidatesFrom(us)
	byID := make(map[uuid.UUID]candidate, len(cands))
	for _, c := range cands {
		byID[c.Sub.ID] = c
	}
	opts, def := planPlacements(in.PlanID, cands, in.EntrySubscriptionID)
	out.DefaultKey = def
	now := s.clock()
	for _, op := range opts {
		end := newPeriodEnd(op, now, interval, count)
		op.NewPeriodEnd = &end
		op.Credit, op.Due = decimal.Zero, decimal.Zero
		if op.Kind == PlacementChange && op.SubscriptionID != nil {
			basis, err := s.loadBasis(ctx, byID[*op.SubscriptionID].Sub)
			switch {
			case errors.Is(err, errProrationMixedCurrency):
				// 本期付费单币种不一致：展示时不报价值，落地时会给出原因
			case err != nil:
				return ManualPreview{}, err
			default:
				op.Credit, _ = prorationCredit(basis, now)
				op.Currency = basis.Currency
				op.GiftDaysLost = giftedDaysLeft(basis, now)
			}
		}
		if priced {
			credit := decimal.Zero
			if op.Kind == PlacementChange && (op.Currency == "" || op.Currency == price.Currency) {
				credit = op.Credit
			}
			op.Due = OrderTotal(price.UnitAmount, decimal.Zero, credit)
			op.BelowMinimum = manualDueBelowMinimum(op.Due, out.MinPayment)
		}
		out.Options = append(out.Options, op)
	}
	return out, nil
}

// CreateManualOrder 后台人工开单（POST /v1/orders/manual）。落点在事务里按当时的订阅重新算，选的那一项不在了回 422。
func (s *Service) CreateManualOrder(ctx context.Context, in ManualOrderInput) (Created, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return Created{}, err
	}
	reason, err := checkManualReason(in.Reason)
	if err != nil {
		return Created{}, err
	}
	settlement, reference, err := checkSettlement(in.Settlement, in.Reference)
	if err != nil {
		return Created{}, err
	}
	md := &manualDraft{
		Grant:     settlement == SettlementGrant,
		Offline:   settlement == SettlementOffline,
		Reason:    reason,
		CreatedBy: p.UserID,
	}
	actor := actorFrom(p)

	var created Created
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.requireUser(ctx, in.UserID); err != nil {
			return err
		}
		us, err := s.subs.ByUser(ctx, in.UserID)
		if err != nil {
			return err
		}
		opts, _ := planPlacements(in.PlanID, candidatesFrom(us), nil)
		op, err := resolvePlacement(in.Target, opts)
		if err != nil {
			return err
		}
		var o model.Order
		created, o, err = s.placeManual(ctx, in, op, md, actor)
		if err != nil {
			return err
		}
		var rec *paymentsvc.RecordResult
		if md.Offline {
			r, err := s.settleManualOffline(ctx, o, op, reference, reason)
			if err != nil {
				return err
			}
			rec = &r
			after, err := s.reload(ctx, o.ID)
			if err != nil {
				return err
			}
			created.Status = after.Status
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionManualCreated,
			ResourceType: ResourceOrder,
			ResourceID:   &o.ID,
			Data:         manualDigest(in, op, settlement, reason, reference, o, created, rec),
		}); err != nil {
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

// placeManual 按选中的落点建单。renew、change 先锁那份订阅，锁后再核一次它的套餐（预览之后可能已被换掉）。
func (s *Service) placeManual(ctx context.Context, in ManualOrderInput, op Placement, md *manualDraft, actor Actor) (Created, model.Order, error) {
	if op.Kind == PlacementNew {
		return s.placeManualNew(ctx, in, md, actor)
	}
	uid := in.UserID
	sub, err := s.subs.Lock(ctx, *op.SubscriptionID, &uid)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	samePlan := sub.PlanID == in.PlanID
	if samePlan != (op.Kind == PlacementRenew) {
		return Created{}, model.Order{}, errChoiceStale
	}
	if op.Kind == PlacementRenew {
		priceID := in.PriceID
		return s.placeRenewal(ctx, renewalDraft{
			Sub:     sub,
			PriceID: &priceID,
			UserID:  in.UserID,
			Manual:  md,
			Actor:   actor,
		})
	}
	return s.placeChange(ctx, changeDraft{
		Sub:     sub,
		PlanID:  in.PlanID,
		PriceID: in.PriceID,
		UserID:  in.UserID,
		AsOf:    s.clock(),
		Manual:  md,
		Actor:   actor,
	})
}

// placeManualNew 另开一份：锁套餐行（与门户新购同序），限购与库存照常，不看可见性与上架窗口。
func (s *Service) placeManualNew(ctx context.Context, in ManualOrderInput, md *manualDraft, actor Actor) (Created, model.Order, error) {
	uid := in.UserID
	planID := in.PlanID
	offer, err := s.catalog.Offer(ctx, in.PriceID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseNew,
		UserID: &uid,
		PlanID: &planID,
		Manual: true,
		Lock:   true,
		Now:    s.clock(),
	})
	if err != nil {
		return Created{}, model.Order{}, err
	}
	limit := offer.Plan.PurchaseLimitPerUser
	if err := s.checkPurchaseLimit(ctx, planID, uid, limit); err != nil {
		return Created{}, model.Order{}, err
	}
	item, err := newOrderItemFromOffer(offer, offer.Price)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	return s.place(ctx, draft{
		Kind:         KindNew,
		UserID:       uid,
		Currency:     offer.Price.Currency,
		Subtotal:     offer.Price.UnitAmount,
		CouponPlanID: &planID,
		Item:         item,
		NewPurchase:  &newPurchase{PlanID: planID, Limited: limit != nil},
		Manual:       md,
		Actor:        actor,
	})
}

// settleManualOffline 在建单事务里按线下渠道结清人工单（与标记已支付同一条入账路径）。
// 应付为 0 的单不需要收款，回 409 让管理员改用赠送；没有入账就整笔回滚。
func (s *Service) settleManualOffline(ctx context.Context, o model.Order, op Placement, reference, reason string) (paymentsvc.RecordResult, error) {
	if !o.PayableAmount.IsPositive() {
		if op.Kind == PlacementChange {
			return paymentsvc.RecordResult{}, errOfflineChangeNothingDue
		}
		return paymentsvc.RecordResult{}, errOfflineNothingDue
	}
	rec, err := s.payments.RecordOffline(ctx, paymentsvc.OfflineInput{
		OrderID:   o.ID,
		OrderNo:   o.OrderNo,
		UserID:    o.UserID,
		Amount:    o.PayableAmount,
		Currency:  money.Currency(o.Currency),
		Reference: reference,
		Reason:    reason,
	})
	if err != nil {
		return paymentsvc.RecordResult{}, err
	}
	if rec.QuarantineKind != "" {
		return paymentsvc.RecordResult{}, errs.Newf(errs.Conflict, "线下收款没有结清这张单，已整笔撤销，请刷新后重试")
	}
	if !rec.Processed || rec.AlreadyHandled || rec.PaymentID == uuid.Nil {
		return paymentsvc.RecordResult{}, errors.New("order: 人工单线下收款没有入账")
	}
	return rec, nil
}

// manualDigest 是 order.manual_created 的审计内容（字段对照旧 manual_order 与 manualPlanChangeAudit）。
func manualDigest(in ManualOrderInput, op Placement, settlement, reason, reference string,
	o model.Order, c Created, rec *paymentsvc.RecordResult) map[string]any {
	d := map[string]any{
		"order_no":   o.OrderNo,
		"user_id":    in.UserID.String(),
		"plan_id":    in.PlanID.String(),
		"price_id":   in.PriceID.String(),
		"placement":  op.Kind,
		"reason":     reason,
		"status":     c.Status,
		"settlement": settlement,
		"currency":   o.Currency,
		"subtotal":   o.SubtotalAmount.StringFixed(2),
		"granted":    o.DiscountAmount.StringFixed(2),
		"total":      o.TotalAmount.StringFixed(2),
	}
	if op.SubscriptionID != nil {
		d["subscription_id"] = op.SubscriptionID.String()
	}
	if c.Change != nil {
		d["proration_credit"] = c.Change.ProrationCredit.StringFixed(2)
		d["balance_refund"] = c.Change.BalanceRefund.StringFixed(2)
	}
	if rec != nil {
		d["reference"] = reference
		d["amount"] = o.PayableAmount.StringFixed(2)
		d["payment_id"] = rec.PaymentID.String()
	}
	return d
}
