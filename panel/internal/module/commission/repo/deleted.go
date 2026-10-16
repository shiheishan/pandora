package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

// 账号注销时作废佣金用到的读写（调用方 service.VoidForDeletedUser，锁序见那里）。

// LockInFlightWithdrawals 按 id 升序锁定用户全部在途提现（申请中、已批准待打款），FOR UPDATE 等锁不跳过。
// 已批准的也锁上：调用方据此拒绝注销。
// 等锁期间被打款或驳回的行，PG 按新版本重查 WHERE，不再返回。
func (r *Repo) LockInFlightWithdrawals(ctx context.Context, userID uuid.UUID) ([]model.Withdrawal, error) {
	var out []model.Withdrawal
	err := r.d.Conn(ctx).Raw(`
SELECT * FROM withdrawals
 WHERE user_id = ? AND status IN ('requested', 'approved')
 ORDER BY id
 FOR UPDATE`, userID).Scan(&out).Error
	return out, mapErr(err)
}

// RejectWithdrawalForDeletedUser 账号注销时驳回一笔申请中的提现（已批准的不驳回：有已批准的就不许注销）。
// 记驳回原因、操作的管理员与时间；没改到行（已不在途）返回 false。
func (r *Repo) RejectWithdrawalForDeletedUser(ctx context.Context, id uuid.UUID, reason string, by uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Withdrawal{}).
		Where("id = ? AND status = ?", id, "requested").
		Updates(map[string]any{
			"status":        "rejected",
			"reject_reason": reason,
			"reviewed_by":   by,
			"reviewed_at":   at,
			"completed_at":  at,
			"updated_at":    at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// LockPendingEntriesOf 按 id 升序锁定某人作为受益人的全部冻结中条目（含待复核），一条语句锁完、等锁不跳过。
// 等锁期间被解冻或作废的行，PG 按新版本重查 WHERE，不再返回。
func (r *Repo) LockPendingEntriesOf(ctx context.Context, referrerID uuid.UUID) ([]model.Entry, error) {
	var out []model.Entry
	err := r.d.Conn(ctx).Raw(`
SELECT * FROM commission_entries
 WHERE referrer_user_id = ? AND status = 'pending'
 ORDER BY id
 FOR UPDATE`, referrerID).Scan(&out).Error
	return out, mapErr(err)
}

// VoidPendingEntry 作废一条冻结中的条目（待复核与否都行）：转 voided、写冲回交易与原因、记操作的管理员与时间，
// 清 review_required（review_reason 留作历史）。没改到行（已不是 pending）返回 false。
func (r *Repo) VoidPendingEntry(ctx context.Context, id, voidTxnID uuid.UUID, reason string, by uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("id = ? AND status = ?", id, "pending").
		Updates(map[string]any{
			"status":          "voided",
			"review_required": false,
			"void_txn_id":     voidTxnID,
			"void_reason":     reason,
			"reviewed_by":     by,
			"reviewed_at":     at,
			"updated_at":      at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ClearAutoTransferOf 清掉某人全部条目上的「等自动转余额」标记，返回清掉的条数。
func (r *Repo) ClearAutoTransferOf(ctx context.Context, referrerID uuid.UUID, at time.Time) (int64, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("referrer_user_id = ? AND auto_transfer_due", referrerID).
		Updates(map[string]any{"auto_transfer_due": false, "updated_at": at})
	return res.RowsAffected, mapErr(res.Error)
}

// InsertForfeiture 插入一行注销作废记录；同一人已有时回 ErrDuplicate。
func (r *Repo) InsertForfeiture(ctx context.Context, f *model.Forfeiture) error {
	if err := checkMoney(f.Amount); err != nil {
		return err
	}
	if err := r.d.Conn(ctx).Create(f).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// ForfeitTotalOf 用户注销时作废的可提现佣金合计（没有为 0）。
func (r *Repo) ForfeitTotalOf(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.d.Conn(ctx).Raw(
		`SELECT COALESCE(sum(amount), 0) FROM commission_forfeitures WHERE user_id = ?`,
		userID,
	).Scan(&total).Error
	return total, mapErr(err)
}
