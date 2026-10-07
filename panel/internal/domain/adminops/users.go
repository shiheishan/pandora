package adminops

import (
	"context"
	"slices"
	"strconv"
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

// listUsersWhere 是用户列表的筛选，总数与取页共用。a 是 listUsersArgs 规整好的 $1–$7
// （租户、LIKE 模式、精确 id、令牌哈希、状态、用户组、订阅状态），返回只含用到的条件的
// WHERE 与它的参数（$1 恒为租户）。
//
// 不写「$2 为空串或命中」这类万能条件：pgx 缓存语句后 PostgreSQL 改用通用计划，万能条件的
// 选择率只能按缺省值估，也没法按实际取值裁掉不用的分支（审计 P12）。每种筛选组合是一条
// 单独的语句文本，各自缓存。语义与原来逐条相同：
//   - q 依次按邮箱 / 显示名片段、用户 id 精确、订阅令牌反查；原来的 u.id::text = 精确值
//     只可能命中规范小写 uuid，所以只在精确值是规范 uuid 时才拼 u.id = 精确值（能走主键）；
//   - 令牌反查写成不相关子查询：整条查询只按哈希查一次凭据（唯一索引），而不是每个
//     用户各探一次。
func listUsersWhere(a []any) (string, []any) {
	pattern, exactID, tokenHash := a[1].(string), a[2].(string), a[3].([]byte)
	statuses, groupID, subState := a[4].([]string), a[5].(string), a[6].(string)
	args := []any{a[0]}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var b strings.Builder
	b.WriteString("u.tenant_id = $1")
	if pattern != "" {
		p := arg(pattern)
		b.WriteString(" AND (lower(u.email) LIKE " + p + " OR lower(coalesce(u.display_name,'')) LIKE " + p)
		if id, err := uuid.Parse(exactID); err == nil && id.String() == exactID {
			b.WriteString(" OR u.id = " + arg(exactID) + "::uuid")
		}
		b.WriteString(` OR u.id IN (SELECT sc.user_id FROM subscription_credentials sc
		                  WHERE sc.tenant_id = $1
		                    AND sc.token_hash = ` + arg(tokenHash) + ` AND sc.status IN ('active','grace')))`)
	}
	if len(statuses) > 0 {
		b.WriteString(" AND u.status::text = ANY(" + arg(statuses) + "::text[])")
	}
	switch groupID {
	case "":
	case "none":
		b.WriteString(" AND u.user_group_id IS NULL")
	default:
		b.WriteString(" AND u.user_group_id::text = " + arg(groupID))
	}
	if subState != "" {
		b.WriteString(" AND " + subStateSQL(arg(subState)))
	}
	return b.String(), args
}

// listUsersPageSQL 先按筛选与排序只取一页 id（page，走 00100 的
// (tenant_id, created_at DESC, id DESC) 索引），再只对这一页拼当前订阅、配额、余额与
// 在线设备。原来这些 LATERAL 挂在 Sort / LIMIT 之下，要对全部用户算完才取 25 条
// （5k 用户 470s）。同一时刻批量建的用户按 id 定序，翻页不重不漏。where 与 limit / offset 的
// 占位符由 listUsersWhere 与调用方给出。
func listUsersPageSQL(where, limit, offset string) string {
	return `
	WITH page AS MATERIALIZED (
	  SELECT u.id, u.created_at
	    FROM users u
	   WHERE ` + where + `
	   ORDER BY u.created_at DESC, u.id DESC
	   LIMIT ` + limit + ` OFFSET ` + offset + `
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
}

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
	// 总数只数 users 本表（不拼读模型），与取页同一份筛选；两条排进一个批次，一次往返
	where, wargs := listUsersWhere(args)
	pageArgs := append(slices.Clone(wargs), in.Limit, in.Offset)
	pageSQL := listUsersPageSQL(where, "$"+strconv.Itoa(len(wargs)+1), "$"+strconv.Itoa(len(wargs)+2))
	var out []UserRow
	var total int64
	b := &pgx.Batch{}
	b.Queue(`SELECT count(*) FROM users u WHERE `+where, wargs...).
		QueryRow(func(row pgx.Row) error { return row.Scan(&total) })
	b.Queue(pageSQL, pageArgs...).Query(func(rows pgx.Rows) error {
		var err error
		out, err = scanUserRows(rows)
		return err
	})
	err = s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{}, b)
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
