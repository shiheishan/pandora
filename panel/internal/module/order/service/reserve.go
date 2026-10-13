package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// placeOrder 插订单行、预留行（state held，expires_at = o.ExpiresAt，held_at = 现在）与一条 reserve 流水（from nil → held）。
// o.ID 为 uuid.Nil 时由库生成并回填。
//
// 唯一冲突怎么区分：db 打开了 GORM TranslateError，错误里拿不到约束名，所以按订单种类判断。orders 上只有两个唯一约束：
// 订单号（随机段碰撞，概率极低）与 orders_one_open_subscription_change（只约束 renewal、upgrade）。
// 续费、换套餐撞了按「已有未完成的续费或变更单」回 ErrOpenChange（订单号碰撞也会落到这里，概率可以忽略）；
// 其余种类只可能是订单号碰撞，回内部错误，用户重试即可（事务已作废，不能在同一事务里换号重插）。
func (s *Service) placeOrder(ctx context.Context, o *model.Order, actor Actor) (model.Reservation, error) {
	if err := s.repo.CreateOrder(ctx, o); err != nil {
		if repo.IsDuplicate(err) {
			if o.Kind == KindRenewal || o.Kind == KindUpgrade {
				return model.Reservation{}, ErrOpenChange
			}
			return model.Reservation{}, errs.Wrap(errs.Internal, fmt.Errorf("order: 订单号碰撞 %s: %w", o.OrderNo, err))
		}
		return model.Reservation{}, err
	}
	now := s.clock()
	res := model.Reservation{
		OrderID:   o.ID,
		UserID:    o.UserID,
		State:     ReservationHeld,
		ExpiresAt: o.ExpiresAt,
		HeldAt:    now,
	}
	if err := s.repo.CreateReservation(ctx, &res); err != nil {
		return model.Reservation{}, err
	}
	ev := model.ReservationEvent{
		ReservationID: res.ID,
		OrderID:       o.ID,
		FromState:     nil,
		ToState:       ReservationHeld,
		EventKind:     EventReserve,
		ActorKind:     actor.Kind,
		ActorID:       actor.ID,
		OccurredAt:    now,
	}
	if err := s.repo.InsertReservationEvent(ctx, &ev); err != nil {
		return model.Reservation{}, err
	}
	return res, nil
}

// checkPurchaseLimit：limit 为 nil 什么都不做；否则 repo.EnsureCounter，再 repo.CounterForUpdate，
// purchased + reserved >= *limit 回 ErrPurchaseLimit。
func (s *Service) checkPurchaseLimit(ctx context.Context, planID, userID uuid.UUID, limit *int) error {
	if limit == nil {
		return nil
	}
	if err := s.repo.EnsureCounter(ctx, planID, userID); err != nil {
		return err
	}
	c, ok, err := s.repo.CounterForUpdate(ctx, planID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("order: 限购计数行缺失")
	}
	if c.Purchased+c.Reserved >= *limit {
		return ErrPurchaseLimit
	}
	return nil
}

// holdNewPurchase 新购的库存与限购占用（数量 1）：catalog.ReserveStock(planID, 1) 返回 true 时插一条库存明细；
// limited 为真时 repo.AdjustCounter(plan, user, 0, +1)（返回 false 当内部错误）并插一条限购明细。
func (s *Service) holdNewPurchase(ctx context.Context, res model.Reservation, planID, userID uuid.UUID, limited bool) error {
	ok, err := s.catalog.ReserveStock(ctx, planID, 1)
	if err != nil {
		return err
	}
	if ok {
		sr := model.StockReservation{
			ReservationID: res.ID,
			OrderID:       res.OrderID,
			PlanID:        planID,
			Quantity:      1,
		}
		if err := s.repo.CreateStockReservation(ctx, &sr); err != nil {
			return err
		}
	}
	if !limited {
		return nil
	}
	changed, err := s.repo.AdjustCounter(ctx, planID, userID, 0, 1)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("order: 限购预留计数更新失败")
	}
	lr := model.LimitReservation{
		ReservationID: res.ID,
		OrderID:       res.OrderID,
		PlanID:        planID,
		UserID:        userID,
		Quantity:      1,
	}
	return s.repo.CreateLimitReservation(ctx, &lr)
}

// captureReservation 确认占用：锁预留行（repo.ReservationByOrder ForNoKeyUpdate），UpdateReservation held→captured（false 回 ErrStateChanged），
// 写 capture 流水；然后库存明细逐条 catalog.CaptureStock、限购明细逐条 AdjustCounter(+q, −q)（false 当内部错误）、
// 订单有券（o.CouponID != nil）时 promo.Capture(o.ID)。余额不在这里处理（由调用方按情况 PostOrderPaid / CaptureFromBalance）。
func (s *Service) captureReservation(ctx context.Context, o model.Order, actor Actor) error {
	res, ok, err := s.repo.ReservationByOrder(ctx, o.ID, repo.ForNoKeyUpdate)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("order: 订单预留不存在")
	}
	now := s.clock()
	updated, err := s.repo.UpdateReservation(ctx, res.ID, ReservationHeld, ReservationCaptured, now, nil)
	if err != nil {
		return err
	}
	if !updated {
		return ErrStateChanged
	}
	ev := model.ReservationEvent{
		ReservationID: res.ID,
		OrderID:       o.ID,
		FromState:     strPtr(ReservationHeld),
		ToState:       ReservationCaptured,
		EventKind:     EventCapture,
		ActorKind:     actor.Kind,
		ActorID:       actor.ID,
		OccurredAt:    now,
	}
	if err := s.repo.InsertReservationEvent(ctx, &ev); err != nil {
		return err
	}

	stocks, err := s.repo.StockReservations(ctx, res.ID)
	if err != nil {
		return err
	}
	for _, st := range stocks {
		if err := s.catalog.CaptureStock(ctx, st.PlanID, st.Quantity); err != nil {
			return err
		}
	}
	limits, err := s.repo.LimitReservations(ctx, res.ID)
	if err != nil {
		return err
	}
	for _, lim := range limits {
		changed, err := s.repo.AdjustCounter(ctx, lim.PlanID, lim.UserID, lim.Quantity, -lim.Quantity)
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("order: 限购确认计数更新失败")
		}
	}
	if o.CouponID != nil {
		if err := s.promo.Capture(ctx, o.ID); err != nil {
			return err
		}
	}
	return nil
}

// releaseOrder 取消或超时释放。调用方已锁住订单行且 o.Status == StatusPending（不满足回 ErrStateChanged）。
// to 是 StatusCancelled 或 StatusExpired，event 是 EventCancel 或 EventExpire，reason 写进预留 release_reason 与订单 cancel_reason。
func (s *Service) releaseOrder(ctx context.Context, o model.Order, to, reason, event string, actor Actor) (model.Order, error) {
	if o.Status != StatusPending {
		return model.Order{}, ErrStateChanged
	}

	res, ok, err := s.repo.ReservationByOrder(ctx, o.ID, repo.ForNoKeyUpdate)
	if err != nil {
		return model.Order{}, err
	}
	if !ok {
		return model.Order{}, fmt.Errorf("order: 订单预留不存在")
	}
	if res.State != ReservationHeld {
		return model.Order{}, ErrStateChanged
	}

	now := s.clock()
	reasonPtr := &reason
	updated, err := s.repo.UpdateReservation(ctx, res.ID, ReservationHeld, ReservationReleased, now, reasonPtr)
	if err != nil {
		return model.Order{}, err
	}
	if !updated {
		return model.Order{}, ErrStateChanged
	}
	ev := model.ReservationEvent{
		ReservationID: res.ID,
		OrderID:       o.ID,
		FromState:     strPtr(ReservationHeld),
		ToState:       ReservationReleased,
		EventKind:     event,
		ActorKind:     actor.Kind,
		ActorID:       actor.ID,
		Reason:        reasonPtr,
		OccurredAt:    now,
	}
	if err := s.repo.InsertReservationEvent(ctx, &ev); err != nil {
		return model.Order{}, err
	}

	stocks, err := s.repo.StockReservations(ctx, res.ID)
	if err != nil {
		return model.Order{}, err
	}
	for _, st := range stocks {
		if err := s.catalog.ReleaseStock(ctx, st.PlanID, st.Quantity); err != nil {
			return model.Order{}, err
		}
	}
	limits, err := s.repo.LimitReservations(ctx, res.ID)
	if err != nil {
		return model.Order{}, err
	}
	for _, lim := range limits {
		changed, err := s.repo.AdjustCounter(ctx, lim.PlanID, lim.UserID, 0, -lim.Quantity)
		if err != nil {
			return model.Order{}, err
		}
		if !changed {
			return model.Order{}, fmt.Errorf("order: 限购释放计数更新失败")
		}
	}
	if o.CouponID != nil {
		if err := s.promo.Release(ctx, o.ID, reason); err != nil {
			return model.Order{}, err
		}
	}
	if o.BalanceApplied.IsPositive() {
		memo := reason
		released, err := s.ledger.ReleaseOrderHold(ctx, o.ID, memo)
		if err != nil {
			return model.Order{}, err
		}
		// 订单记了余额抵扣，冻结却不在 held：账与单对不上，不静默放过（与 promo.Release 回 ErrNotHeld 同样严格）
		if !released {
			return model.Order{}, fmt.Errorf("order: 订单 %s 有余额抵扣但没有可释放的冻结", o.ID)
		}
	}

	intentTo := paymentsvc.IntentCancelled
	if to == StatusExpired {
		intentTo = paymentsvc.IntentExpired
	}
	if _, err := s.payments.CloseOrderIntents(ctx, o.ID, intentTo); err != nil {
		return model.Order{}, err
	}

	// 超时只写 status 与 expired_at，不写 cancel_reason（旧 release.go 同口径；后台只要 cancel_reason 非空就显示「取消原因」）。
	// 超时原因只记在预留的 release_reason、预留流水与审计里。
	cols := map[string]any{"status": to}
	if to == StatusCancelled {
		cols["cancelled_at"] = now
		cols["cancel_reason"] = reason
	} else {
		cols["expired_at"] = now
	}
	okOrder, err := s.repo.UpdateOrder(ctx, o.ID, StatusPending, cols)
	if err != nil {
		return model.Order{}, err
	}
	if !okOrder {
		return model.Order{}, ErrStateChanged
	}

	action := ActionOrderCancelled
	if to == StatusExpired {
		action = ActionOrderExpired
	}
	data := map[string]any{
		"order_no": o.OrderNo,
		"reason":   reason,
	}
	if err := s.audit.Write(ctx, auditsvc.Event{
		Action:       action,
		ResourceType: ResourceOrder,
		ResourceID:   &o.ID,
		Data:         data,
	}); err != nil {
		return model.Order{}, err
	}

	if err := s.emitOrderCancelled(ctx, o, to, reason); err != nil {
		return model.Order{}, err
	}

	out, found, err := s.repo.OrderByID(ctx, o.ID, repo.NoLock)
	if err != nil {
		return model.Order{}, err
	}
	if !found {
		return model.Order{}, errors.New("order: 释放后订单不存在")
	}
	return out, nil
}

func strPtr(s string) *string { return &s }
