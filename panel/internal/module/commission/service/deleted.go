package service

import (
	"context"
	"errors"

	"github.com/aegispanel/aegis/internal/module/commission/model"
	"github.com/aegispanel/aegis/internal/module/commission/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// VoidForDeletedUser 在账号注销的事务里作废这个人名下全部未出账的佣金（用户 2026-10-14 定），由 identity 的 DeleteUser 经端口调用：
//  1. 有已批准待打款的提现就整笔拒绝注销（ErrApprovedWithdrawalPending，用户 10-14 定：管理员可能已线下打款）；
//     申请中的提现驳回，原因 account_deleted。提现只改状态不记账，驳回后这笔钱回到可提现。
//  2. 他作为受益人的冻结中条目（待复核与否）逐条作废：记 commission_voided 冲回计提（借 待结佣金 / 贷 平台收入 main，按条目唯一），
//     条目转 voided，原因 account_deleted，复核人记执行注销的管理员。
//  3. 清掉他条目上「等自动转余额」的标记。
//  4. 按唯一口径算剩下的可用佣金（第 1 步驳回的已退回来），为正的记一笔 commission_voided（借 可提现佣金 / 贷 平台收入 main），
//     并写 commission_forfeitures（每人最多一行，库里唯一）。可提现是池、不按条目扣，所以这部分不落到条目上，
//     可用佣金口径相应减去作废合计（见 available）。
//
// 已打款的提现、已转余额的钱不动。全部做完后他的待结、可提现佣金科目都为 0。再调一次什么都不做、返回全 0。
//
// 锁序（注释与 module-order 规则同步）：注销事务锁用户行（FOR NO KEY UPDATE）后先调 LockForDeletion 拿 LockUser
// （与提现申请、手动/自动转余额同一把咨询锁），再取消待付单（会锁他的余额与冻结科目）、停订阅（库存、券、订阅行都在这之前），
// 然后调本方法：LockUser（同一事务里重入）→ 在途提现行（按 id 升序）→ 冻结中条目行（一条语句按 id 升序锁完）
// → 逐笔分录锁 待结佣金或可提现佣金 + 平台收入 main（按科目 id 升序；平台收入 id 总小于个人佣金科目，先等平台收入）。
// 咨询锁必须在取消待付单之前拿：否则注销拿着他的余额科目等咨询锁，转余额拿着咨询锁与可提现佣金等余额，两边互等。
// 拿咨询锁的路径都把它放在第一把，之前不持有别的锁，所以注销一开头就拿它不会与谁互等。
// 拿科目之前不持有任何佣金科目锁，所以与这些路径都不互等：
//   - 计提（结算事务已持平台收入，插条目时外键给用户行加 KEY SHARE）：调用方此时只持用户行的 NO KEY UPDATE，与 KEY SHARE 相容，
//     结算照常提交后这里才拿到平台收入；注销必须在把用户行升到 FOR UPDATE（改占位邮箱）之前调本方法，否则会与计提互等。
//     那一刻才提交的条目不在第 2 步的名单里，由解冻时的已注销判断转人工复核（settleOne）。
//   - 解冻 settleOne（条目行 → 待结 + 可提现）、复核作废（条目行 → 待结 + 平台收入）：这里等条目行时不持有科目。
//   - 自动转余额 autoTransferOne、手动转余额、提现申请：同一把 LockUser，先后串行；之后它们看到已注销回 409。
//   - 打款 MarkWithdrawalPaid（提现行 → 可提现 + 打款渠道）、后台审批：这里等提现行时不持有科目；抢先提交的打款这里重读后不再在途，不动。
func (s *Service) VoidForDeletedUser(ctx context.Context, userID uuid.UUID) (VoidResult, error) {
	adminID, err := principalID(ctx)
	if err != nil {
		return VoidResult{}, err
	}
	res := VoidResult{
		VoidedPending:  decimal.Zero,
		Forfeited:      decimal.Zero,
		RejectedAmount: decimal.Zero,
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockUser(ctx, userID); err != nil {
			return err
		}
		now := s.clock()

		wds, err := s.repo.LockInFlightWithdrawals(ctx, userID)
		if err != nil {
			return err
		}
		// 先查完再驳回：行都已锁住，并发的批准要么已提交（这里看到 approved），要么排在注销之后（届时看到已注销回 409）
		for _, w := range wds {
			if w.Status == WithdrawalApproved {
				return ErrApprovedWithdrawalPending
			}
		}
		for _, w := range wds {
			changed, err := s.repo.RejectWithdrawalForDeletedUser(ctx, w.ID, ReasonAccountDeleted, adminID, now)
			if err != nil {
				return err
			}
			if !changed {
				return errPayoutStateLost
			}
			res.RejectedWithdrawals++
			res.RejectedAmount = res.RejectedAmount.Add(w.Amount)
		}

		entries, err := s.repo.LockPendingEntriesOf(ctx, userID)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := s.voidPendingForDeleted(ctx, e, adminID); err != nil {
				return err
			}
			res.VoidedEntries++
			res.VoidedPending = res.VoidedPending.Add(e.CommissionAmount)
		}

		if _, err := s.repo.ClearAutoTransferOf(ctx, userID, now); err != nil {
			return err
		}

		avail, err := s.available(ctx, userID)
		if err != nil {
			return err
		}
		if !avail.IsPositive() {
			if avail.IsNegative() {
				plog.From(ctx).Warn("commission: 注销时可用佣金为负（账本被手工改过？），不作废",
					zap.String("user_id", userID.String()),
					zap.String("available", avail.StringFixed(2)))
			}
		} else {
			if err := s.forfeitAvailable(ctx, userID, avail); err != nil {
				return err
			}
			res.Forfeited = avail
		}
		return nil
	})
	if err != nil {
		return VoidResult{}, err
	}
	return res, nil
}

// LockForDeletion 在注销事务里拿这个人的佣金咨询锁（LockUser），持有到注销提交：注销锁用户行之后、取消待付单之前调用，
// 理由见 VoidForDeletedUser 的锁序。必须在事务里调用（咨询锁随事务释放）。
func (s *Service) LockForDeletion(ctx context.Context, userID uuid.UUID) error {
	// 事务外调用时锁会随这次单独的事务立刻释放，等于没锁：直接拒绝，免得静默失效
	if !tx.InTx(ctx) {
		return errors.New("commission: LockForDeletion 必须在注销事务里调用")
	}
	return s.repo.LockUser(ctx, userID)
}

// voidPendingForDeleted 作废一条冻结中的条目（调用方已锁条目行）：冲回计提，条目转 voided。
func (s *Service) voidPendingForDeleted(ctx context.Context, e model.Entry, adminID uuid.UUID) error {
	txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
		Kind:       ledgersvc.KindCommissionVoided,
		SourceType: SourceCommissionEntry,
		SourceID:   e.ID,
		Memo:       "账号注销，作废冻结中的佣金",
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionPending, e.ReferrerUserID), Direction: ledgersvc.Debit, Amount: e.CommissionAmount},
			{Account: ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefMain), Direction: ledgersvc.Credit, Amount: e.CommissionAmount},
		},
	})
	if err != nil {
		if errors.Is(err, ledgersvc.ErrInsufficientBalance) {
			return ErrVoidUnderfunded
		}
		return err
	}
	changed, err := s.repo.VoidPendingEntry(ctx, e.ID, txnID, ReasonAccountDeleted, adminID, s.clock())
	if err != nil {
		return err
	}
	if !changed {
		return errSettleLost
	}
	return nil
}

// forfeitAvailable 作废剩下的可用佣金：记 commission_voided（借 可提现佣金 / 贷 平台收入 main），写 commission_forfeitures。
func (s *Service) forfeitAvailable(ctx context.Context, userID uuid.UUID, amount decimal.Decimal) error {
	id, err := uuid.NewV7()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
		Kind:       ledgersvc.KindCommissionVoided,
		SourceType: SourceCommissionForfeiture,
		SourceID:   id,
		Memo:       "账号注销，作废可提现佣金",
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, userID), Direction: ledgersvc.Debit, Amount: amount},
			{Account: ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefMain), Direction: ledgersvc.Credit, Amount: amount},
		},
	})
	if err != nil {
		if errors.Is(err, ledgersvc.ErrInsufficientBalance) {
			return errForfeitUnderfunded
		}
		return err
	}
	err = s.repo.InsertForfeiture(ctx, &model.Forfeiture{
		ID:          id,
		UserID:      userID,
		Amount:      amount,
		Reason:      ReasonAccountDeleted,
		LedgerTxnID: txnID,
	})
	if errors.Is(err, repo.ErrDuplicate) {
		// 持有 LockUser 时不应发生：作废过一次后可用为 0，不会再走到这里
		return errs.Newf(errs.Conflict, "这个账号的可提现佣金已作废过")
	}
	return err
}

// errForfeitUnderfunded 是可提现佣金科目余额不够作废（账本与条目、提现、转余额记录对不上，需要人工核对）。
var errForfeitUnderfunded = errs.Newf(errs.Conflict, "可提现佣金科目余额不足，无法作废，请先核对账本")

// rejectDeleted 在受益人或申请人已注销时回 ErrAccountDeleted（identity 的无锁读 AccountDeleted）。
// 注销事务里先作废、再提交；调用方须已锁住会与注销作废冲突的行（用户咨询锁、提现行或条目行），
// 这样读到「未注销」时注销的作废一定排在自己提交之后，看得见自己做的事。
func (s *Service) rejectDeleted(ctx context.Context, userID uuid.UUID) error {
	gone, err := s.accountDeleted(ctx, userID)
	if err != nil {
		return err
	}
	if gone {
		return ErrAccountDeleted
	}
	return nil
}

// accountDeleted 是 identity 的无锁读 AccountDeleted。
func (s *Service) accountDeleted(ctx context.Context, userID uuid.UUID) (bool, error) {
	refs, err := s.needRefs()
	if err != nil {
		return false, err
	}
	return refs.AccountDeleted(ctx, userID)
}
