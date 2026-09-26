// [INPUT]: 依赖 revenue_report_adjustments / orders / tenants / users 表，依赖 platform 的 db/httpx/audit
// [OUTPUT]: 对外提供 RevenueTimeseries、RevenueAdjustment 与 List/Create/ReverseRevenueAdjustment
// [POS]: domain/adminops 的收入读模型与收入调整：日界按租户时区，调整追加写、冲销另起反向记录，每条带登记人邮箱
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type RevenuePoint struct {
	Date         string `json:"date"`
	ActualCredit int64  `json:"actual_credit"`
	ActualDebit  int64  `json:"actual_debit"`
	Adjustment   int64  `json:"adjustment"`
	DisplayedNet int64  `json:"displayed_net"`
}

type RevenueAdjustment struct {
	ID          string  `json:"id"`
	Currency    string  `json:"currency"`
	Amount      int64   `json:"amount"`
	Reason      string  `json:"reason"`
	EffectiveOn string  `json:"effective_on"`
	ReversalOf  *string `json:"reversal_of,omitempty"`
	CreatedBy   string  `json:"created_by"`
	// CreatedByEmail 是登记人（列表「登记人 · 时间」）；账号已删除时为空。
	CreatedByEmail *string   `json:"created_by_email"`
	CreatedAt      time.Time `json:"created_at"`
	Reversed       bool      `json:"reversed"`
}

type CreateRevenueAdjustmentInput struct {
	Currency       string
	Amount         int64
	Reason         string
	EffectiveOn    string
	IdempotencyKey string
}

func validateRevenueCurrency(currency string) error {
	if currency != "CNY" && currency != "USD" {
		return httpx.Invalid(map[string]string{"currency": "仅支持 CNY 或 USD"})
	}
	return nil
}

func validateRevenueDays(days int) error {
	if days != 7 && days != 30 && days != 90 {
		return httpx.Invalid(map[string]string{"days": "仅支持 7、30 或 90 天"})
	}
	return nil
}

// RevenueTimeseries 返回按日补齐的收入点，以及紧邻的前一个等长区间的 displayed_net
// 合计（「较上一区间」用）。两者同一个时区、同一份口径。
func (s *Service) RevenueTimeseries(ctx context.Context, tenantID, currency string, days int) ([]RevenuePoint, int64, error) {
	if err := validateRevenueCurrency(currency); err != nil {
		return nil, 0, err
	}
	if err := validateRevenueDays(days); err != nil {
		return nil, 0, err
	}
	points := []RevenuePoint{}
	var previousTotal int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var timezone string
		if err := tx.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenantID).Scan(&timezone); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			WITH params AS (
			  SELECT (now() AT TIME ZONE $4)::date AS today
			), dates AS (
			  SELECT generate_series(p.today - ($3::int - 1), p.today, interval '1 day')::date AS day
			    FROM params p
			), ledger AS (
			  SELECT (lt.occurred_at AT TIME ZONE $4)::date AS day,
			         coalesce(sum(le.amount) FILTER (WHERE le.direction='credit'),0)::bigint AS credit,
			         coalesce(sum(le.amount) FILTER (WHERE le.direction='debit'),0)::bigint AS debit
			    FROM ledger_entries le
			    JOIN ledger_transactions lt ON lt.id=le.transaction_id AND lt.tenant_id=le.tenant_id
			    JOIN ledger_accounts la ON la.id=le.account_id AND la.tenant_id=le.tenant_id
			    CROSS JOIN params p
			   WHERE le.tenant_id=$1 AND le.currency=$2 AND la.account_type='platform_revenue'
			     AND (lt.occurred_at AT TIME ZONE $4)::date >= p.today - ($3::int - 1)
			   GROUP BY (lt.occurred_at AT TIME ZONE $4)::date
			), adjustments AS (
			  SELECT effective_on AS day, sum(amount)::bigint AS amount
			    FROM revenue_report_adjustments, params p
			   WHERE tenant_id=$1 AND currency=$2
			     AND effective_on >= p.today - ($3::int - 1)
			   GROUP BY effective_on
			)
			SELECT to_char(d.day,'YYYY-MM-DD'), coalesce(l.credit,0), coalesce(l.debit,0),
			       coalesce(a.amount,0), coalesce(l.credit,0)-coalesce(l.debit,0)+coalesce(a.amount,0)
			  FROM dates d LEFT JOIN ledger l ON l.day=d.day LEFT JOIN adjustments a ON a.day=d.day
			 ORDER BY d.day`, tenantID, currency, days, timezone)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p RevenuePoint
			if err := rows.Scan(&p.Date, &p.ActualCredit, &p.ActualDebit, &p.Adjustment, &p.DisplayedNet); err != nil {
				return err
			}
			points = append(points, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// 前一个等长区间：[today-2d+1, today-d]
		return tx.QueryRow(ctx, `
			WITH params AS (
			  SELECT (now() AT TIME ZONE $4)::date - $3::int AS last_day
			)
			SELECT coalesce((SELECT sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
			                   FROM ledger_entries le
			                   JOIN ledger_transactions lt ON lt.id=le.transaction_id AND lt.tenant_id=le.tenant_id
			                   JOIN ledger_accounts la ON la.id=le.account_id AND la.tenant_id=le.tenant_id
			                  WHERE le.tenant_id=$1 AND le.currency=$2 AND la.account_type='platform_revenue'
			                    AND (lt.occurred_at AT TIME ZONE $4)::date BETWEEN p.last_day - ($3::int - 1) AND p.last_day), 0)::bigint
			     + coalesce((SELECT sum(a.amount) FROM revenue_report_adjustments a
			                  WHERE a.tenant_id=$1 AND a.currency=$2
			                    AND a.effective_on BETWEEN p.last_day - ($3::int - 1) AND p.last_day), 0)::bigint
			  FROM params p`, tenantID, currency, days, timezone).Scan(&previousTotal)
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}
	return points, previousTotal, nil
}

func (s *Service) ListRevenueAdjustments(ctx context.Context, tenantID, currency string) ([]RevenueAdjustment, error) {
	if currency != "" {
		if err := validateRevenueCurrency(currency); err != nil {
			return nil, err
		}
	}
	out := []RevenueAdjustment{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id,a.currency,a.amount,a.reason,to_char(a.effective_on,'YYYY-MM-DD'),
		       a.reversal_of,a.created_by,(SELECT u.email FROM users u WHERE u.tenant_id=a.tenant_id AND u.id=a.created_by),a.created_at,exists(SELECT 1 FROM revenue_report_adjustments r WHERE r.tenant_id=a.tenant_id AND r.reversal_of=a.id)
		  FROM revenue_report_adjustments a WHERE a.tenant_id=$1 AND ($2='' OR a.currency=$2)
		 ORDER BY a.created_at DESC LIMIT 200`, tenantID, currency)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a RevenueAdjustment
			if err := rows.Scan(&a.ID, &a.Currency, &a.Amount, &a.Reason, &a.EffectiveOn, &a.ReversalOf, &a.CreatedBy, &a.CreatedByEmail, &a.CreatedAt, &a.Reversed); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

func (s *Service) CreateRevenueAdjustment(ctx context.Context, tenantID, actorID string, in CreateRevenueAdjustmentInput) (*RevenueAdjustment, error) {
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.Reason = strings.TrimSpace(in.Reason)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	fields := map[string]string{}
	if err := validateRevenueCurrency(in.Currency); err != nil {
		fields["currency"] = "仅支持 CNY 或 USD"
	}
	if in.Amount == 0 || in.Amount < -1_000_000_000_000 || in.Amount > 1_000_000_000_000 {
		fields["amount"] = "调整金额必须非零且绝对值不超过 100 亿"
	}
	if len([]rune(in.Reason)) < 5 || len([]rune(in.Reason)) > 500 {
		fields["reason"] = "理由需为 5–500 个字符"
	}
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 255 {
		fields["idempotency_key"] = "缺少或无效的幂等键"
	}
	var requestedDay *time.Time
	if in.EffectiveOn != "" {
		day, err := time.Parse("2006-01-02", in.EffectiveOn)
		if err != nil {
			fields["effective_on"] = "日期格式应为 YYYY-MM-DD"
		} else {
			requestedDay = &day
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}

	var out RevenueAdjustment
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var tenantToday time.Time
		if err := tx.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date FROM tenants WHERE id=$1`, tenantID).Scan(&tenantToday); err != nil {
			return err
		}
		day := tenantToday
		if requestedDay != nil {
			day = *requestedDay
		}
		if day.After(tenantToday) {
			return httpx.Invalid(map[string]string{"effective_on": "生效日期不能晚于租户今天"})
		}

		err := tx.QueryRow(ctx, `SELECT id,currency,amount,reason,to_char(effective_on,'YYYY-MM-DD'),
		       reversal_of,created_by,(SELECT u.email FROM users u WHERE u.tenant_id=revenue_report_adjustments.tenant_id AND u.id=revenue_report_adjustments.created_by),created_at FROM revenue_report_adjustments
		 WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID, in.IdempotencyKey).
			Scan(&out.ID, &out.Currency, &out.Amount, &out.Reason, &out.EffectiveOn,
				&out.ReversalOf, &out.CreatedBy, &out.CreatedByEmail, &out.CreatedAt)
		if err == nil {
			if out.Currency != in.Currency || out.Amount != in.Amount || out.Reason != in.Reason || out.EffectiveOn != day.Format("2006-01-02") || out.ReversalOf != nil {
				return httpx.New(httpx.CodeIdempotencyReuse, "该幂等键已用于不同的报表调整")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if err := tx.QueryRow(ctx, `INSERT INTO revenue_report_adjustments
		 (tenant_id,currency,amount,reason,effective_on,created_by,idempotency_key)
		 VALUES($1,$2,$3,$4,$5,$6,$7)
		 RETURNING id,currency,amount,reason,to_char(effective_on,'YYYY-MM-DD'),created_by,(SELECT u.email FROM users u WHERE u.tenant_id=revenue_report_adjustments.tenant_id AND u.id=revenue_report_adjustments.created_by),created_at`,
			tenantID, in.Currency, in.Amount, in.Reason, day, actorID, in.IdempotencyKey).
			Scan(&out.ID, &out.Currency, &out.Amount, &out.Reason, &out.EffectiveOn, &out.CreatedBy, &out.CreatedByEmail, &out.CreatedAt); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "revenue.adjustment.create", ResourceType: "revenue_report_adjustment", ResourceID: &out.ID, APIDomain: "admin", Outcome: "success", AfterDigest: map[string]any{"currency": out.Currency, "amount": out.Amount, "effective_on": out.EffectiveOn, "reason": out.Reason}, RequestID: httpx.RequestIDFrom(ctx)})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}

func (s *Service) ReverseRevenueAdjustment(ctx context.Context, tenantID, actorID, id, reason, idempotencyKey string) (*RevenueAdjustment, error) {
	reason = strings.TrimSpace(reason)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	fields := map[string]string{}
	if len([]rune(reason)) < 5 || len([]rune(reason)) > 500 {
		fields["reason"] = "撤销理由需为 5–500 个字符"
	}
	if idempotencyKey == "" || len(idempotencyKey) > 255 {
		fields["idempotency_key"] = "缺少或无效的幂等键"
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}

	var out RevenueAdjustment
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id,currency,amount,reason,to_char(effective_on,'YYYY-MM-DD'),
		       reversal_of,created_by,(SELECT u.email FROM users u WHERE u.tenant_id=revenue_report_adjustments.tenant_id AND u.id=revenue_report_adjustments.created_by),created_at FROM revenue_report_adjustments
		 WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID, idempotencyKey).
			Scan(&out.ID, &out.Currency, &out.Amount, &out.Reason, &out.EffectiveOn,
				&out.ReversalOf, &out.CreatedBy, &out.CreatedByEmail, &out.CreatedAt)
		if err == nil {
			if out.ReversalOf == nil || *out.ReversalOf != id || out.Reason != reason {
				return httpx.New(httpx.CodeIdempotencyReuse, "该幂等键已用于不同的撤销操作")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// The reporting ledger is append-only for the application role, so row
		// locks (SELECT ... FOR UPDATE) are intentionally unavailable. Serialize
		// reversals with a transaction-scoped advisory lock instead; the partial
		// unique index on reversal_of remains the final integrity guard.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`, tenantID, id); err != nil {
			return err
		}

		var currency, originalReason, effective string
		var amount int64
		var reversalOf *string
		err = tx.QueryRow(ctx, `SELECT currency,amount,reason,to_char(effective_on,'YYYY-MM-DD'),reversal_of
		 FROM revenue_report_adjustments WHERE tenant_id=$1 AND id=$2`, tenantID, id).
			Scan(&currency, &amount, &originalReason, &effective, &reversalOf)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if reversalOf != nil {
			return httpx.New(httpx.CodeConflict, "反向记录不能再次撤销")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT exists(SELECT 1 FROM revenue_report_adjustments WHERE tenant_id=$1 AND reversal_of=$2)`, tenantID, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return httpx.New(httpx.CodeConflict, "该调整已撤销")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO revenue_report_adjustments
		 (tenant_id,currency,amount,reason,effective_on,reversal_of,created_by,idempotency_key)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		 RETURNING id,currency,amount,reason,to_char(effective_on,'YYYY-MM-DD'),reversal_of,created_by,(SELECT u.email FROM users u WHERE u.tenant_id=revenue_report_adjustments.tenant_id AND u.id=revenue_report_adjustments.created_by),created_at`,
			tenantID, currency, -amount, reason, effective, id, actorID, idempotencyKey).
			Scan(&out.ID, &out.Currency, &out.Amount, &out.Reason, &out.EffectiveOn, &out.ReversalOf, &out.CreatedBy, &out.CreatedByEmail, &out.CreatedAt); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "revenue.adjustment.reverse", ResourceType: "revenue_report_adjustment", ResourceID: &out.ID, APIDomain: "admin", Outcome: "success", BeforeDigest: map[string]any{"original_id": id, "currency": currency, "amount": amount, "reason": originalReason}, AfterDigest: map[string]any{"reversal_id": out.ID, "amount": out.Amount, "reason": out.Reason}, RequestID: httpx.RequestIDFrom(ctx)})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}
