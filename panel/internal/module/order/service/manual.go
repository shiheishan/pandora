package service

// 后台人工开单：
//
//	落点   由管理员选（预览给出的选项之一）：renew 在那一份上开续费单，change 在那一份上开换套餐单（剩余价值与门户同口径，
//	       赠送单新价算 0 元、剩余价值全额退进余额），new 另开一份
//	结算   grant 赠送（缺省，全额减免当场履约）、pending 待用户支付（低于最低额回 422）、
//	       offline 线下已收款（建单后在同一事务里经 payment.RecordOffline 结清）
//	审计   order.manual_created，与订单同一事务：没有审计的人工单等于没人负责
//
// 人工单不用券、不用余额；不受门户「同套餐只续不新开」与「同套餐只能有一张待付新购单」限制，限购与库存照常。
// 另开一份照门户的可见性校验（隐藏、不在上架窗口、别组的组价回 404）；
// 续一期与换套餐不看可见性（续的是自己的套餐；换套餐只看目标套餐允许变更）。

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

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
			r, err := s.settleManualOffline(ctx, o, reference, reason)
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

// placeManualNew 另开一份：锁套餐行（与门户新购同序），限购与库存照常；可见性、上架窗口与用户组价按这个用户看
// （Manual: false，与门户新购同一道目录校验）。
func (s *Service) placeManualNew(ctx context.Context, in ManualOrderInput, md *manualDraft, actor Actor) (Created, model.Order, error) {
	uid := in.UserID
	planID := in.PlanID
	offer, err := s.catalog.Offer(ctx, in.PriceID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseNew,
		UserID: &uid,
		PlanID: &planID,
		Manual: false,
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

// settleManualOffline 在建单事务里按线下渠道结清人工单（与标记已支付同一条入账路径）。应付为 0 的单 place 已在建单前拒掉；
// 没有入账就整笔回滚。
func (s *Service) settleManualOffline(ctx context.Context, o model.Order, reference, reason string) (paymentsvc.RecordResult, error) {
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

// manualDigest 是 order.manual_created 的审计内容。
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
		d["direction"] = changeAuditData(o, o.TotalAmount)["direction"]
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
