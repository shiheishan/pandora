// [INPUT]: 依赖 platform/db 的租户事务、platform/crypto 的 HashToken（订阅令牌反查）、platform/httpx 的错误模型；读 users / user_groups / subscriptions / quota_balances / subscription_online_devices / orders / referrals / telegram_bindings；订单行复用 orderRowSelectSQL
// [OUTPUT]: 对外提供 UserRow、UserCurrentSub、ListUsersInput、UserDetail、SubscriptionRow、QuotaRow、UserStats、UserRef、TelegramRef 与 Service.ListUsers / GetUser
// [POS]: domain/adminops 的后台用户读模型（契约后台-03 GET v1/users 与 GET v1/users/{id}）：从 service.go 拆出，列表带当前订阅摘要与多条件筛选，详情带配额、设备、统计（实收按币种拆开）、邀请人与 Telegram
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// liveSubscriptionStatusesSQL 是「还在用」的订阅状态：用户列表的 sub_state=active、
// 当前订阅的挑选都以它为准。
const liveSubscriptionStatusesSQL = `('active','trialing','grace','past_due')`

// currentSubscriptionSQL 是用户 u「当前订阅」某一列的标量子查询：优先还在用的，
// 其次到期最晚、最近创建的。用户列表的当前订阅摘要与批量筛选（套餐、到期）
// 用同一个挑法，列表里看到的套餐就是筛选按的套餐。
func currentSubscriptionSQL(col string) string {
	return `(SELECT s.` + col + ` FROM subscriptions s
	          WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id
	          ORDER BY (s.status IN ` + liveSubscriptionStatusesSQL + `) DESC,
	                   s.current_period_end DESC NULLS LAST, s.created_at DESC
	          LIMIT 1)`
}

// subStateSQL 是 sub_state 筛选（active / expired / none）的条件，p 是承载取值的参数占位符。
// expired 指有过订阅、但没有一条还在用。
func subStateSQL(p string) string {
	hasAny := `EXISTS (SELECT 1 FROM subscriptions s WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id)`
	live := `EXISTS (SELECT 1 FROM subscriptions s WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id
	           AND s.status IN ` + liveSubscriptionStatusesSQL + `)`
	return `((` + p + ` = 'active' AND ` + live + `) OR (` + p + ` = 'expired' AND ` + hasAny + ` AND NOT ` + live +
		`) OR (` + p + ` = 'none' AND NOT ` + hasAny + `))`
}

func validSubState(v string) bool {
	switch v {
	case "", "active", "expired", "none":
		return true
	}
	return false
}

type UserRow struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"`
	Status      string  `json:"status"`
	RiskLevel   string  `json:"risk_level"`
	// GroupName 决定这个用户能看到哪些套餐、能用哪些券、按什么价买
	GroupName   string     `json:"group_name"`
	GroupID     *string    `json:"group_id"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
	SubCount    int        `json:"subscription_count"`
	ActiveSub   *string    `json:"active_plan"`
	Balance     int64      `json:"balance"`
	Currency    string     `json:"currency"`
	// CurrentSubscription 只在列表里有值：优先取还在用的订阅，没有则取最近一条
	CurrentSubscription *UserCurrentSub `json:"current_subscription"`
}

type UserCurrentSub struct {
	ID               string     `json:"id"`
	PlanName         string     `json:"plan_name"`
	Status           string     `json:"status"`
	CurrentPeriodEnd *time.Time `json:"current_period_end"`
	Traffic          struct {
		Limit    *int64 `json:"limit"`
		Consumed int64  `json:"consumed"`
	} `json:"traffic"`
	// DeviceLimit 是生效值：订阅覆盖 → 套餐版本上限 → 0（不限）
	DeviceLimit   int `json:"device_limit"`
	OnlineDevices int `json:"online_devices"`
}

type ListUsersInput struct {
	Query string
	// Status 逗号分隔，等值匹配
	Status string
	// GroupID 为 uuid 或 "none"（未分组）
	GroupID string
	// SubState 为 active / expired / none
	SubState string
	Limit    int
	Offset   int
}

func (s *Service) ListUsers(ctx context.Context, tenantID string, in ListUsersInput) ([]UserRow, int64, error) {
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 25
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	fields := map[string]string{}
	in.GroupID = strings.TrimSpace(in.GroupID)
	if in.GroupID != "" && in.GroupID != "none" {
		if _, err := uuid.Parse(in.GroupID); err != nil {
			fields["group_id"] = "用户组必须是 uuid 或 none"
		}
	}
	if !validSubState(in.SubState) {
		fields["sub_state"] = "订阅状态只能是 active、expired 或 none"
	}
	if len(fields) > 0 {
		return nil, 0, httpx.Invalid(fields)
	}

	// q 依次按：邮箱 / 显示名片段、用户 id 精确、订阅令牌反查（与发放时同一个哈希，
	// 只比哈希，不把订阅地址暴露给管理员）。粘贴整条订阅地址时取最后一段
	query := strings.TrimSpace(in.Query)
	var pattern, exactID string
	var tokenHash []byte
	if query != "" {
		pattern = "%" + strings.ToLower(query) + "%"
		exactID = strings.ToLower(query)
		token := query
		if i := strings.LastIndex(token, "/"); i >= 0 {
			token = token[i+1:]
		}
		if i := strings.IndexByte(token, '?'); i >= 0 {
			token = token[:i]
		}
		tokenHash = crypto.HashToken(token)
	}
	statuses := []string{}
	for _, st := range strings.Split(in.Status, ",") {
		if st = strings.TrimSpace(st); st != "" {
			statuses = append(statuses, st)
		}
	}

	where := `u.tenant_id = $1
		AND ($2 = '' OR lower(u.email) LIKE $2 OR lower(coalesce(u.display_name,'')) LIKE $2
		     OR u.id::text = $3
		     OR EXISTS (SELECT 1 FROM subscription_credentials sc
		                 WHERE sc.tenant_id = u.tenant_id AND sc.user_id = u.id
		                   AND sc.token_hash = $4 AND sc.status IN ('active','grace')))
		AND (cardinality($5::text[]) = 0 OR u.status::text = ANY($5::text[]))
		AND ($6 = '' OR ($6 = 'none' AND u.user_group_id IS NULL) OR u.user_group_id::text = $6)
		AND ($7 = '' OR ` + subStateSQL("$7") + `)`
	args := []any{tenantID, pattern, exactID, tokenHash, statuses, in.GroupID, in.SubState}

	out := []UserRow{}
	var total int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
			       u.created_at, u.last_login_at,
			       (SELECT count(*) FROM subscriptions s WHERE s.user_id = u.id),
			       (SELECT pl.name FROM subscriptions s
			          JOIN plans pl ON pl.id = s.plan_id
			         WHERE s.user_id = u.id AND s.status IN ('active','trialing')
			         ORDER BY s.created_at DESC LIMIT 1),
			       coalesce((SELECT -la.balance_signed FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 0),
			       coalesce((SELECT la.currency FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 'CNY'),
			       coalesce(g.name, ''), u.user_group_id::text,
			       cs.id::text, cs.plan_name, cs.status, cs.current_period_end,
			       tq.limit_value, coalesce(tq.consumed, 0), coalesce(cs.device_limit, 0),
			       coalesce(cs.online_devices, 0)
			  FROM users u
			  LEFT JOIN user_groups g ON g.tenant_id = u.tenant_id AND g.id = u.user_group_id
			  LEFT JOIN LATERAL (
			        SELECT s.id, pl.name AS plan_name, s.status, s.current_period_end,
			               coalesce(s.device_limit, pv.max_devices, 0) AS device_limit,
			               coalesce(od.device_count, 0)::int AS online_devices
			          FROM subscriptions s
			          JOIN plans pl ON pl.id = s.plan_id
			          LEFT JOIN plan_versions pv ON pv.id = s.plan_version_id
			          LEFT JOIN subscription_online_devices od
			                 ON od.tenant_id = s.tenant_id AND od.subscription_id = s.id
			         WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id
			         ORDER BY (s.status IN `+liveSubscriptionStatusesSQL+`) DESC,
			                  s.current_period_end DESC NULLS LAST, s.created_at DESC
			         LIMIT 1) cs ON true
			  LEFT JOIN LATERAL (
			        SELECT q.limit_value, q.consumed FROM quota_balances q
			         WHERE q.tenant_id = u.tenant_id AND q.subscription_id = cs.id
			           AND q.metric = 'traffic.bytes'
			         ORDER BY q.period_start DESC LIMIT 1) tq ON true
			 WHERE `+where+`
			 ORDER BY u.created_at DESC
			 LIMIT $8 OFFSET $9`, append(args, in.Limit, in.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r UserRow
			var subID, planName, subStatus *string
			var periodEnd *time.Time
			var cur UserCurrentSub
			if err := rows.Scan(&r.ID, &r.Email, &r.DisplayName, &r.Status, &r.RiskLevel,
				&r.CreatedAt, &r.LastLoginAt, &r.SubCount, &r.ActiveSub,
				&r.Balance, &r.Currency, &r.GroupName, &r.GroupID,
				&subID, &planName, &subStatus, &periodEnd,
				&cur.Traffic.Limit, &cur.Traffic.Consumed, &cur.DeviceLimit, &cur.OnlineDevices); err != nil {
				return err
			}
			if subID != nil {
				cur.ID, cur.PlanName, cur.Status, cur.CurrentPeriodEnd = *subID, *planName, *subStatus, periodEnd
				r.CurrentSubscription = &cur
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}
	return out, total, nil
}

type UserDetail struct {
	UserRow
	EmailVerified bool              `json:"email_verified"`
	Subscriptions []SubscriptionRow `json:"subscriptions"`
	Orders        []OrderRow        `json:"recent_orders"`
	Roles         []string          `json:"roles"`
	Stats         UserStats         `json:"stats"`
	Referrer      *UserRef          `json:"referrer"`
	Telegram      *TelegramRef      `json:"telegram"`
}

type SubscriptionRow struct {
	ID                 string     `json:"id"`
	PlanName           string     `json:"plan_name"`
	PlanVersion        int        `json:"plan_version"`
	Status             string     `json:"status"`
	CurrentPeriodStart *time.Time `json:"current_period_start"`
	PeriodEnd          *time.Time `json:"current_period_end"`
	Amount             int64      `json:"amount"`
	Currency           string     `json:"currency"`
	AutoRenew          bool       `json:"auto_renew"`
	// Quotas 与门户 v1/me/subscriptions 同形
	Quotas              []QuotaRow `json:"quotas"`
	DeviceLimitOverride *int       `json:"device_limit_override"`
	PlanMaxDevices      *int       `json:"plan_max_devices"`
	OnlineDevices       int        `json:"online_devices"`
}

type QuotaRow struct {
	Metric    string `json:"metric"`
	Limit     *int64 `json:"limit"`
	Consumed  int64  `json:"consumed"`
	Remaining *int64 `json:"remaining"`
}

type UserStats struct {
	// PaidTotal 与导出同一口径：paid / fulfilled 订单的 paid_amount 之和，跨币种直接相加；
	// 只为不打断已上线的前端而保留，按币种显示改读 PaidTotals（R80）
	PaidTotal int64 `json:"paid_total"`
	// PaidTotals 是同一口径按币种拆开，币种升序，没有实收时为空数组；元素复用 dashboard_tasks.go 的 CurrencyAmount
	PaidTotals    []CurrencyAmount `json:"paid_totals"`
	OrderCount    int              `json:"order_count"`
	ReferralCount int              `json:"referral_count"`
}

type UserRef struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type TelegramRef struct {
	Username string    `json:"username"`
	BoundAt  time.Time `json:"bound_at"`
}

func (s *Service) GetUser(ctx context.Context, tenantID, userID string) (*UserDetail, error) {
	// 非 uuid 的 id 与不存在同样 404：交给 SQL 会变成 500
	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var d UserDetail

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var verifiedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
			       u.created_at, u.last_login_at, u.email_verified_at,
			       coalesce((SELECT -la.balance_signed FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 0),
			       coalesce((SELECT la.currency FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 'CNY'),
			       coalesce((SELECT g.name FROM user_groups g
			                  WHERE g.id = u.user_group_id), ''),
			       u.user_group_id::text,
			       coalesce((SELECT sum(o.paid_amount) FROM orders o WHERE o.tenant_id = u.tenant_id
			                  AND o.user_id = u.id AND o.status IN ('paid','fulfilled')), 0)::bigint,
			       (SELECT count(*) FROM orders o WHERE o.tenant_id = u.tenant_id AND o.user_id = u.id)::int,
			       (SELECT count(*) FROM referrals rf WHERE rf.tenant_id = u.tenant_id
			                  AND rf.referrer_user_id = u.id)::int
			  FROM users u WHERE u.tenant_id = $1 AND u.id = $2`,
			tenantID, userID,
		).Scan(&d.ID, &d.Email, &d.DisplayName, &d.Status, &d.RiskLevel,
			&d.CreatedAt, &d.LastLoginAt, &verifiedAt, &d.Balance, &d.Currency,
			&d.GroupName, &d.GroupID, &d.Stats.PaidTotal, &d.Stats.OrderCount, &d.Stats.ReferralCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		d.EmailVerified = verifiedAt != nil

		rows, err := tx.Query(ctx, `
			SELECT currency, sum(paid_amount)::bigint FROM orders
			 WHERE tenant_id = $1 AND user_id = $2 AND status IN ('paid','fulfilled')
			 GROUP BY currency HAVING sum(paid_amount) > 0
			 ORDER BY currency`, tenantID, userID)
		if err != nil {
			return err
		}
		d.Stats.PaidTotals, err = pgx.CollectRows(rows, pgx.RowToStructByPos[CurrencyAmount])
		if err != nil {
			return err
		}
		if d.Stats.PaidTotals == nil {
			d.Stats.PaidTotals = []CurrencyAmount{}
		}

		var ref UserRef
		switch err := tx.QueryRow(ctx, `
			SELECT r.id::text, r.email::text FROM referrals rf
			  JOIN users r ON r.tenant_id = rf.tenant_id AND r.id = rf.referrer_user_id
			 WHERE rf.tenant_id = $1 AND rf.referee_user_id = $2`, tenantID, userID).Scan(&ref.ID, &ref.Email); {
		case err == nil:
			d.Referrer = &ref
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var tg TelegramRef
		switch err := tx.QueryRow(ctx, `
			SELECT username, bound_at FROM telegram_bindings
			 WHERE tenant_id = $1 AND user_id = $2
			 ORDER BY bound_at DESC LIMIT 1`, tenantID, userID).Scan(&tg.Username, &tg.BoundAt); {
		case err == nil:
			d.Telegram = &tg
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		d.Subscriptions = []SubscriptionRow{}
		srows, err := tx.Query(ctx, `
			SELECT s.id, pl.name, pv.version, s.status, s.current_period_start, s.current_period_end,
			       s.snapshot_amount, s.snapshot_currency, s.auto_renew,
			       s.device_limit, pv.max_devices, coalesce(od.device_count, 0)::int
			  FROM subscriptions s
			  JOIN plans pl ON pl.id = s.plan_id
			  JOIN plan_versions pv ON pv.id = s.plan_version_id
			  LEFT JOIN subscription_online_devices od
			         ON od.tenant_id = s.tenant_id AND od.subscription_id = s.id
			 WHERE s.tenant_id = $1 AND s.user_id = $2
			 ORDER BY s.created_at DESC`, tenantID, userID)
		if err != nil {
			return err
		}
		for srows.Next() {
			r := SubscriptionRow{Quotas: []QuotaRow{}}
			if err := srows.Scan(&r.ID, &r.PlanName, &r.PlanVersion, &r.Status,
				&r.CurrentPeriodStart, &r.PeriodEnd, &r.Amount, &r.Currency, &r.AutoRenew,
				&r.DeviceLimitOverride, &r.PlanMaxDevices, &r.OnlineDevices); err != nil {
				srows.Close()
				return err
			}
			d.Subscriptions = append(d.Subscriptions, r)
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}
		for i := range d.Subscriptions {
			qrows, err := tx.Query(ctx, `
				SELECT metric, limit_value, consumed, remaining FROM quota_balances
				 WHERE tenant_id = $1 AND subscription_id = $2
				 ORDER BY metric, period_start DESC`, tenantID, d.Subscriptions[i].ID)
			if err != nil {
				return err
			}
			quotas, err := pgx.CollectRows(qrows, pgx.RowToStructByPos[QuotaRow])
			if err != nil {
				return err
			}
			d.Subscriptions[i].Quotas = append(d.Subscriptions[i].Quotas, quotas...)
		}

		d.Orders = []OrderRow{}
		// 与订单列表同一份查询：原先这里自己写了一遍 SELECT，漏了首项快照，
		// plan_name / interval / interval_count / item_count 恒为零值（缺陷 9）
		orows, err := tx.Query(ctx, orderRowSelectSQL+`
			 WHERE o.tenant_id = $1 AND o.user_id = $2
			 ORDER BY o.created_at DESC LIMIT 20`, tenantID, userID)
		if err != nil {
			return err
		}
		for orows.Next() {
			r, err := scanOrderRow(orows)
			if err != nil {
				orows.Close()
				return err
			}
			d.Orders = append(d.Orders, r)
		}
		orows.Close()
		if err := orows.Err(); err != nil {
			return err
		}

		d.Roles = []string{}
		rrows, err := tx.Query(ctx, `
			SELECT r.code FROM role_bindings rb JOIN roles r ON r.id = rb.role_id
			 WHERE rb.tenant_id = $1 AND rb.user_id = $2
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			 ORDER BY r.code`, tenantID, userID)
		if err != nil {
			return err
		}
		roles, err := pgx.CollectRows(rrows, pgx.RowTo[string])
		d.Roles = append(d.Roles, roles...)
		return err
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return &d, nil
}
