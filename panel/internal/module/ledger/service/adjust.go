package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Adjustment 是后台人工调账的入参。Amount 正数加、负数减；Currency 为空时用站点币种。
type Adjustment struct {
	UserID   uuid.UUID
	Amount   decimal.Decimal
	Currency string
	Reason   string
}

// AdjustResult 是调账后的结果。
type AdjustResult struct {
	Currency money.Currency
	Balance  decimal.Decimal
	TxnID    uuid.UUID
}

// 调账原因的字数范围（按 rune 计，去掉首尾空白后）。
const (
	adjustReasonMin = 5
	adjustReasonMax = 500
)

// normalizeAdjustment 校验调账金额与原因（与币种无关）；返回去掉首尾空白的原因。
func normalizeAdjustment(a Adjustment) (reason string, err error) {
	if a.Amount.IsZero() {
		return "", errs.Newf(errs.ValidationFailed, "调整金额不能为零")
	}
	if money.Check(a.Amount) != nil {
		return "", errs.Invalid(map[string]string{"amount": "金额最多两位小数"})
	}
	reason = strings.TrimSpace(a.Reason)
	if !utf8.ValidString(reason) {
		return "", errs.Invalid(map[string]string{"reason": "请写清调整原因，5 到 500 个字"})
	}
	if strings.ContainsRune(reason, 0) {
		return "", errs.Invalid(map[string]string{"reason": "请写清调整原因，5 到 500 个字"})
	}
	n := utf8.RuneCountInString(reason)
	if n < adjustReasonMin || n > adjustReasonMax {
		return "", errs.Invalid(map[string]string{"reason": "请写清调整原因，5 到 500 个字"})
	}
	return reason, nil
}

// AdjustBalance 由管理员直接增减用户余额：记 balance_adjusted，同事务写审计 balance.adjusted。
func (s *Service) AdjustBalance(ctx context.Context, a Adjustment) (AdjustResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return AdjustResult{}, err
	}
	if p.Audience != authn.AudienceAdmin {
		return AdjustResult{}, errs.New(errs.Forbidden)
	}
	if a.UserID == uuid.Nil {
		return AdjustResult{}, ErrUserNotFound
	}
	reason, err := normalizeAdjustment(a)
	if err != nil {
		return AdjustResult{}, err
	}
	var cur money.Currency
	if strings.TrimSpace(a.Currency) != "" {
		cur, err = money.ParseCurrency(a.Currency)
		if err != nil {
			return AdjustResult{}, errs.Invalid(map[string]string{"currency": "币种只支持 CNY 或 USD"})
		}
	} else {
		cur, err = s.DefaultCurrency(ctx)
		if err != nil {
			return AdjustResult{}, err
		}
	}
	var result AdjustResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		var adjErr error
		result, adjErr = s.adjustBalance(ctx, a, cur, reason)
		return adjErr
	})
	if err != nil {
		return AdjustResult{}, err
	}
	return result, nil
}

func (s *Service) adjustBalance(ctx context.Context, a Adjustment, cur money.Currency, reason string) (AdjustResult, error) {
	abs := a.Amount.Abs()
	var legs []Leg
	if a.Amount.IsPositive() {
		legs = []Leg{
			{Account: PlatformAccount(Suspense, RefManualAdjust), Direction: Debit, Amount: abs},
			{Account: UserAccount(UserBalance, a.UserID), Direction: Credit, Amount: abs},
		}
	} else {
		legs = []Leg{
			{Account: UserAccount(UserBalance, a.UserID), Direction: Debit, Amount: abs},
			{Account: PlatformAccount(Suspense, RefManualAdjust), Direction: Credit, Amount: abs},
		}
	}
	txnID, err := s.Post(ctx, Posting{
		Kind:       KindBalanceAdjusted,
		Currency:   cur,
		SourceType: SourceUser,
		SourceID:   a.UserID,
		Memo:       reason,
		Legs:       legs,
	})
	if err != nil {
		if errors.Is(err, ErrInsufficientBalance) {
			return AdjustResult{}, errs.Newf(errs.Conflict, "余额不足，无法扣减这么多")
		}
		return AdjustResult{}, err
	}
	after, err := s.Balance(ctx, a.UserID, cur)
	if err != nil {
		return AdjustResult{}, err
	}
	uid := a.UserID
	if err := s.audit.Write(ctx, auditsvc.Event{
		Action:       "balance.adjusted",
		ResourceType: "user",
		ResourceID:   &uid,
		Data: map[string]any{
			"amount":        a.Amount.StringFixed(2),
			"currency":      string(cur),
			"reason":        reason,
			"balance_after": after.StringFixed(2),
			"ledger_txn":    txnID.String(),
		},
	}); err != nil {
		return AdjustResult{}, err
	}
	return AdjustResult{
		Currency: cur,
		Balance:  after,
		TxnID:    txnID,
	}, nil
}
