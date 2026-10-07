package adminops

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// currentSubscriptionSQL 是用户 u「当前订阅」某一列的标量子查询，口径在
// subscription.CurrentSQL（R118）。用户列表的当前订阅摘要与批量筛选（套餐、到期）
// 用同一个挑法，列表里看到的套餐就是筛选按的套餐。
func currentSubscriptionSQL(col string) string {
	return subscription.CurrentSQL("u.tenant_id", "u.id", col)
}

// hasLiveSubscriptionSQL 是用户 u「存在在用订阅」的条件：sub_state=active 与
// 批量运营的 has_active_sub=true 同义。
var hasLiveSubscriptionSQL = subscription.HasLiveSQL("u.tenant_id", "u.id")

// subStateSQL 是 sub_state 筛选（active / expired / none）的条件，p 是承载取值的参数占位符。
// expired 指有过订阅、但没有一条还在用。
func subStateSQL(p string) string {
	hasAny := `EXISTS (SELECT 1 FROM subscriptions s WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id)`
	live := hasLiveSubscriptionSQL
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

// onlineSinceSQL 是「在线」窗口的起点：与在线设备视图 subscription_online_devices（00094）
// 同一个窗口来源 app.device_limit_window_minutes，按租户读一次。调用方把它放进一个
// MATERIALIZED CTE，整条查询只算一次。
func onlineSinceSQL(tenantExpr string) string {
	return `now() - make_interval(mins => app.device_limit_window_minutes(` + tenantExpr + `))`
}

// onlineDevicesSQL 是一条订阅当前在线设备数的子查询（列 device_count）：窗口内上报过的
// 去重 IP 哈希，跨节点汇总，与视图 subscription_online_devices 的 device_count 同一口径。
// 只按这一条订阅走 idx_node_alive_recent (subscription_id, last_seen_at)，不再让视图对
// 全站在线记录先 GROUP BY 再连。subExpr 为 NULL 时计数为 0。
func onlineDevicesSQL(tenantExpr, subExpr, sinceExpr string) string {
	return `SELECT count(DISTINCT a.ip_hash)::int AS device_count
	          FROM node_alive_ips a
	         WHERE a.tenant_id = ` + tenantExpr + ` AND a.subscription_id = ` + subExpr + `
	           AND a.last_seen_at > ` + sinceExpr
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

// listUsersArgs 校验并规整列表输入，返回规整后的输入与 listUsersWhereSQL 的 $1–$7。
func listUsersArgs(tenantID string, in ListUsersInput) (ListUsersInput, []any, error) {
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
		return in, nil, httpx.Invalid(fields)
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
	return in, []any{tenantID, pattern, exactID, tokenHash, statuses, in.GroupID, in.SubState}, nil
}

// listUsersWhereSQL 是用户列表的筛选（参数见 listUsersArgs），总数与取页共用。
//
// 令牌反查写成不相关子查询：整条查询只按哈希查一次凭据（唯一索引），而不是每个
// 用户各探一次。u.tenant_id = $1 已在最前，与按 u.tenant_id 关联等价。
var listUsersWhereSQL = `u.tenant_id = $1
		AND ($2 = '' OR lower(u.email) LIKE $2 OR lower(coalesce(u.display_name,'')) LIKE $2
		     OR u.id::text = $3
		     OR u.id IN (SELECT sc.user_id FROM subscription_credentials sc
		                  WHERE sc.tenant_id = $1
		                    AND sc.token_hash = $4 AND sc.status IN ('active','grace')))
		AND (cardinality($5::text[]) = 0 OR u.status::text = ANY($5::text[]))
		AND ($6 = '' OR ($6 = 'none' AND u.user_group_id IS NULL) OR u.user_group_id::text = $6)
		AND ($7 = '' OR ` + subStateSQL("$7") + `)`

// listUsersPageSQL 先按筛选与排序只取一页 id（page，走 00100 的
// (tenant_id, created_at DESC, id DESC) 索引），再只对这一页拼当前订阅、配额、余额与
// 在线设备。原来这些 LATERAL 挂在 Sort / LIMIT 之下，要对全部用户算完才取 25 条
// （5k 用户 470s）。同一时刻批量建的用户按 id 定序，翻页不重不漏。$8 / $9 是 LIMIT / OFFSET。
var listUsersPageSQL = `
	WITH page AS MATERIALIZED (
	  SELECT u.id, u.created_at
	    FROM users u
	   WHERE ` + listUsersWhereSQL + `
	   ORDER BY u.created_at DESC, u.id DESC
	   LIMIT $8 OFFSET $9
	), win AS MATERIALIZED (
	  SELECT ` + onlineSinceSQL("$1") + ` AS since
	)
	SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
	       u.created_at, u.last_login_at,
	       (SELECT count(*) FROM subscriptions s WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id),
	       CASE WHEN cs.status IN ` + subscription.LiveStatusesSQL + ` THEN cs.plan_name END,
	       coalesce(-bal.balance_signed, 0), coalesce(bal.currency, 'CNY'),
	       coalesce(g.name, ''), u.user_group_id::text,
	       cs.id::text, cs.plan_name, cs.status, cs.current_period_end,
	       tq.limit_value, coalesce(tq.consumed, 0), coalesce(cs.device_limit, 0),
	       coalesce(od.device_count, 0)
	  FROM page p
	  JOIN users u ON u.tenant_id = $1 AND u.id = p.id
	  CROSS JOIN win
	  LEFT JOIN user_groups g ON g.tenant_id = u.tenant_id AND g.id = u.user_group_id
	  LEFT JOIN LATERAL (
	        -- 余额与币种取同一行（原来是两个各自 LIMIT 1 的子查询）
	        SELECT la.balance_signed, la.currency FROM ledger_accounts la
	         WHERE la.tenant_id = u.tenant_id AND la.owner_user_id = u.id
	           AND la.account_type = 'user_balance' LIMIT 1) bal ON true
	  LEFT JOIN LATERAL (
	        SELECT s.id, pl.name AS plan_name, s.status, s.current_period_end,
	               coalesce(s.device_limit, pv.max_devices, 0) AS device_limit
	          FROM subscriptions s
	          LEFT JOIN plans pl ON pl.id = s.plan_id
	          LEFT JOIN plan_versions pv ON pv.id = s.plan_version_id
	         WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id
	         ORDER BY ` + subscription.CurrentOrderSQL + `
	         LIMIT 1) cs ON true
	  LEFT JOIN LATERAL (` + onlineDevicesSQL("u.tenant_id", "cs.id", "win.since") + `) od ON true
	  LEFT JOIN LATERAL (
	        SELECT q.limit_value, q.consumed FROM quota_balances q
	         WHERE q.tenant_id = u.tenant_id AND q.subscription_id = cs.id
	           AND q.metric = 'traffic.bytes'
	         ORDER BY q.period_start DESC LIMIT 1) tq ON true
	 ORDER BY p.created_at DESC, p.id DESC`

// scanUserRows 读列表行（列形状见 listUsersPageSQL）。
func scanUserRows(rows pgx.Rows) ([]UserRow, error) {
	defer rows.Close()
	out := []UserRow{}
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
			return nil, err
		}
		if subID != nil {
			cur.ID, cur.Status, cur.CurrentPeriodEnd = *subID, *subStatus, periodEnd
			if planName != nil {
				cur.PlanName = *planName
			}
			r.CurrentSubscription = &cur
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) ListUsers(ctx context.Context, tenantID string, in ListUsersInput) ([]UserRow, int64, error) {
	in, args, err := listUsersArgs(tenantID, in)
	if err != nil {
		return nil, 0, err
	}
	var out []UserRow
	var total int64
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 总数只数 users 本表（不拼读模型），与取页同一份筛选
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+listUsersWhereSQL, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, listUsersPageSQL, append(args, in.Limit, in.Offset)...)
		if err != nil {
			return err
		}
		out, err = scanUserRows(rows)
		return err
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
		// 在线设备按单条订阅计（onlineDevicesSQL），窗口起点只算一次
		srows, err := tx.Query(ctx, `
			WITH win AS MATERIALIZED (SELECT `+onlineSinceSQL("$1")+` AS since)
			SELECT s.id, pl.name, pv.version, s.status, s.current_period_start, s.current_period_end,
			       s.snapshot_amount, s.snapshot_currency, s.auto_renew,
			       s.device_limit, pv.max_devices, coalesce(od.device_count, 0)::int
			  FROM subscriptions s
			  JOIN plans pl ON pl.id = s.plan_id
			  JOIN plan_versions pv ON pv.id = s.plan_version_id
			  CROSS JOIN win
			  LEFT JOIN LATERAL (`+onlineDevicesSQL("s.tenant_id", "s.id", "win.since")+`) od ON true
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
		// 全部订阅的配额一次读完（原来每条订阅各查一次），每条订阅内的顺序不变
		if len(d.Subscriptions) > 0 {
			bySub := make(map[string]int, len(d.Subscriptions))
			subIDs := make([]string, len(d.Subscriptions))
			for i, sub := range d.Subscriptions {
				bySub[sub.ID], subIDs[i] = i, sub.ID
			}
			qrows, err := tx.Query(ctx, `
				SELECT subscription_id::text, metric, limit_value, consumed, remaining FROM quota_balances
				 WHERE tenant_id = $1 AND subscription_id = ANY($2::uuid[])
				 ORDER BY subscription_id, metric, period_start DESC`, tenantID, subIDs)
			if err != nil {
				return err
			}
			for qrows.Next() {
				var sub string
				var q QuotaRow
				if err := qrows.Scan(&sub, &q.Metric, &q.Limit, &q.Consumed, &q.Remaining); err != nil {
					qrows.Close()
					return err
				}
				if i, ok := bySub[sub]; ok {
					d.Subscriptions[i].Quotas = append(d.Subscriptions[i].Quotas, q)
				}
			}
			qrows.Close()
			if err := qrows.Err(); err != nil {
				return err
			}
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
