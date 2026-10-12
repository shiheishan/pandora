package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// markPaid 把待付订单转 paid：paid_amount = 订单总额（含余额抵扣部分），paid_at = at。返回更新后的订单。
func (s *Service) markPaid(ctx context.Context, o model.Order, at time.Time) (model.Order, error) {
	ok, err := s.repo.UpdateOrder(ctx, o.ID, StatusPending, map[string]any{
		"status":      StatusPaid,
		"paid_amount": o.TotalAmount,
		"paid_at":     at,
	})
	if err != nil {
		return model.Order{}, err
	}
	if !ok {
		return model.Order{}, ErrStateChanged
	}
	return s.reload(ctx, o.ID)
}

// fulfil 按种类履约，再 paid→fulfilled（fulfilled_at = at）。o 必须是 paid。返回更新后的订单。
//
//	topup：无事可做（钱在结算记账时已进余额）
//	new：开通新订阅（source_order_id 指向本单），同一更新把 subscription_id 写成新订阅
//	renewal：在原订阅上续一期（版本沿用订阅自己的）
//	addon：给目标订阅发一笔流量包余额
//
// 订阅上的价格快照取订单行（建单时的价格档单价），与旧 provision.go 取 prices.unit_amount 同口径。
func (s *Service) fulfil(ctx context.Context, o model.Order, at time.Time, actor subsvc.Actor) (model.Order, error) {
	if o.Status != StatusPaid {
		return model.Order{}, ErrStateChanged
	}
	cols := map[string]any{
		"status":       StatusFulfilled,
		"fulfilled_at": at,
	}
	switch o.Kind {
	case KindTopup:
	case KindNew:
		it, err := s.onlyItem(ctx, o)
		if err != nil {
			return model.Order{}, err
		}
		if it.PlanID == nil || it.PlanVersionID == nil {
			return model.Order{}, fmt.Errorf("order: 新购订单行缺套餐或版本 %s", o.ID)
		}
		price, err := itemPrice(o, it)
		if err != nil {
			return model.Order{}, err
		}
		orderID := o.ID
		label := ""
		if o.SubscriptionLabel != nil {
			label = *o.SubscriptionLabel
		}
		sub, err := s.subs.Provision(ctx, subsvc.Provision{
			UserID:        o.UserID,
			PlanID:        *it.PlanID,
			PlanVersionID: *it.PlanVersionID,
			Price:         price,
			SourceOrderID: &orderID,
			Label:         label,
			Actor:         actor,
		})
		if err != nil {
			return model.Order{}, err
		}
		cols["subscription_id"] = sub.ID
	case KindRenewal:
		if o.SubscriptionID == nil {
			return model.Order{}, fmt.Errorf("order: 续费订单缺订阅 %s", o.ID)
		}
		it, err := s.onlyItem(ctx, o)
		if err != nil {
			return model.Order{}, err
		}
		price, err := itemPrice(o, it)
		if err != nil {
			return model.Order{}, err
		}
		orderID := o.ID
		createdAt := o.CreatedAt
		if _, err := s.subs.Renew(ctx, subsvc.Renewal{
			SubscriptionID: *o.SubscriptionID,
			UserID:         o.UserID,
			Price:          price,
			PaidAt:         at,
			OrderCreatedAt: &createdAt,
			OrderID:        &orderID,
			Actor:          actor,
			Source:         "order",
		}); err != nil {
			return model.Order{}, err
		}
	case KindAddon:
		it, err := s.onlyItem(ctx, o)
		if err != nil {
			return model.Order{}, err
		}
		if it.SnapshotTrafficBytes == nil {
			return model.Order{}, fmt.Errorf("order: 流量包订单行缺流量 %s", o.ID)
		}
		if _, err := s.subs.GrantTrafficPack(ctx, subsvc.PackGrant{
			UserID:         o.UserID,
			SubscriptionID: o.SubscriptionID,
			Source:         subsvc.PackSourceOrder,
			SourceID:       o.ID,
			Bytes:          *it.SnapshotTrafficBytes,
		}); err != nil {
			return model.Order{}, err
		}
	default:
		return model.Order{}, fmt.Errorf("order: 不支持履约的订单种类 %q", o.Kind)
	}
	ok, err := s.repo.UpdateOrder(ctx, o.ID, StatusPaid, cols)
	if err != nil {
		return model.Order{}, err
	}
	if !ok {
		return model.Order{}, ErrStateChanged
	}
	return s.reload(ctx, o.ID)
}

// settleZeroPay 建单事务里应付为 0 的单当场履约：有余额抵扣时 CaptureFromBalance 一步冻结并扣款（不先 HoldForOrder），
// 确认预留，转 paid，再履约。不调 PaidHook（旧代码零应付单不计佣）。
func (s *Service) settleZeroPay(ctx context.Context, o model.Order, actor Actor) (model.Order, error) {
	if o.BalanceApplied.IsPositive() {
		if _, _, err := s.ledger.CaptureFromBalance(ctx, ledgersvc.HoldInput{
			OrderID:  o.ID,
			UserID:   o.UserID,
			Currency: money.Currency(o.Currency),
			Amount:   o.BalanceApplied,
		}); err != nil {
			return model.Order{}, err
		}
	}
	if err := s.captureReservation(ctx, o, actor); err != nil {
		return model.Order{}, err
	}
	now := s.clock()
	paid, err := s.markPaid(ctx, o, now)
	if err != nil {
		return model.Order{}, err
	}
	return s.fulfil(ctx, paid, now, subActor(actor))
}

// subActor 把 order 的操作者映射成 subscription 的操作者（user、admin 带 ID，payment、system 不带）。
func subActor(a Actor) subsvc.Actor {
	switch a.Kind {
	case ActorUser:
		return subsvc.Actor{Kind: subsvc.ActorUser, ID: a.ID}
	case ActorAdmin:
		return subsvc.Actor{Kind: subsvc.ActorAdmin, ID: a.ID}
	case ActorPayment:
		return subsvc.Actor{Kind: subsvc.ActorPayment}
	default:
		return subsvc.Actor{Kind: subsvc.ActorSystem}
	}
}

// onlyItem 取订单唯一的一条订单行（新购、续费、流量包都只有一条）。
func (s *Service) onlyItem(ctx context.Context, o model.Order) (model.Item, error) {
	items, err := s.repo.ItemsByOrders(ctx, []uuid.UUID{o.ID})
	if err != nil {
		return model.Item{}, err
	}
	if len(items) != 1 {
		return model.Item{}, fmt.Errorf("order: 订单 %s 应有 1 条订单行，实际 %d", o.ID, len(items))
	}
	return items[0], nil
}

// itemPrice 把套餐订单行快照映射成订阅的价格快照。
func itemPrice(o model.Order, it model.Item) (subsvc.PriceSnapshot, error) {
	if it.SnapshotInterval == nil || it.SnapshotIntervalCount == nil {
		return subsvc.PriceSnapshot{}, fmt.Errorf("order: 订单行缺周期快照 %s", o.ID)
	}
	return subsvc.PriceSnapshot{
		PriceID:       it.PriceID,
		Currency:      money.Currency(o.Currency),
		Amount:        it.UnitAmount,
		Interval:      *it.SnapshotInterval,
		IntervalCount: *it.SnapshotIntervalCount,
	}, nil
}

// reload 在事务里重读一次订单（状态更新后返回最新行）。
func (s *Service) reload(ctx context.Context, id uuid.UUID) (model.Order, error) {
	o, ok, err := s.repo.OrderByID(ctx, id, repo.NoLock)
	if err != nil {
		return model.Order{}, err
	}
	if !ok {
		return model.Order{}, errors.New("order: 订单不存在")
	}
	return o, nil
}
