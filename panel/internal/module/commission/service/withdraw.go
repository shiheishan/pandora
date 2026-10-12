package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/model"
	"github.com/aegispanel/aegis/internal/module/commission/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// errPayoutStateLost 是打款改状态时没改到行。
var errPayoutStateLost = errs.Newf(errs.Conflict, "提现状态已变化，请刷新后重试")

// errPayoutUnderfunded 是可提现佣金科目余额不够这笔打款（账本与提现记录对不上，需要人工核对）。
var errPayoutUnderfunded = errs.Newf(errs.Conflict, "可提现佣金账户余额不足，无法打款")

// available 按唯一口径算用户某币种的可用佣金：已解冻 − 未驳回的提现（在途与已打款）− 已转余额。可能为负（只在账本被手工改过时）。
// 要据此放行动账的调用方必须先在本事务里 LockUser。
func (s *Service) available(ctx context.Context, userID uuid.UUID, cur money.Currency) (decimal.Decimal, error) {
	et, err := s.repo.EntryTotalsOf(ctx, userID, string(cur))
	if err != nil {
		return decimal.Zero, err
	}
	wt, err := s.repo.WithdrawalTotalsOf(ctx, userID, string(cur))
	if err != nil {
		return decimal.Zero, err
	}
	tr, err := s.repo.TransferTotalOf(ctx, userID, string(cur))
	if err != nil {
		return decimal.Zero, err
	}
	return et.Available.Sub(wt.InFlight).Sub(wt.Paid).Sub(tr), nil
}

// RequestWithdrawal 发起一笔提现申请（站点币种），返回申请 id。只改状态不记账，钱在打款那一步才离开可提现佣金。
// 一个用户同一时间只能有一笔在途；与转余额用同一把用户锁、同一口径，后到的一方一定看得见前一方。
func (s *Service) RequestWithdrawal(ctx context.Context, userID uuid.UUID, amount decimal.Decimal, payoutDetail string) (uuid.UUID, error) {
	payout, err := validateWithdrawal(amount, payoutDetail)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockUser(ctx, userID); err != nil {
			return err
		}
		cfg, err := s.settings.Commission(ctx)
		if err != nil {
			return err
		}
		if amount.LessThan(cfg.MinWithdraw) {
			return ErrWithdrawTooSmall
		}
		inFlight, err := s.repo.HasInFlightWithdrawal(ctx, userID)
		if err != nil {
			return err
		}
		if inFlight {
			return ErrWithdrawPending
		}
		cur, err := s.ledger.DefaultCurrency(ctx)
		if err != nil {
			return err
		}
		avail, err := s.available(ctx, userID, cur)
		if err != nil {
			return err
		}
		if !avail.IsPositive() {
			return ErrNoCommission
		}
		if amount.GreaterThan(avail) {
			return ErrWithdrawTooMuch
		}
		sealed, err := s.sealer.Seal([]byte(payout), payoutAAD(userID))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		w := &model.Withdrawal{
			UserID:       userID,
			Currency:     string(cur),
			Amount:       amount,
			PayoutSealed: sealed,
			Status:       WithdrawalRequested,
		}
		if err := s.repo.InsertWithdrawal(ctx, w); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return ErrWithdrawPending
			}
			return err
		}
		id = w.ID
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionWithdrawalRequested,
			ResourceType: ResourceWithdrawal,
			ResourceID:   &id,
			Data:         map[string]any{"amount": amount.StringFixed(2), "currency": string(cur)},
		})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// validateWithdrawal 校验提现金额与收款信息，返回去掉首尾空白的收款信息。
func validateWithdrawal(amount decimal.Decimal, payoutDetail string) (string, error) {
	fields := map[string]string{}
	if !amount.IsPositive() {
		fields["amount"] = "提现金额必须大于 0"
	} else if money.Check(amount) != nil {
		fields["amount"] = "金额最多两位小数"
	}
	payout := strings.TrimSpace(payoutDetail)
	switch {
	case payout == "":
		fields["payout_detail"] = "请填写收款方式"
	case strings.ContainsRune(payout, 0):
		fields["payout_detail"] = "收款方式含有非法字符"
	case utf8.RuneCountInString(payout) > MaxPayoutDetailRunes:
		fields["payout_detail"] = "收款方式最多 500 个字"
	}
	if len(fields) > 0 {
		return "", errs.Invalid(fields)
	}
	return payout, nil
}

// ReviewWithdrawal 批准或驳回一笔申请中的提现（后台），返回新状态。驳回必须给理由，批准不能带理由；不动账。
func (s *Service) ReviewWithdrawal(ctx context.Context, id uuid.UUID, approve bool, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case approve && reason != "":
		return "", errs.Invalid(map[string]string{"reason": "批准时不能填写拒绝理由"})
	case !approve && reason == "":
		// 拒绝必须给理由：用户会来问，「不知道为什么被拒」最容易升级成工单
		return "", errs.Invalid(map[string]string{"reason": "拒绝时必须填写理由"})
	case strings.ContainsRune(reason, 0):
		return "", errs.Invalid(map[string]string{"reason": "理由含有非法字符"})
	case utf8.RuneCountInString(reason) > MaxRejectReasonRunes:
		return "", errs.Invalid(map[string]string{"reason": "理由最多 500 个字"})
	}
	adminID, err := principalID(ctx)
	if err != nil {
		return "", err
	}
	to, action := WithdrawalApproved, ActionWithdrawalApprove
	var reasonPtr *string
	if !approve {
		to, action = WithdrawalRejected, ActionWithdrawalReject
		reasonPtr = &reason
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		w, ok, err := s.repo.WithdrawalByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !ok {
			return ErrWithdrawalNotFound
		}
		// 只有申请中的可以审批：已打款的再被「拒绝」会让钱既出去了又记成拒绝
		if !CanWithdrawalTransition(w.Status, to) {
			return ErrWithdrawalHandled
		}
		changed, err := s.repo.ReviewWithdrawal(ctx, id, to, reasonPtr, adminID, s.clock())
		if err != nil {
			return err
		}
		if !changed {
			return ErrWithdrawalHandled
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       action,
			ResourceType: ResourceWithdrawal,
			ResourceID:   &id,
			Data:         map[string]any{"status": to, "reason": reason},
		})
	})
	if err != nil {
		return "", err
	}
	return to, nil
}

// MarkWithdrawalPaid 记录一笔已批准的提现已实际打款（后台）：锁行、记 commission_paid、改状态、写审计，同一事务。
// 这是提现流程里唯一动账本的一步。
func (s *Service) MarkWithdrawalPaid(ctx context.Context, id uuid.UUID, reference string) error {
	reference = strings.TrimSpace(reference)
	switch {
	case reference == "":
		return errs.Invalid(map[string]string{"payout_reference": "请填写转账流水号，日后对账要用"})
	case strings.ContainsRune(reference, 0):
		return errs.Invalid(map[string]string{"payout_reference": "流水号含有非法字符"})
	case utf8.RuneCountInString(reference) > MaxPayoutReferenceRunes:
		return errs.Invalid(map[string]string{"payout_reference": "流水号最多 128 个字"})
	}
	adminID, err := principalID(ctx)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		w, ok, err := s.repo.WithdrawalByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !ok {
			return ErrWithdrawalNotFound
		}
		if !CanWithdrawalTransition(w.Status, WithdrawalPaid) {
			return ErrWithdrawalNotPayable
		}
		cur := money.Currency(w.Currency)
		txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindCommissionPaid,
			Currency:   cur,
			SourceType: SourceWithdrawal,
			SourceID:   w.ID,
			Memo:       "佣金提现打款",
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, w.UserID), Direction: ledgersvc.Debit, Amount: w.Amount},
				{Account: ledgersvc.PlatformAccount(ledgersvc.ChannelCash, ledgersvc.RefPayout), Direction: ledgersvc.Credit, Amount: w.Amount},
			},
		})
		if err != nil {
			if errors.Is(err, ledgersvc.ErrInsufficientBalance) {
				return errPayoutUnderfunded
			}
			return err
		}
		changed, err := s.repo.MarkWithdrawalPaid(ctx, id, reference, txnID, adminID, s.clock())
		if err != nil {
			return err
		}
		if !changed {
			return errPayoutStateLost
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionWithdrawalPaid,
			ResourceType: ResourceWithdrawal,
			ResourceID:   &id,
			Data: map[string]any{
				"amount":     w.Amount.StringFixed(2),
				"currency":   w.Currency,
				"reference":  reference,
				"ledger_txn": txnID.String(),
			},
		})
	})
}
