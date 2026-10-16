package service

import (
	"context"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// ActionStackCreated 是门户建叠加单的审计动作。
const ActionStackCreated = "subscription.stack_created"

// stackDraft 是一张叠加单的输入（门户与后台人工单共用）。调用方已锁住 Sub。
type stackDraft struct {
	Sub      subsvc.Subscription
	PriceID  *uuid.UUID // nil 取套餐唯一的在售价
	UserID   uuid.UUID
	Checkout Checkout
	Manual   *manualDraft
	Actor    Actor
	Audit    string // 为空不写审计（人工单自己写）
}

// placeStack 在调用方事务里建一张叠加单。
// 与续费同一取舍不锁套餐行；同一订阅同时只许一张未完结的续费、叠加或换套餐单（库里唯一索引兜底）。
func (s *Service) placeStack(ctx context.Context, d stackDraft) (Created, model.Order, error) {
	now := s.clock()
	if !d.Sub.StateAt(now).AcceptsChange() {
		return Created{}, model.Order{}, subsvc.ErrRenewalWindowClosed
	}
	open, err := s.repo.OpenChangeOrderExists(ctx, d.Sub.ID)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	if open {
		return Created{}, model.Order{}, ErrOpenChange
	}

	uid := d.UserID
	priceID := d.PriceID
	if priceID == nil {
		op, err := s.catalog.PlanOffers(ctx, d.Sub.PlanID, catalogsvc.OfferRequest{
			Use:    catalogsvc.UseStack,
			UserID: &uid,
			PlanID: &d.Sub.PlanID,
			Manual: d.Manual != nil,
			Now:    now,
		})
		if err != nil {
			return Created{}, model.Order{}, err
		}
		if len(op.Prices) == 0 {
			return Created{}, model.Order{}, catalogsvc.ErrChangeNotAllowed
		}
		priceID = &op.Prices[0].ID
	}

	offer, err := s.catalog.Offer(ctx, *priceID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseStack,
		UserID: &uid,
		PlanID: &d.Sub.PlanID,
		Manual: d.Manual != nil,
		Lock:   false,
		Now:    now,
	})
	if err != nil {
		return Created{}, model.Order{}, err
	}
	if offer.Plan.Kind != catalogsvc.KindOneTime {
		return Created{}, model.Order{}, subsvc.ErrStackNotOneTime
	}

	// 订单行使用当前版本，叠加后这一份切到它
	item, err := newOrderItemFromOffer(offer, offer.Price)
	if err != nil {
		return Created{}, model.Order{}, err
	}
	subID := d.Sub.ID
	return s.place(ctx, draft{
		Kind:           KindStack,
		UserID:         d.UserID,
		Subtotal:       offer.Price.UnitAmount,
		CouponPlanID:   &d.Sub.PlanID,
		SubscriptionID: &subID,
		Item:           item,
		Checkout:       d.Checkout,
		Manual:         d.Manual,
		Actor:          d.Actor,
		AuditAction:    d.Audit,
		AuditData: map[string]any{
			"subscription_id": subID.String(),
			"price_id":        (*priceID).String(),
		},
	})
}

// CreateStackOrder 门户叠加（POST /v1/me/subscriptions/{id}/stack）。
func (s *Service) CreateStackOrder(ctx context.Context, in StackOrderInput) (Created, error) {
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
		created, _, err = s.placeStack(ctx, stackDraft{
			Sub:      sub,
			PriceID:  in.PriceID,
			UserID:   userID,
			Checkout: in.Checkout,
			Actor:    actor,
			Audit:    ActionStackCreated,
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
