package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TraceIPRow 是按来源 IP 聚合的一行（含整个时间窗的总数，每行都一样）。
type TraceIPRow struct {
	IPHash        []byte    `gorm:"column:ip_hash"`
	IPEnc         []byte    `gorm:"column:ip_enc"`
	Events        int       `gorm:"column:events"`
	Logins        int       `gorm:"column:logins"`
	UAKinds       int       `gorm:"column:ua_kinds"`
	Accounts      int       `gorm:"column:accounts"`
	AccountIDs    string    `gorm:"column:account_ids"`
	LastAt        time.Time `gorm:"column:last_at"`
	DistinctIPs   int       `gorm:"column:distinct_ips"`
	TotalEvents   int       `gorm:"column:total_events"`
	TotalLogins   int       `gorm:"column:total_logins"`
	TotalFailures int       `gorm:"column:total_login_failures"`
}

const traceIPsSQL = `
WITH w AS (
  SELECT ip_hash, ip_enc, actor_id, actor_kind, action, outcome, user_agent, occurred_at
    FROM audit_events
   WHERE occurred_at > ? AND ip_hash IS NOT NULL AND actor_kind IN ('user', 'anonymous')
), ip AS (
  SELECT ip_hash,
         count(*)::int AS events,
         (count(*) FILTER (WHERE action = 'user.login' AND outcome = 'success'))::int AS logins,
         (count(*) FILTER (WHERE action = 'user.login_failed'))::int AS login_failures,
         count(DISTINCT user_agent)::int AS ua_kinds,
         (count(DISTINCT actor_id) FILTER (WHERE actor_kind = 'user'))::int AS accounts,
         COALESCE(array_to_string((array_agg(DISTINCT actor_id::text) FILTER (WHERE actor_kind = 'user'))[1:20], ','), '') AS account_ids,
         max(occurred_at) AS last_at,
         max(ip_enc) AS ip_enc
    FROM w
   GROUP BY ip_hash
), ranked AS (
  SELECT ip.*,
         row_number() OVER (ORDER BY events DESC, last_at DESC, ip_hash) AS rk,
         (count(*) OVER ())::int AS distinct_ips,
         (sum(events) OVER ())::int AS total_events,
         (sum(logins) OVER ())::int AS total_logins,
         (sum(login_failures) OVER ())::int AS total_login_failures
    FROM ip
)
SELECT ip_hash, ip_enc, events, logins, ua_kinds, accounts, account_ids, last_at,
       distinct_ips, total_events, total_logins, total_login_failures
  FROM ranked
 WHERE rk <= ? OR encode(ip_hash, 'hex') = ANY(CAST(? AS text[]))
 ORDER BY rk`

// TraceIPs 按来源 IP 聚合 since 之后的事件：先按事件数取头部 limit 个，再带上 extraHex（ip_hash 的小写 hex）指定的。
func (r *Repo) TraceIPs(ctx context.Context, since time.Time, limit int, extraHex []string) ([]TraceIPRow, error) {
	var rows []TraceIPRow
	err := r.d.Conn(ctx).Raw(traceIPsSQL, since, limit, textArray(extraHex)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []TraceIPRow{}, nil
	}
	return rows, nil
}

// TraceUserRow 是按用户聚合的一行与它的一个来源 IP（同一用户多行，按 rk、ip_rk 排好）。
type TraceUserRow struct {
	UserID    uuid.UUID `gorm:"column:user_id"`
	Events    int       `gorm:"column:events"`
	Logins    int       `gorm:"column:logins"`
	LoginIPs  int       `gorm:"column:login_ips"`
	IPs       int       `gorm:"column:ips"`
	UAKinds   int       `gorm:"column:ua_kinds"`
	SharedIPs int       `gorm:"column:shared_ips"`
	LastAt    time.Time `gorm:"column:last_at"`
	Rk        int       `gorm:"column:rk"`
	IPEnc     []byte    `gorm:"column:ip_enc"`
	IPCount   int       `gorm:"column:ip_count"`
	IPShared  bool      `gorm:"column:ip_shared"`
}

var traceUserSort = map[string]string{
	"ips":        "u.ips",
	"logins":     "u.logins",
	"login_ips":  "u.login_ips",
	"shared_ips": "u.shared_ips",
	"ua_kinds":   "uas.ua_kinds",
}

const traceUsersSQLTemplate = `
WITH w AS (
  SELECT actor_id, ip_hash, ip_enc, user_agent, action, outcome, occurred_at
    FROM audit_events
   WHERE occurred_at > ? AND actor_kind = 'user' AND actor_id IS NOT NULL AND ip_hash IS NOT NULL
), ui AS (
  SELECT actor_id, ip_hash, count(*)::int AS n,
         (count(*) FILTER (WHERE action = 'user.login' AND outcome = 'success'))::int AS logins,
         max(occurred_at) AS last_at, max(ip_enc) AS ip_enc
    FROM w
   GROUP BY actor_id, ip_hash
), shared AS (
  SELECT ip_hash FROM ui GROUP BY ip_hash HAVING count(*) > 1
), uas AS (
  SELECT actor_id, count(DISTINCT user_agent)::int AS ua_kinds FROM w GROUP BY actor_id
), u AS (
  SELECT ui.actor_id,
         sum(ui.n)::int AS events,
         sum(ui.logins)::int AS logins,
         (count(*) FILTER (WHERE ui.logins > 0))::int AS login_ips,
         count(*)::int AS ips,
         count(s.ip_hash)::int AS shared_ips,
         max(ui.last_at) AS last_at
    FROM ui LEFT JOIN shared s ON s.ip_hash = ui.ip_hash
   GROUP BY ui.actor_id
), ranked AS (
  SELECT u.*, uas.ua_kinds,
         row_number() OVER (ORDER BY %s DESC, u.last_at DESC, u.actor_id) AS rk
    FROM u JOIN uas ON uas.actor_id = u.actor_id
), picked AS (
  SELECT * FROM ranked WHERE rk <= ? OR actor_id = ANY(CAST(? AS uuid[]))
), ips AS (
  SELECT ui.actor_id, ui.ip_enc, ui.n, (s.ip_hash IS NOT NULL) AS shared,
         row_number() OVER (PARTITION BY ui.actor_id ORDER BY ui.n DESC, ui.last_at DESC, ui.ip_hash) AS ip_rk
    FROM ui
    JOIN picked p ON p.actor_id = ui.actor_id
    LEFT JOIN shared s ON s.ip_hash = ui.ip_hash
)
SELECT p.actor_id AS user_id, p.events, p.logins, p.login_ips, p.ips, p.ua_kinds, p.shared_ips, p.last_at, p.rk,
       i.ip_enc, i.n AS ip_count, i.shared AS ip_shared
  FROM picked p
  JOIN ips i ON i.actor_id = p.actor_id AND i.ip_rk <= 100
 ORDER BY p.rk, i.ip_rk`

// TraceUsers 按操作者聚合 since 之后 actor_kind = user 的事件：按 sort 取头部 limit 个，再带上 ids；每个用户最多带 100 个 IP。
func (r *Repo) TraceUsers(ctx context.Context, since time.Time, sort string, limit int, ids []uuid.UUID) ([]TraceUserRow, error) {
	key := traceUserSort[sort]
	if key == "" {
		key = traceUserSort["ips"]
	}
	sql := fmt.Sprintf(traceUsersSQLTemplate, key)
	var rows []TraceUserRow
	err := r.d.Conn(ctx).Raw(sql, since, limit, uuidArray(ids)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []TraceUserRow{}, nil
	}
	return rows, nil
}

func textArray(vals []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(v)
	}
	b.WriteByte('}')
	return b.String()
}
