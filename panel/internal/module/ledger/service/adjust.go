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

// Adjustment 是后台人工调账的入参。Amount 正数加、负数减。
type Adjustment struct {
	UserID uuid.UUID
	Amount decimal.Decimal
	Reason string
}

// AdjustResult 是调账后的结果。
type AdjustResult struct {
	Balance decimal.Decimal
	TxnID   uuid.UUID
}

// 调账原因的字数范围（按 rune 计，去掉首尾空白后）。
const (
	adjustReasonMin = 5
	adjustReasonMax = 500
)

// errAccountDeleted 是给已注销账号加钱（与 identity 同码同文案）；扣减不拒。
var errAccountDeleted = errs.Newf(errs.Conflict, "账号已注销")

// normalizeAdjustment 校验调账金额与原因；返回去掉首尾空白的原因。
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
	if s.accounts == nil {
		return AdjustResult{}, errs.Wrap(errs.Internal, errors.New("ledger: AccountStates 未装配"))
	}
	var result AdjustResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		// 已注销账号只拒加钱：扣减照常放行（仍要原因、重认证、审计），用于线下退款后把剩下的余额冲平——
		// 注销前的余额、注销时释放的冻结、注销后转入的迟到付款都会留在这个账号上。
		// 先于记账：identity 读时给用户行加共享锁，锁序与注销（用户行 → 订单 → 余额冻结）一致。
		if a.Amount.IsPositive() {
			deleted, err := s.accounts.IsDeleted(ctx, a.UserID)
			if err != nil {
				return err
			}
			if deleted {
				return errAccountDeleted
			}
		}
		var adjErr error
		result, adjErr = s.adjustBalance(ctx, a, reason)
		return adjErr
	})
	if err != nil {
		return AdjustResult{}, err
	}
	return result, nil
}

func (s *Service) adjustBalance(ctx context.Context, a Adjustment, reason string) (AdjustResult, error) {
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
	txnID, err := s.postChecked(ctx, Posting{
		Kind:       KindBalanceAdjusted,
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
	after, err := s.Balance(ctx, a.UserID)
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
			"reason":        reason,
			"balance_after": after.StringFixed(2),
			"ledger_txn":    txnID.String(),
		},
	}); err != nil {
		return AdjustResult{}, err
	}
	return AdjustResult{
		Balance: after,
		TxnID:   txnID,
	}, nil
}
