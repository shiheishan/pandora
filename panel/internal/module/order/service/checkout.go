package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// newPurchase 是新购要占的库存与限购。
type newPurchase struct {
	PlanID  uuid.UUID
	Limited bool // 套餐有每人限购
}

// draft 是一张待建订单（在建单事务里组装好再交给 place）。
type draft struct {
	Kind           string
	UserID         uuid.UUID
	Currency       money.Currency
	Subtotal       decimal.Decimal
	CouponPlanID   *uuid.UUID // 券适用范围：套餐类订单给套餐 id，流量包为 nil；充值不用券
	SubscriptionID *uuid.UUID
	Label          *string
	Item           *model.Item // 充值为 nil；OrderID、Currency 由 place 填
	NewPurchase    *newPurchase
	Checkout       Checkout
	Actor          Actor
	AuditAction    string
	AuditData      map[string]any
}

// place 在调用方事务里落单，返回结果与落库后的订单行。
func (s *Service) place(ctx context.Context, d draft) (Created, model.Order, error) {
	now := s.clock()

	var discount decimal.Decimal
	var couponID *uuid.UUID
	var couponCode string
	if d.Kind != KindTopup {
		couponCode = promosvc.NormalizeCode(d.Checkout.CouponCode)
		if couponCode != "" {
			applied, err := s.promo.Evaluate(ctx, couponCode, promosvc.Target{
				UserID:   d.UserID,
				PlanID:   d.CouponPlanID,
				Currency: string(d.Currency),
				Subtotal: d.Subtotal,
			})
			if err != nil {
				return Created{}, model.Order{}, err
			}
			if applied != nil {
				discount = applied.Discount
				couponID = &applied.CouponID
			}
		}
	}

	total := OrderTotal(d.Subtotal, discount, decimal.Zero)

	var bal BalancePlan
	if d.Kind == KindTopup {
		bal = BalancePlan{Applied: decimal.Zero, Payable: total}
	} else if total.IsPositive() {
		minPay, err := s.payments.MinPayment(ctx, d.Currency)
		if err != nil {
			return Created{}, model.Order{}, err
		}
		requested := d.Checkout.UseBalance
		available := decimal.Zero
		if requested.IsPositive() || total.LessThan(minPay) {
			available, err = s.ledger.Balance(ctx, d.UserID, d.Currency)
			if err != nil {
				return Created{}, model.Order{}, err
			}
		}
		bal = PlanBalance(total, available, requested, minPay)
		if bal.BelowMinimum {
			return Created{}, model.Order{}, ErrBelowMinimum
		}
	} else {
		bal = BalancePlan{Applied: decimal.Zero, Payable: decimal.Zero}
	}

	if err := checkExpect(d.Checkout.Expect, total, bal, now); err != nil {
		return Created{}, model.Order{}, err
	}

	orderNo, err := newOrderNo(now)
	if err != nil {
		return Created{}, model.Order{}, err
	}

	o := model.Order{
		OrderNo:               orderNo,
		UserID:                d.UserID,
		Kind:                  d.Kind,
		Status:                StatusPending,
		Currency:              string(d.Currency),
		SubtotalAmount:        d.Subtotal,
		DiscountAmount:        discount,
		ProrationCreditAmount: decimal.Zero,
		WaivedAmount:          decimal.Zero,
		TotalAmount:           total,
		BalanceApplied:        bal.Applied,
		PayableAmount:         bal.Payable,
		PaidAmount:            decimal.Zero,
		CouponID:              couponID,
		SubscriptionID:        d.SubscriptionID,
		SubscriptionLabel:     d.Label,
		StateVersion:          1,
		ExpiresAt:             now.Add(PaymentWindow),
	}

	res, err := s.placeOrder(ctx, &o, d.Actor)
	if err != nil {
		return Created{}, model.Order{}, err
	}

	if couponCode != "" {
		reserved, err := s.promo.Reserve(ctx, couponCode, promosvc.Target{
			UserID:   d.UserID,
			PlanID:   d.CouponPlanID,
			Currency: string(d.Currency),
			Subtotal: d.Subtotal,
		}, promosvc.OrderRef{ID: o.ID, No: o.OrderNo})
		if err != nil {
			return Created{}, model.Order{}, err
		}
		if reserved == nil {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
		if couponID != nil && reserved.CouponID != *couponID {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
		if !reserved.Discount.Equal(discount) {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
		couponID = &reserved.CouponID
		discount = reserved.Discount
		o.CouponID = couponID
		o.DiscountAmount = discount
		total = OrderTotal(d.Subtotal, discount, decimal.Zero)
		if !total.Equal(o.TotalAmount) {
			return Created{}, model.Order{}, ErrQuoteChanged
		}
	}

	if d.Item != nil {
		it := *d.Item
		it.OrderID = o.ID
		it.Currency = string(d.Currency)
		if err := s.repo.CreateItem(ctx, &it); err != nil {
			return Created{}, model.Order{}, err
		}
	}

	if d.NewPurchase != nil {
		if err := s.holdNewPurchase(ctx, res, d.NewPurchase.PlanID, d.UserID, d.NewPurchase.Limited); err != nil {
			return Created{}, model.Order{}, err
		}
	}

	if bal.Payable.IsPositive() && bal.Applied.IsPositive() {
		_, err := s.ledger.HoldForOrder(ctx, ledgersvc.HoldInput{
			OrderID:  o.ID,
			UserID:   d.UserID,
			Currency: d.Currency,
			Amount:   bal.Applied,
		})
		if err != nil {
			return Created{}, model.Order{}, err
		}
	}

	if bal.Payable.IsZero() {
		settled, err := s.settleZeroPay(ctx, o, d.Actor)
		if err != nil {
			return Created{}, model.Order{}, err
		}
		o = settled
	}

	auditData := map[string]any{
		"order_no": o.OrderNo,
		"kind":     d.Kind,
		"currency": string(d.Currency),
		"total":    total.StringFixed(2),
	}
	for k, v := range d.AuditData {
		auditData[k] = v
	}
	if err := s.audit.Write(ctx, auditsvc.Event{
		Action:       d.AuditAction,
		ResourceType: ResourceOrder,
		ResourceID:   &o.ID,
		Data:         auditData,
	}); err != nil {
		return Created{}, model.Order{}, err
	}

	created := Created{
		OrderID:        o.ID,
		OrderNo:        o.OrderNo,
		Currency:       d.Currency,
		Total:          o.TotalAmount,
		Discount:       o.DiscountAmount,
		BalanceApplied: o.BalanceApplied,
		Payable:        o.PayableAmount,
		Status:         o.Status,
	}
	return created, o, nil
}
