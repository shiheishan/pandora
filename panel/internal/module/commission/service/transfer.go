package service

import (
	"context"
	"errors"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/model"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// postTransfer 记一笔佣金转余额：借 可提现佣金 / 贷 用户余额（commission_to_balance），并写 commission_transfers（entryID 非 nil 时是自动转余额，库里按条目唯一）。
// 调用方必须已在本事务里 LockUser。返回账本交易 id。
func (s *Service) postTransfer(ctx context.Context, userID uuid.UUID, amount decimal.Decimal, entryID *uuid.UUID, memo string) (uuid.UUID, error) {
	transferID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, errs.Wrap(errs.Internal, err)
	}
	txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
		Kind:       ledgersvc.KindCommissionToBalance,
		SourceType: SourceCommissionTransfer,
		SourceID:   transferID,
		Memo:       memo,
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, userID), Direction: ledgersvc.Debit, Amount: amount},
			{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, userID), Direction: ledgersvc.Credit, Amount: amount},
		},
	})
	if err != nil {
		if errors.Is(err, ledgersvc.ErrInsufficientBalance) {
			return uuid.Nil, ErrTransferInsufficient
		}
		return uuid.Nil, err
	}
	if err := s.repo.InsertTransfer(ctx, &model.Transfer{
		ID:          transferID,
		UserID:      userID,
		Amount:      amount,
		LedgerTxnID: txnID,
		EntryID:     entryID,
	}); err != nil {
		return uuid.Nil, err
	}
	return txnID, nil
}

// TransferToBalance 把可提现佣金转成账户余额，返回账本交易 id。
// 提现要审批、要等打款、有最低门槛；多数人只是想拿佣金续费，这条路让钱直接回到站内。
// 账本：借 可提现佣金 / 贷 用户余额，负债在科目间搬家。与提现申请用同一把用户锁、同一口径：在途提现占用的部分不能再转。
func (s *Service) TransferToBalance(ctx context.Context, userID uuid.UUID, amount decimal.Decimal) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, errs.Invalid(map[string]string{"amount": "转入金额必须大于 0"})
	}
	if money.Check(amount) != nil {
		return uuid.Nil, errs.Invalid(map[string]string{"amount": "金额最多两位小数"})
	}
	var txnID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockUser(ctx, userID); err != nil {
			return err
		}
		if err := s.rejectDeleted(ctx, userID); err != nil {
			return err
		}
		avail, err := s.available(ctx, userID)
		if err != nil {
			return err
		}
		if avail.LessThan(amount) {
			return ErrTransferInsufficient
		}
		txnID, err = s.postTransfer(ctx, userID, amount, nil, "佣金转入余额")
		if err != nil {
			return err
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionTransferredToBal,
			ResourceType: ResourceLedgerTransaction,
			ResourceID:   &txnID,
			Data: map[string]any{
				"amount":            amount.StringFixed(2),
				"commission_before": avail.StringFixed(2),
				"commission_after":  avail.Sub(amount).StringFixed(2),
			},
		})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return txnID, nil
}
