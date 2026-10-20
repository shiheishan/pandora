package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TraceIPRow 是按来源 IP 聚合的一行。三个 Total / DistinctIPs 是整个时间窗的总数（每行都一样），
// 只有取头部（limit > 0）时才算；只补查指定 IP 时为 0。
type TraceIPRow struct {
	IPEnc         []byte    `gorm:"column:ip_enc"`
	Events        int       `gorm:"column:events"`
	UAKinds       int       `gorm:"column:ua_kinds"`
	Accounts      int       `gorm:"column:accounts"`
	AccountIDs    string    `gorm:"column:account_ids"`
	LastAt        time.Time `gorm:"column:last_at"`
	DistinctIPs   int       `gorm:"column:distinct_ips"`
	TotalLogins   int       `gorm:"column:total_logins"`
	TotalFailures int       `gorm:"column:total_login_failures"`
}

// traceIPsSQL 取头部：窗口内先窄聚合到 (ip_hash, user_agent, actor_kind, actor_id) 一行（g，哈希聚合），再按 ip_hash 合并，
// 窗口函数随行带回排名与全窗总数，再取前 limit 名并带上指定的 IP。不在原表上直接 count(DISTINCT)：那样要按 ip_hash 排好序的输入，
// 规划器会顺着 audit_events_ip_idx 逐行回表（30 天 113 万行实测中位 4.4 秒，先窄聚合 1.1 秒，结果逐行相同）。
const traceIPsSQL = `
WITH g AS (
  SELECT ip_hash, user_agent, actor_kind, actor_id,
         count(*) AS n,
         count(*) FILTER (WHERE action = 'user.login' AND outcome = 'success') AS logins,
         count(*) FILTER (WHERE action = 'user.login_failed') AS login_failures,
         max(occurred_at) AS last_at,
         max(ip_enc) AS ip_enc
    FROM audit_events
   WHERE occurred_at > ? AND ip_hash IS NOT NULL AND actor_kind IN ('user', 'anonymous')
   GROUP BY ip_hash, user_agent, actor_kind, actor_id
), ip AS (
  SELECT ip_hash,
         sum(n)::int AS events,
         sum(logins)::int AS logins,
         sum(login_failures)::int AS login_failures,
         count(DISTINCT user_agent)::int AS ua_kinds,
         (count(DISTINCT actor_id) FILTER (WHERE actor_kind = 'user'))::int AS accounts,
         COALESCE(array_to_string((array_agg(DISTINCT actor_id::text) FILTER (WHERE actor_kind = 'user'))[1:20], ','), '') AS account_ids,
         max(last_at) AS last_at,
         max(ip_enc) AS ip_enc
    FROM g
   GROUP BY ip_hash
), ranked AS (
  SELECT ip.*,
         row_number() OVER (ORDER BY events DESC, last_at DESC, ip_hash) AS rk,
         (count(*) OVER ())::int AS distinct_ips,
         (sum(logins) OVER ())::int AS total_logins,
         (sum(login_failures) OVER ())::int AS total_login_failures
    FROM ip
)
SELECT ip_enc, events, ua_kinds, accounts, account_ids, last_at,
       distinct_ips, total_logins, total_login_failures
  FROM ranked
 WHERE rk <= ? OR encode(ip_hash, 'hex') = ANY(CAST(? AS text[]))
 ORDER BY rk`

// traceIPsOnlySQL 只补查指定 IP：先按 ip_hash 过滤（走 audit_events_ip_idx），不排名、不算全窗总数。
const traceIPsOnlySQL = `
SELECT max(ip_enc) AS ip_enc,
       count(*)::int AS events,
       count(DISTINCT user_agent)::int AS ua_kinds,
       (count(DISTINCT actor_id) FILTER (WHERE actor_kind = 'user'))::int AS accounts,
       COALESCE(array_to_string((array_agg(DISTINCT actor_id::text) FILTER (WHERE actor_kind = 'user'))[1:20], ','), '') AS account_ids,
       max(occurred_at) AS last_at
  FROM audit_events
 WHERE ip_hash IN (SELECT decode(h, 'hex') FROM unnest(CAST(? AS text[])) AS h)
   AND ip_hash IS NOT NULL AND occurred_at > ? AND actor_kind IN ('user', 'anonymous')
 GROUP BY ip_hash
 ORDER BY events DESC, last_at DESC, ip_hash`

// TraceIPs 按来源 IP 聚合 since 之后的事件：limit > 0 时按事件数取头部 limit 个、再带上 extraHex（ip_hash 的小写 hex）指定的；
// limit = 0 时只补查 extraHex（另一条窄 SQL，不带全窗总数）。
func (r *Repo) TraceIPs(ctx context.Context, since time.Time, limit int, extraHex []string) ([]TraceIPRow, error) {
	var rows []TraceIPRow
	var err error
	switch {
	case limit > 0:
		err = r.d.Conn(ctx).Raw(traceIPsSQL, since, limit, textArray(extraHex)).Scan(&rows).Error
	case len(extraHex) > 0:
		err = r.d.Conn(ctx).Raw(traceIPsOnlySQL, textArray(extraHex), since).Scan(&rows).Error
	}
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []TraceIPRow{}, nil
	}
	return rows, nil
}

// TraceUserRow 是按用户聚合的一行与它的一个来源 IP（同一用户多行，按排名、IP 次数排好）。
type TraceUserRow struct {
	UserID    uuid.UUID `gorm:"column:user_id"`
	Logins    int       `gorm:"column:logins"`
	LoginIPs  int       `gorm:"column:login_ips"`
	IPs       int       `gorm:"column:ips"`
	UAKinds   int       `gorm:"column:ua_kinds"`
	SharedIPs int       `gorm:"column:shared_ips"`
	LastAt    time.Time `gorm:"column:last_at"`
	IPEnc     []byte    `gorm:"column:ip_enc"`
	IPCount   int       `gorm:"column:ip_count"`
	IPShared  bool      `gorm:"column:ip_shared"`
}

// traceUserSort 是允许的排序列（拼进 SQL 的字符串只能来自这里，不拼入参）。
var traceUserSort = map[string]string{
	"ips":        "u.ips",
	"logins":     "u.logins",
	"login_ips":  "u.login_ips",
	"shared_ips": "u.shared_ips",
	"ua_kinds":   "uas.ua_kinds",
}

// traceUsersSQLTemplate 取头部。原表只扫一遍、先窄聚合到 (actor_id, ip_hash, user_agent) 一行（g），
// 后面的 ui / shared / uas / u 都从 g 派生：被引用多次而物化的只是分组后的行，不是整窗原始行。
const traceUsersSQLTemplate = `
WITH g AS (
  SELECT actor_id, ip_hash, user_agent,
         count(*)::int AS n,
         (count(*) FILTER (WHERE action = 'user.login' AND outcome = 'success'))::int AS logins,
         max(occurred_at) AS last_at,
         max(ip_enc) AS ip_enc
    FROM audit_events
   WHERE occurred_at > ? AND actor_kind = 'user' AND actor_id IS NOT NULL AND ip_hash IS NOT NULL
   GROUP BY actor_id, ip_hash, user_agent
), ui AS (
  SELECT actor_id, ip_hash, sum(n)::int AS n, sum(logins)::int AS logins,
         max(last_at) AS last_at, max(ip_enc) AS ip_enc
    FROM g
   GROUP BY actor_id, ip_hash
), shared AS (
  SELECT ip_hash FROM ui GROUP BY ip_hash HAVING count(*) > 1
), uas AS (
  SELECT actor_id, count(DISTINCT user_agent)::int AS ua_kinds FROM g GROUP BY actor_id
), u AS (
  SELECT ui.actor_id,
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
SELECT p.actor_id AS user_id, p.logins, p.login_ips, p.ips, p.ua_kinds, p.shared_ips, p.last_at,
       i.ip_enc, i.n AS ip_count, i.shared AS ip_shared
  FROM picked p
  JOIN ips i ON i.actor_id = p.actor_id AND i.ip_rk <= 100
 ORDER BY p.rk, i.ip_rk`

// traceUsersOnlySQL 只补查指定用户：按 actor_id 过滤（走 audit_events_actor_idx），不排名；
// 「与别人共用」逐个 IP 用 EXISTS 回查同窗口里别的用户（走 audit_events_ip_idx），口径与头部 SQL 的 shared 相同。
const traceUsersOnlySQL = `
WITH g AS (
  SELECT actor_id, ip_hash, user_agent,
         count(*)::int AS n,
         (count(*) FILTER (WHERE action = 'user.login' AND outcome = 'success'))::int AS logins,
         max(occurred_at) AS last_at,
         max(ip_enc) AS ip_enc
    FROM audit_events
   WHERE actor_id = ANY(CAST(? AS uuid[])) AND actor_id IS NOT NULL AND occurred_at > ?
     AND actor_kind = 'user' AND ip_hash IS NOT NULL
   GROUP BY actor_id, ip_hash, user_agent
), ui AS (
  SELECT actor_id, ip_hash, sum(n)::int AS n, sum(logins)::int AS logins,
         max(last_at) AS last_at, max(ip_enc) AS ip_enc
    FROM g
   GROUP BY actor_id, ip_hash
), uis AS (
  SELECT ui.*,
         EXISTS (SELECT 1 FROM audit_events e
                  WHERE e.ip_hash = ui.ip_hash AND e.ip_hash IS NOT NULL AND e.occurred_at > ?
                    AND e.actor_kind = 'user' AND e.actor_id IS NOT NULL AND e.actor_id <> ui.actor_id) AS shared
    FROM ui
), uas AS (
  SELECT actor_id, count(DISTINCT user_agent)::int AS ua_kinds FROM g GROUP BY actor_id
), u AS (
  SELECT uis.actor_id,
         sum(uis.logins)::int AS logins,
         (count(*) FILTER (WHERE uis.logins > 0))::int AS login_ips,
         count(*)::int AS ips,
         (count(*) FILTER (WHERE uis.shared))::int AS shared_ips,
         max(uis.last_at) AS last_at
    FROM uis
   GROUP BY uis.actor_id
), ips AS (
  SELECT actor_id, ip_enc, n, shared,
         row_number() OVER (PARTITION BY actor_id ORDER BY n DESC, last_at DESC, ip_hash) AS ip_rk
    FROM uis
)
SELECT u.actor_id AS user_id, u.logins, u.login_ips, u.ips, uas.ua_kinds, u.shared_ips, u.last_at,
       i.ip_enc, i.n AS ip_count, i.shared AS ip_shared
  FROM u
  JOIN uas ON uas.actor_id = u.actor_id
  JOIN ips i ON i.actor_id = u.actor_id AND i.ip_rk <= 100
 ORDER BY u.actor_id, i.ip_rk`

// TraceUsers 按操作者聚合 since 之后 actor_kind = user 的事件：limit > 0 时按 sort 取头部 limit 个、再带上 ids；
// limit = 0 时只补查 ids（另一条窄 SQL）。每个用户最多带 100 个 IP。
func (r *Repo) TraceUsers(ctx context.Context, since time.Time, sort string, limit int, ids []uuid.UUID) ([]TraceUserRow, error) {
	var rows []TraceUserRow
	var err error
	switch {
	case limit > 0:
		key := traceUserSort[sort]
		if key == "" {
			key = traceUserSort["ips"]
		}
		err = r.d.Conn(ctx).Raw(fmt.Sprintf(traceUsersSQLTemplate, key), since, limit, uuidArray(ids)).Scan(&rows).Error
	case len(ids) > 0:
		err = r.d.Conn(ctx).Raw(traceUsersOnlySQL, uuidArray(ids), since, since).Scan(&rows).Error
	}
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
