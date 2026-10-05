// [INPUT]: 依赖本包 commission.go 的 PostWithdrawalPayout（打款记账）、CommissionDefault* 与 ValidCommissionScope / CommissionScopeEveryOrder，依赖 platform 的 db 租户事务、audit 同事务审计、httpx 的错误模型与请求 ID；读写 withdrawals / system_settings 的 commission.*，读 commission_entries / referrals / users
// [OUTPUT]: 对外提供 Service.AdminListWithdrawals / AdminReviewWithdrawal / AdminMarkWithdrawalPaid / AdminCommissionOverview / AdminSetCommissionConfig 与 AdminWithdrawal、AdminCommissionStats、CommissionConfigInput
// [POS]: domain/billing 的后台佣金与提现用例（从 api/admin/commission.go 下沉）：申请 → 审批 → 打款三步只有打款动账本（同事务调 PostWithdrawalPayout 再 CAS 置 paid）；总览的参数缺行回退与计提同一组 CommissionDefault*；收款信息只回密文，由 handler 用信封解开
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// AdminWithdrawal 是后台提现列表的一行；收款信息是密文（AAD "payout"），调用方解开。
type AdminWithdrawal struct {
	ID              string
	Email           string
	UserID          string
	Amount          int64
	Currency        string
	Status          string
	PayoutEncrypted []byte
	Reject          string
	Requested       any
	Completed       any
	// Earned 是这个用户累计赚到的佣金，用来判断提现是否合理：
	// 提现额远大于历史佣金说明哪里不对
	Earned int64
}

// AdminListWithdrawals 按申请时间倒序列出最近 200 笔提现，status 为空即不筛。
func (s *Service) AdminListWithdrawals(ctx context.Context, tenantID, status string) ([]AdminWithdrawal, error) {
	out := []AdminWithdrawal{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT wd.id::text, COALESCE(u.email::text,''), wd.user_id::text,
			       wd.amount, wd.currency::text, wd.status,
			       COALESCE(wd.payout_detail_encrypted, ''::bytea),
			       COALESCE(wd.reject_reason,''), wd.requested_at, wd.completed_at,
			       COALESCE((SELECT sum(ce.commission_amount) FROM commission_entries ce
			                  WHERE ce.tenant_id = wd.tenant_id
			                    AND ce.referrer_user_id = wd.user_id
			                    AND ce.status <> 'reversed'), 0)
			  FROM withdrawals wd
			  LEFT JOIN users u ON u.id = wd.user_id
			 WHERE wd.tenant_id = $1
			   AND ($2 = '' OR wd.status = $2)
			 ORDER BY wd.requested_at DESC LIMIT 200`, tenantID, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rw AdminWithdrawal
			if err := rows.Scan(&rw.ID, &rw.Email, &rw.UserID, &rw.Amount, &rw.Currency,
				&rw.Status, &rw.PayoutEncrypted, &rw.Reject, &rw.Requested, &rw.Completed,
				&rw.Earned); err != nil {
				return err
			}
			out = append(out, rw)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AdminReviewWithdrawal 批准或拒绝一笔还在待处理的提现（action 为 approve / reject，
// newStatus 为对应的 approved / rejected，均由调用方校验）；同事务写 withdrawal.<action> 审计。
func (s *Service) AdminReviewWithdrawal(ctx context.Context, tenantID string, actorID *string,
	id, action, newStatus, reason string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 只有还在待处理状态的申请可以审批。已打款的再被「拒绝」
		// 会让钱既出去了又记成拒绝
		tag, err := tx.Exec(ctx, `
			UPDATE withdrawals
			   SET status = $3, reject_reason = $4, updated_at = now(),
			       completed_at = CASE WHEN $3 = 'rejected' THEN now() ELSE completed_at END
			 WHERE tenant_id = $1 AND id = $2::uuid
			   AND status IN ('requested','reviewing')`,
			tenantID, id, newStatus, nullIfEmptyStr(reason))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.New(httpx.CodeConflict, "该提现申请已被处理过")
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "withdrawal." + action, ResourceType: "withdrawal", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"status": newStatus, "reason": reason},
		})
	})
	return err
}

// AdminMarkWithdrawalPaid 记录一笔已批准的提现已实际打款：锁行、记账、CAS 置 paid、写审计，同一事务。
//
// 这是唯一动账本的一步：钱真的离开平台了。
func (s *Service) AdminMarkWithdrawalPaid(ctx context.Context, tenantID string, actorID *string,
	id, reference string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var userID, currency string
		var amount int64
		err := tx.QueryRow(ctx, `
			SELECT user_id::text, currency::text, amount
			  FROM withdrawals
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'approved'
			 FOR UPDATE`, tenantID, id).Scan(&userID, &currency, &amount)
		if err == pgx.ErrNoRows {
			return httpx.New(httpx.CodeConflict, "只有已批准的提现才能标记为已打款")
		}
		if err != nil {
			return err
		}

		txnID, err := s.PostWithdrawalPayout(ctx, tx, tenantID,
			userID, currency, amount, id)
		if err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE withdrawals
			   SET status = 'paid', payout_reference = $3, payout_txn_id = $4::uuid,
			       completed_at = now(), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid
			   AND status = 'processing' AND payout_txn_id = $4::uuid`,
			tenantID, id, reference, txnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.New(httpx.CodeConflict, "提现状态已变化，请刷新后重试")
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "withdrawal.paid", ResourceType: "withdrawal", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"amount": amount, "currency": currency, "reference": reference,
				"ledger_txn": txnID},
		})
	})
	return err
}

// AdminCommissionStats 是分销的整体情况与当前参数。
type AdminCommissionStats struct {
	Pending, Available, PaidOut, ThisMonth, TotalEarned   int64
	Entries, NeedReview, WaitingWithdrawals, InvitedUsers int
	RatePercent, FreezeDays                               int
	MinWithdraw                                           int64
	Scope                                                 string
}

// AdminCommissionOverview 汇总佣金、提现与分销参数；参数缺行回退 CommissionDefault*，
// 计佣范围没有设置或值不认识都按每笔订单。
func (s *Service) AdminCommissionOverview(ctx context.Context, tenantID string) (*AdminCommissionStats, error) {
	var out *AdminCommissionStats
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var pending, available, paidOut, thisMonth, totalEarned int64
		var entries, reviewers, invited int
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(sum(commission_amount) FILTER (WHERE status='pending'),0),
			       COALESCE(sum(commission_amount) FILTER (WHERE status='available'),0),
			       COALESCE(sum(commission_amount) FILTER (
			         WHERE created_at >= date_trunc('month', now())),0),
			       COALESCE(sum(commission_amount) FILTER (
			         WHERE status NOT IN ('reversed','rejected')),0),
			       count(*),
			       count(*) FILTER (WHERE review_required = true AND status = 'pending'),
			       (SELECT count(*) FROM referrals WHERE tenant_id = $1)
			  FROM commission_entries WHERE tenant_id = $1`,
			tenantID).Scan(&pending, &available, &thisMonth, &totalEarned,
			&entries, &reviewers, &invited); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(sum(amount),0) FROM withdrawals
			 WHERE tenant_id = $1 AND status = 'paid'`, tenantID).Scan(&paidOut); err != nil {
			return err
		}

		var rate, freeze int
		var minW int64
		var scope string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.rate_percent'),$2::int),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.freeze_days'),$3::int),
			       COALESCE((SELECT (value #>> '{}')::bigint FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.min_withdraw'),$4::bigint),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.scope'),'')`,
			// 缺行回退与计提同一组常量（CommissionDefault*）
			tenantID, CommissionDefaultRatePercent, CommissionDefaultFreezeDays,
			CommissionDefaultMinWithdraw).Scan(&rate, &freeze, &minW, &scope); err != nil {
			return err
		}
		// 与计提同一个兜底：没有设置或值不认识都按每笔订单
		if !ValidCommissionScope(scope) {
			scope = CommissionScopeEveryOrder
		}

		var waiting int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM withdrawals
			 WHERE tenant_id = $1 AND status IN ('requested','reviewing')`,
			tenantID).Scan(&waiting); err != nil {
			return err
		}

		out = &AdminCommissionStats{
			Pending: pending, Available: available, PaidOut: paidOut,
			ThisMonth: thisMonth, Entries: entries,
			NeedReview: reviewers, WaitingWithdrawals: waiting,
			RatePercent: rate, FreezeDays: freeze, MinWithdraw: minW,
			TotalEarned: totalEarned, InvitedUsers: invited, Scope: scope,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CommissionConfigInput 是已校验过的分销参数修改，nil 即不改。
type CommissionConfigInput struct {
	RatePercent *int
	FreezeDays  *int
	MinWithdraw *int64
	Scope       *string
}

// AdminSetCommissionConfig upsert 给出的分销参数；有改动才写 commission.config_changed 审计。
func (s *Service) AdminSetCommissionConfig(ctx context.Context, tenantID string, actorID *string,
	in CommissionConfigInput) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		set := func(key string, val any) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, $2, to_jsonb($3::bigint))
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, key, val)
			return err
		}
		changed := map[string]any{}
		if in.Scope != nil {
			// 计佣范围是字符串，不能走上面按 bigint 写的 set
			if _, err := tx.Exec(ctx, `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, 'commission.scope', to_jsonb($2::text))
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, *in.Scope); err != nil {
				return err
			}
			changed["scope"] = *in.Scope
		}
		if in.RatePercent != nil {
			if err := set("commission.rate_percent", int64(*in.RatePercent)); err != nil {
				return err
			}
			changed["rate_percent"] = *in.RatePercent
		}
		if in.FreezeDays != nil {
			if err := set("commission.freeze_days", int64(*in.FreezeDays)); err != nil {
				return err
			}
			changed["freeze_days"] = *in.FreezeDays
		}
		if in.MinWithdraw != nil {
			if err := set("commission.min_withdraw", *in.MinWithdraw); err != nil {
				return err
			}
			changed["min_withdraw"] = *in.MinWithdraw
		}
		if len(changed) == 0 {
			return nil
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "commission.config_changed", ResourceType: "system_settings",
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx), AfterDigest: changed,
		})
	})
	return err
}
