// [INPUT]: 依赖 domain/billing 的提现打款记账、佣金口径与 CommissionScope* / ValidCommissionScope / CommissionDefault*，读写 commission_entries / withdrawals / referrals / system_settings，依赖 platform 的 db/httpx/audit
// [OUTPUT]: 对包内提供提现列表、审批、打款、分销总览、分销参数与余额调整处理器
// [POS]: api/admin 后台-06 佣金与提现的 HTTP 外壳与读模型：总览带累计佣金、邀请注册数与计佣范围；分销参数的计佣范围以字符串 jsonb 存进 system_settings
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 提现审批与打款。
//
// 三步：申请 → 审批 → 打款。只有最后一步动账本。
//
// 为什么审批不动账：审批只是一个人点了同意，钱还在平台账上。
// 如果审批时就记「钱出去了」，那么审批完到实际转账之间的这段时间里，
// 账本会显示一笔并不存在的支出 —— 而这段时间可能长达几天。

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listWithdrawals 返回提现申请列表。
//
// 收款信息在这里解密。管理员不解密就没法打款 —— 这是这份数据
// 存在的唯一理由，也是它必须加密存储的理由。
func (h *handlers) listWithdrawals(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	status := r.URL.Query().Get("status")

	type row struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		UserID    string `json:"user_id"`
		Amount    int64  `json:"amount"`
		Currency  string `json:"currency"`
		Status    string `json:"status"`
		Payout    string `json:"payout_detail"`
		Reject    string `json:"reject_reason"`
		Requested any    `json:"requested_at"`
		Completed any    `json:"completed_at"`
		// Earned 是这个用户累计赚到的佣金，用来判断提现是否合理：
		// 提现额远大于历史佣金说明哪里不对
		Earned int64 `json:"earned_total"`
	}
	out := []row{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
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
			var rw row
			var enc []byte
			if err := rows.Scan(&rw.ID, &rw.Email, &rw.UserID, &rw.Amount, &rw.Currency,
				&rw.Status, &enc, &rw.Reject, &rw.Requested, &rw.Completed,
				&rw.Earned); err != nil {
				return err
			}
			rw.Payout = h.decryptWith(enc, "payout")
			out = append(out, rw)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"withdrawals": out})
}

// reviewWithdrawal 批准或拒绝一笔提现。
func (h *handlers) reviewWithdrawal(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	var req struct {
		Action string `json:"action"` // approve / reject
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Reason = normalizeWithdrawalReason(req.Reason)
	if req.Action != "approve" && req.Action != "reject" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"操作只能是 approve 或 reject"))
		return
	}
	if req.Action == "reject" && req.Reason == "" {
		// 拒绝必须给理由：用户会来问，而「不知道为什么被拒」
		// 是最容易升级成工单和差评的一类回复
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "拒绝时必须填写理由"}))
		return
	}

	if req.Action == "approve" && req.Reason != "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"reason": "批准时不能填写拒绝理由"}))
		return
	}

	newStatus := "approved"
	if req.Action == "reject" {
		newStatus = "rejected"
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 只有还在待处理状态的申请可以审批。已打款的再被「拒绝」
		// 会让钱既出去了又记成拒绝
		tag, err := tx.Exec(r.Context(), `
			UPDATE withdrawals
			   SET status = $3, reject_reason = $4, updated_at = now(),
			       completed_at = CASE WHEN $3 = 'rejected' THEN now() ELSE completed_at END
			 WHERE tenant_id = $1 AND id = $2::uuid
			   AND status IN ('requested','reviewing')`,
			tenantID, id, newStatus, nullIfBlank(req.Reason))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.New(httpx.CodeConflict, "该提现申请已被处理过")
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "withdrawal." + req.Action, ResourceType: "withdrawal", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{"status": newStatus, "reason": req.Reason},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"status": newStatus})
}

// markWithdrawalPaid 记录一笔提现已实际打款。
//
// 这是唯一动账本的一步：钱真的离开平台了。
func (h *handlers) markWithdrawalPaid(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	var req struct {
		Reference string `json:"payout_reference"` // 转账流水号
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Reference = normalizePayoutReference(req.Reference)
	if req.Reference == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"payout_reference": "请填写转账流水号，日后对账要用"}))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var userID, currency string
		var amount int64
		err := tx.QueryRow(r.Context(), `
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

		txnID, err := h.d.Billing.PostWithdrawalPayout(r.Context(), tx, tenantID,
			userID, currency, amount, id)
		if err != nil {
			return err
		}

		tag, err := tx.Exec(r.Context(), `
			UPDATE withdrawals
			   SET status = 'paid', payout_reference = $3, payout_txn_id = $4::uuid,
			       completed_at = now(), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid
			   AND status = 'processing' AND payout_txn_id = $4::uuid`,
			tenantID, id, req.Reference, txnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.New(httpx.CodeConflict, "提现状态已变化，请刷新后重试")
		}

		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "withdrawal.paid", ResourceType: "withdrawal", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{
				"amount": amount, "currency": currency, "reference": req.Reference,
				"ledger_txn": txnID},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"status": "paid"})
}

// Withdrawal free-text is canonicalized before validation and before any
// transaction, audit digest or ledger-backed state transition sees it.
func normalizeWithdrawalReason(value string) string {
	return strings.TrimSpace(value)
}

func normalizePayoutReference(value string) string {
	return strings.TrimSpace(value)
}

// commissionOverview 是分销的整体情况，给管理员看的。
func (h *handlers) commissionOverview(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())

	out := map[string]any{}
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var pending, available, paidOut, thisMonth, totalEarned int64
		var entries, reviewers, invited int
		if err := tx.QueryRow(r.Context(), `
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
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(sum(amount),0) FROM withdrawals
			 WHERE tenant_id = $1 AND status = 'paid'`, tenantID).Scan(&paidOut); err != nil {
			return err
		}

		var rate, freeze int
		var minW int64
		var scope string
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.rate_percent'),$2::int),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.freeze_days'),$3::int),
			       COALESCE((SELECT (value #>> '{}')::bigint FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.min_withdraw'),$4::bigint),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='commission.scope'),'')`,
			// 缺行回退与计提同一组常量（billing.CommissionDefault*）
			tenantID, billing.CommissionDefaultRatePercent, billing.CommissionDefaultFreezeDays,
			billing.CommissionDefaultMinWithdraw).Scan(&rate, &freeze, &minW, &scope); err != nil {
			return err
		}
		// 与计提同一个兜底：没有设置或值不认识都按每笔订单
		if !billing.ValidCommissionScope(scope) {
			scope = billing.CommissionScopeEveryOrder
		}

		var waiting int
		if err := tx.QueryRow(r.Context(), `
			SELECT count(*) FROM withdrawals
			 WHERE tenant_id = $1 AND status IN ('requested','reviewing')`,
			tenantID).Scan(&waiting); err != nil {
			return err
		}

		out = map[string]any{
			"pending": pending, "available": available, "paid_out": paidOut,
			"this_month": thisMonth, "entries": entries,
			"need_review": reviewers, "waiting_withdrawals": waiting,
			"rate_percent": rate, "freeze_days": freeze, "min_withdraw": minW,
			"total_earned": totalEarned, "invited_users": invited, "scope": scope,
		}
		return nil
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// setCommissionConfig 改分销参数。
func (h *handlers) setCommissionConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req struct {
		RatePercent *int    `json:"rate_percent"`
		FreezeDays  *int    `json:"freeze_days"`
		MinWithdraw *int64  `json:"min_withdraw"`
		Scope       *string `json:"scope"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	fields := map[string]string{}
	if req.RatePercent != nil && (*req.RatePercent < 0 || *req.RatePercent > 50) {
		// 上限 50%：再高就不是分销而是送钱了。真要突破得改代码，
		// 这道坎的作用是拦住手滑多打一个零
		fields["rate_percent"] = "佣金比例需在 0 到 50 之间"
	}
	if req.FreezeDays != nil && (*req.FreezeDays < 0 || *req.FreezeDays > 90) {
		fields["freeze_days"] = "冻结天数需在 0 到 90 之间"
	}
	if req.MinWithdraw != nil && *req.MinWithdraw < 0 {
		fields["min_withdraw"] = "最低提现金额不能为负"
	}
	if req.Scope != nil && !billing.ValidCommissionScope(*req.Scope) {
		fields["scope"] = "计佣范围只能是 first_order 或 every_order"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		set := func(key string, val any) error {
			_, err := tx.Exec(r.Context(), `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, $2, to_jsonb($3::bigint))
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, key, val)
			return err
		}
		changed := map[string]any{}
		if req.Scope != nil {
			// 计佣范围是字符串，不能走上面按 bigint 写的 set
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO system_settings (tenant_id, key, value)
				VALUES ($1, 'commission.scope', to_jsonb($2::text))
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
				tenantID, *req.Scope); err != nil {
				return err
			}
			changed["scope"] = *req.Scope
		}
		if req.RatePercent != nil {
			if err := set("commission.rate_percent", int64(*req.RatePercent)); err != nil {
				return err
			}
			changed["rate_percent"] = *req.RatePercent
		}
		if req.FreezeDays != nil {
			if err := set("commission.freeze_days", int64(*req.FreezeDays)); err != nil {
				return err
			}
			changed["freeze_days"] = *req.FreezeDays
		}
		if req.MinWithdraw != nil {
			if err := set("commission.min_withdraw", *req.MinWithdraw); err != nil {
				return err
			}
			changed["min_withdraw"] = *req.MinWithdraw
		}
		if len(changed) == 0 {
			return nil
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "commission.config_changed", ResourceType: "system_settings",
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(r.Context()), AfterDigest: changed,
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

func nullIfBlank(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// adjustBalance 由管理员直接增减用户余额。
//
// 这是整个后台最需要留痕的操作之一：它能凭空给账户加钱。
// 所以理由是必填的，且会连同调整前后的金额一起进审计。
func (h *handlers) adjustBalance(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	userID := chi.URLParam(r, "id")
	var req struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Reason   string `json:"reason"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	var actorID string
	if a := httpx.PrincipalFrom(r.Context()); a != nil {
		actorID = a.UserID
	}

	after, err := h.d.Billing.AdjustBalance(r.Context(), tenantID, billing.AdjustBalanceInput{
		UserID:   userID,
		Amount:   req.Amount,
		Currency: req.Currency,
		Reason:   req.Reason,
		ActorID:  actorID,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"balance": after})
}
