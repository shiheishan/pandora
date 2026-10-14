package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/module/ledger/repo"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// HoldInput 是按订单冻结余额的入参。
type HoldInput struct {
	OrderID  uuid.UUID
	UserID   uuid.UUID
	Currency money.Currency
	Amount   decimal.Decimal
}

// Hold 是一条订单余额冻结。
type Hold struct {
	ID           uuid.UUID
	OrderID      uuid.UUID
	UserID       uuid.UUID
	Currency     money.Currency
	Amount       decimal.Decimal
	Status       string
	HoldTxnID    uuid.UUID
	CaptureTxnID *uuid.UUID
	ReleaseTxnID *uuid.UUID
}

// OrderPaid 是订单结算记账的入参。余额抵扣额 = Total − Paid，必须等于这张订单冻结的金额（没有冻结时必须为 0）。
type OrderPaid struct {
	OrderID   uuid.UUID
	UserID    uuid.UUID
	Currency  money.Currency
	Total     decimal.Decimal
	Paid      decimal.Decimal
	Fee       decimal.Decimal
	Surcharge decimal.Decimal // 向用户加收的支付手续费，随渠道收款到账、不属于订单总额，单独贷记 platform_revenue 的 RefSurcharge；渠道实收 = Paid + Surcharge，Fee ≤ 渠道实收
	Provider  string
}

// HoldForOrder 把用户可用余额的一部分冻结给订单：记 balance_hold，再登记 balance_holds。在调用方事务里执行。
func (s *Service) HoldForOrder(ctx context.Context, in HoldInput) (Hold, error) {
	if err := validateHoldInput(in); err != nil {
		return Hold{}, err
	}
	var out Hold
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var holdErr error
		out, holdErr = s.holdForOrder(ctx, in)
		return holdErr
	})
	if err != nil {
		return Hold{}, err
	}
	return out, nil
}

func validateHoldInput(in HoldInput) error {
	if in.OrderID == uuid.Nil || in.UserID == uuid.Nil {
		return fmt.Errorf("%w: 订单或用户不能为空", ErrInvalidPosting)
	}
	if !in.Currency.Valid() {
		return fmt.Errorf("%w: 币种不合法", ErrInvalidPosting)
	}
	if !in.Amount.IsPositive() {
		return fmt.Errorf("%w: 冻结金额必须大于 0", ErrInvalidPosting)
	}
	if err := money.Check(in.Amount); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPosting, err)
	}
	return nil
}

func (s *Service) holdForOrder(ctx context.Context, in HoldInput) (Hold, error) {
	// 先查再写，撞唯一索引作并发兜底。
	_, found, err := s.repo.HoldByOrder(ctx, in.OrderID)
	if err != nil {
		return Hold{}, err
	}
	if found {
		return Hold{}, ErrHoldExists
	}
	txnID, err := s.postChecked(ctx, Posting{
		Kind:       KindBalanceHold,
		Currency:   in.Currency,
		SourceType: SourceOrder,
		SourceID:   in.OrderID,
		Legs: []Leg{
			{Account: UserAccount(UserBalance, in.UserID), Direction: Debit, Amount: in.Amount},
			{Account: UserAccount(UserBalanceHold, in.UserID), Direction: Credit, Amount: in.Amount},
		},
	})
	if err != nil {
		if errors.Is(err, ErrDuplicatePosting) {
			return Hold{}, ErrHoldExists
		}
		return Hold{}, err
	}
	h := &model.Hold{
		OrderID:   in.OrderID,
		UserID:    in.UserID,
		Currency:  string(in.Currency),
		Amount:    in.Amount,
		Status:    HoldHeld,
		HoldTxnID: txnID,
	}
	if err := s.repo.InsertHold(ctx, h); err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			return Hold{}, ErrHoldExists
		}
		return Hold{}, err
	}
	return toHold(*h), nil
}

// OrderHold 读订单的冻结（不加锁）；没有时 found 为 false。
func (s *Service) OrderHold(ctx context.Context, orderID uuid.UUID) (Hold, bool, error) {
	h, found, err := s.repo.HoldByOrder(ctx, orderID)
	if err != nil || !found {
		return Hold{}, found, err
	}
	return toHold(h), true, nil
}

// ReleaseOrderHold 释放订单的冻结（取消、超时）；在调用方事务里执行。
func (s *Service) ReleaseOrderHold(ctx context.Context, orderID uuid.UUID, memo string) (bool, error) {
	var released bool
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var relErr error
		released, relErr = s.releaseOrderHold(ctx, orderID, memo)
		return relErr
	})
	if err != nil {
		return false, err
	}
	return released, nil
}

func (s *Service) releaseOrderHold(ctx context.Context, orderID uuid.UUID, memo string) (bool, error) {
	h, found, err := s.repo.LockHoldByOrder(ctx, orderID)
	if err != nil {
		return false, err
	}
	if !found || h.Status == HoldReleased {
		return false, nil
	}
	if h.Status == HoldCaptured {
		return false, ErrHoldCaptured
	}
	if h.Status != HoldHeld {
		return false, nil
	}
	cur, err := money.ParseCurrency(h.Currency)
	if err != nil {
		return false, err
	}
	txnID, err := s.postChecked(ctx, Posting{
		Kind:       KindBalanceRelease,
		Currency:   cur,
		SourceType: SourceOrder,
		SourceID:   orderID,
		Memo:       memo,
		Legs: []Leg{
			{Account: UserAccount(UserBalanceHold, h.UserID), Direction: Debit, Amount: h.Amount},
			{Account: UserAccount(UserBalance, h.UserID), Direction: Credit, Amount: h.Amount},
		},
	})
	if err != nil {
		return false, err
	}
	ok, err := s.repo.MarkHoldReleased(ctx, h.ID, txnID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("ledger: 释放冻结时状态已变")
	}
	return true, nil
}

// CaptureFromBalance 冻结用户余额并当场扣款：余额全额支付的订单（零元待付、余额续费）用它，不要先 HoldForOrder 再 PostOrderPaid。
// 先把用户余额、冻结、收入三个科目按 ID 升序一次锁完，再记 balance_hold 与 order_paid 两笔，冻结直接是 captured；
// 分两步锁会让收入科目晚锁，与别的订单结算交叉成环（40P01）。返回冻结与 order_paid 交易 ID。在调用方事务里执行。
func (s *Service) CaptureFromBalance(ctx context.Context, in HoldInput) (Hold, uuid.UUID, error) {
	if err := validateHoldInput(in); err != nil {
		return Hold{}, uuid.Nil, err
	}
	var h Hold
	var txnID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		_, err := s.lockAccounts(ctx, in.Currency, []AccountRef{
			UserAccount(UserBalance, in.UserID),
			UserAccount(UserBalanceHold, in.UserID),
			PlatformAccount(PlatformRevenue, RefMain),
		})
		if err != nil {
			return err
		}
		h, err = s.holdForOrder(ctx, in)
		if err != nil {
			return err
		}
		txnID, err = s.postOrderPaid(ctx, OrderPaid{
			OrderID:  in.OrderID,
			UserID:   in.UserID,
			Currency: in.Currency,
			Total:    in.Amount,
			Paid:     decimal.Zero,
			Fee:      decimal.Zero,
		})
		if err != nil {
			return err
		}
		h.Status = HoldCaptured
		h.CaptureTxnID = &txnID
		return nil
	})
	if err != nil {
		return Hold{}, uuid.Nil, err
	}
	return h, txnID, nil
}

// PostOrderPaid 记订单结算（kind order_paid）并扣掉冻结；在调用方事务里执行。
// 冻结已释放时回 ErrHoldReleased；同一订单已结算回 ErrDuplicatePosting（先查，不让调用方事务作废）。
func (s *Service) PostOrderPaid(ctx context.Context, p OrderPaid) (uuid.UUID, error) {
	if err := validateOrderPaid(p); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var paidErr error
		id, paidErr = s.postOrderPaid(ctx, p)
		return paidErr
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func validateOrderPaid(p OrderPaid) error {
	if p.OrderID == uuid.Nil || p.UserID == uuid.Nil {
		return fmt.Errorf("%w: 订单或用户不能为空", ErrInvalidPosting)
	}
	if !p.Currency.Valid() {
		return fmt.Errorf("%w: 币种不合法", ErrInvalidPosting)
	}
	for _, check := range []struct {
		d   decimal.Decimal
		msg string
	}{
		{p.Total, "订单总额"},
		{p.Paid, "渠道实收"},
		{p.Fee, "渠道手续费"},
		{p.Surcharge, "用户手续费"},
	} {
		if err := money.CheckNonNegative(check.d); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidPosting, check.msg, err)
		}
	}
	if p.Paid.Cmp(p.Total) > 0 {
		return fmt.Errorf("%w: 渠道实收不能超过订单总额", ErrInvalidPosting)
	}
	channelGross := p.Paid.Add(p.Surcharge)
	if p.Fee.Cmp(channelGross) > 0 {
		return fmt.Errorf("%w: 渠道手续费不能超过渠道实收", ErrInvalidPosting)
	}
	if p.Surcharge.IsPositive() && !p.Paid.IsPositive() {
		return fmt.Errorf("%w: 只有渠道收款才能带用户手续费", ErrInvalidPosting)
	}
	if channelGross.IsPositive() {
		if p.Provider == "" {
			return fmt.Errorf("%w: 有渠道实收时必须指定渠道代码", ErrInvalidPosting)
		}
		if len(p.Provider) > 64 {
			return fmt.Errorf("%w: 渠道代码过长", ErrInvalidPosting)
		}
	}
	return nil
}

func (s *Service) postOrderPaid(ctx context.Context, p OrderPaid) (uuid.UUID, error) {
	applied := p.Total.Sub(p.Paid)
	h, holdFound, err := s.repo.LockHoldByOrder(ctx, p.OrderID)
	if err != nil {
		return uuid.Nil, err
	}
	if holdFound && h.Status == HoldCaptured {
		return uuid.Nil, ErrDuplicatePosting
	}
	if applied.IsPositive() {
		if !holdFound {
			return uuid.Nil, fmt.Errorf("%w: 有余额抵扣但没有冻结记录", ErrInvalidPosting)
		}
		// 冻结已释放（订单取消或超时）后支付才到，order 据此转挂账。
		if h.Status == HoldReleased {
			return uuid.Nil, ErrHoldReleased
		}
		if h.Status != HoldHeld {
			return uuid.Nil, fmt.Errorf("%w: 冻结状态不是 held", ErrInvalidPosting)
		}
		if !h.Amount.Equal(applied) {
			return uuid.Nil, fmt.Errorf("%w: 冻结金额与余额抵扣不一致", ErrInvalidPosting)
		}
		if h.UserID != p.UserID {
			return uuid.Nil, fmt.Errorf("%w: 冻结用户与订单用户不一致", ErrInvalidPosting)
		}
		if h.Currency != string(p.Currency) {
			return uuid.Nil, fmt.Errorf("%w: 冻结币种与订单不一致", ErrInvalidPosting)
		}
	} else if holdFound && h.Status == HoldHeld {
		return uuid.Nil, fmt.Errorf("%w: 有 held 冻结但未使用余额抵扣", ErrInvalidPosting)
	}
	if p.Total.IsZero() {
		return uuid.Nil, nil
	}
	legs := orderPaidLegs(p, applied)
	txnID, err := s.postChecked(ctx, Posting{
		Kind:       KindOrderPaid,
		Currency:   p.Currency,
		SourceType: SourceOrder,
		SourceID:   p.OrderID,
		Legs:       legs,
	})
	if err != nil {
		return uuid.Nil, err
	}
	if applied.IsPositive() {
		ok, err := s.repo.MarkHoldCaptured(ctx, h.ID, txnID)
		if err != nil {
			return uuid.Nil, err
		}
		if !ok {
			return uuid.Nil, errors.New("ledger: 扣款冻结时状态已变")
		}
	}
	return txnID, nil
}

func orderPaidLegs(p OrderPaid, applied decimal.Decimal) []Leg {
	var legs []Leg
	channelNet := p.Paid.Add(p.Surcharge).Sub(p.Fee)
	if channelNet.IsPositive() {
		legs = append(legs, Leg{
			Account:   PlatformAccount(ChannelCash, p.Provider),
			Direction: Debit,
			Amount:    channelNet,
		})
	}
	if p.Fee.IsPositive() {
		legs = append(legs, Leg{
			Account:   PlatformAccount(PlatformFeeExpense, p.Provider),
			Direction: Debit,
			Amount:    p.Fee,
		})
	}
	if applied.IsPositive() {
		legs = append(legs, Leg{
			Account:   UserAccount(UserBalanceHold, p.UserID),
			Direction: Debit,
			Amount:    applied,
		})
	}
	legs = append(legs, Leg{
		Account:   PlatformAccount(PlatformRevenue, RefMain),
		Direction: Credit,
		Amount:    p.Total,
	})
	if p.Surcharge.IsPositive() {
		legs = append(legs, Leg{
			Account:   PlatformAccount(PlatformRevenue, RefSurcharge),
			Direction: Credit,
			Amount:    p.Surcharge,
		})
	}
	return legs
}

func toHold(m model.Hold) Hold {
	cur, _ := money.ParseCurrency(m.Currency)
	return Hold{
		ID:           m.ID,
		OrderID:      m.OrderID,
		UserID:       m.UserID,
		Currency:     cur,
		Amount:       m.Amount,
		Status:       m.Status,
		HoldTxnID:    m.HoldTxnID,
		CaptureTxnID: m.CaptureTxnID,
		ReleaseTxnID: m.ReleaseTxnID,
	}
}
