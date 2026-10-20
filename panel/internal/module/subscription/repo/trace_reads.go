package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 本文件是后台「安全与运维 › 溯源总览」的拉取日志聚合读（由 subscription/service/trace_reads.go 调）。
// 取头部时各一条 SQL：先按时间窗做一次扫描、窄聚合，再取头部并把调用方指定的 IP / 用户一并带回；
// 按用户只补查指定用户（limit = 0）时走另一条按凭据过滤的窄 SQL。

// TraceIPRow 是拉取日志按来源 IP 聚合的一行（含整个时间窗的总数，每行都一样）。
type TraceIPRow struct {
	IPEnc         []byte    `gorm:"column:ip_enc"`
	Fetches       int       `gorm:"column:fetches"`
	UAKinds       int       `gorm:"column:ua_kinds"`
	Accounts      int       `gorm:"column:accounts"`
	AccountIDs    string    `gorm:"column:account_ids"`
	LastAt        time.Time `gorm:"column:last_at"`
	DistinctIPs   int       `gorm:"column:distinct_ips"`
	TotalFetches  int       `gorm:"column:total_fetches"`
	TotalFailures int       `gorm:"column:total_failures"`
}

// TraceUserRow 是拉取日志按用户聚合的一行与它的一个来源 IP（同一用户多行连着来，按排名、IP 次数排好；没有 IP 的用户 IP 列为空）。
type TraceUserRow struct {
	UserID    uuid.UUID `gorm:"column:user_id"`
	Fetches   int       `gorm:"column:fetches"`
	Failures  int       `gorm:"column:failures"`
	IPs       int       `gorm:"column:ips"`
	UAKinds   int       `gorm:"column:ua_kinds"`
	Families  string    `gorm:"column:families"`
	SharedIPs int       `gorm:"column:shared_ips"`
	LastAt    time.Time `gorm:"column:last_at"`
	IPEnc     []byte    `gorm:"column:ip_enc"`
	IPCount   *int      `gorm:"column:ip_count"`
	IPShared  *bool     `gorm:"column:ip_shared"`
}

// traceTextArray 把文本切片编成 PostgreSQL text 数组字面量（值只会是小写 hex）。
// GORM 会把切片参数展开成逗号列表，所以数组一律编成字符串再 CAST。
func traceTextArray(vals []string) string {
	if len(vals) == 0 {
		return "{}"
	}
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

// traceUserSort 是允许按用户排行的排序列（拼进 SQL 的字符串只能来自这里，不拼入参）。
var traceUserSort = map[string]string{
	"fetches":    "u.fetches",
	"ips":        "u.ips",
	"ua_kinds":   "u.ua_kinds",
	"failures":   "u.failures",
	"last":       "u.last_at",
	"shared_ips": "COALESCE(us.shared_ips, 0)",
}

// TraceIPs 按来源 IP 聚合 since 之后的拉取：先按次数取头部 limit 个，再带上 extraHex（ip_hash 的小写 hex）指定的。
func (r *Repo) TraceIPs(ctx context.Context, since time.Time, limit int, extraHex []string) ([]TraceIPRow, error) {
	var rows []TraceIPRow
	err := r.d.Conn(ctx).Raw(`
		WITH g AS (
		  SELECT f.ip_hash, f.ua_hash, c.user_id,
		         count(*) AS n,
		         count(*) FILTER (WHERE f.result <> 'ok') AS failed,
		         max(f.fetched_at) AS last_at,
		         max(f.ip_enc) AS ip_enc
		    FROM subscription_fetch_log f
		    LEFT JOIN subscription_credentials c ON c.id = f.credential_id
		   WHERE f.fetched_at > ? AND f.ip_hash IS NOT NULL
		   GROUP BY f.ip_hash, f.ua_hash, c.user_id
		), ip AS (
		  SELECT ip_hash,
		         sum(n)::int AS fetches,
		         sum(failed)::int AS failures,
		         count(DISTINCT ua_hash)::int AS ua_kinds,
		         count(DISTINCT user_id)::int AS accounts,
		         COALESCE(array_to_string((array_agg(DISTINCT user_id::text) FILTER (WHERE user_id IS NOT NULL))[1:20], ','), '') AS account_ids,
		         max(last_at) AS last_at,
		         max(ip_enc) AS ip_enc
		    FROM g
		   GROUP BY ip_hash
		), ranked AS (
		  SELECT ip.*,
		         row_number() OVER (ORDER BY fetches DESC, last_at DESC, ip_hash) AS rk,
		         (count(*) OVER ())::int AS distinct_ips,
		         (sum(fetches) OVER ())::int AS total_fetches,
		         (sum(failures) OVER ())::int AS total_failures
		    FROM ip
		)
		SELECT ip_enc, fetches, ua_kinds, accounts, account_ids, last_at,
		       distinct_ips, total_fetches, total_failures
		  FROM ranked
		 WHERE rk <= ? OR encode(ip_hash, 'hex') = ANY(CAST(? AS text[]))
		 ORDER BY rk`,
		since, limit, traceTextArray(extraHex)).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []TraceIPRow{}, nil
	}
	return rows, nil
}

// traceUsersSQL 取头部。原表只扫一遍、先按凭据窄聚合（gc，不连表），再连凭据并到 (user_id, ip_hash, ua_hash, ua_family) 一行（g），
// 后面的 ui / shared / u / us 都从 g 派生：被引用多次而物化的只是分组后的行，不是整窗原始行。
// 先按凭据分组：整窗原始行不用先连表再排序，30 天 400 万行实测 7.2–8.4 秒降到 6.0–6.4 秒，结果逐行相同。%s 是排序列。
const traceUsersSQL = `
WITH gc AS (
  SELECT f.credential_id, f.ip_hash, f.ua_hash, f.ua_family,
         count(*)::int AS n,
         (count(*) FILTER (WHERE f.result <> 'ok'))::int AS failed,
         max(f.fetched_at) AS last_at,
         max(f.ip_enc) AS ip_enc
    FROM subscription_fetch_log f
   WHERE f.fetched_at > ? AND f.credential_id IS NOT NULL
   GROUP BY f.credential_id, f.ip_hash, f.ua_hash, f.ua_family
), g AS (
  SELECT c.user_id, gc.ip_hash, gc.ua_hash, gc.ua_family,
         sum(gc.n)::int AS n,
         sum(gc.failed)::int AS failed,
         max(gc.last_at) AS last_at,
         max(gc.ip_enc) AS ip_enc
    FROM gc
    JOIN subscription_credentials c ON c.id = gc.credential_id
   GROUP BY c.user_id, gc.ip_hash, gc.ua_hash, gc.ua_family
), ui AS (
  SELECT user_id, ip_hash, sum(n)::int AS n, max(last_at) AS last_at, max(ip_enc) AS ip_enc
    FROM g
   WHERE ip_hash IS NOT NULL
   GROUP BY user_id, ip_hash
), shared AS (
  SELECT ip_hash FROM ui GROUP BY ip_hash HAVING count(*) > 1
), u AS (
  SELECT user_id,
         sum(n)::int AS fetches,
         sum(failed)::int AS failures,
         count(DISTINCT ip_hash)::int AS ips,
         count(DISTINCT ua_hash)::int AS ua_kinds,
         array_to_string(array_agg(DISTINCT ua_family ORDER BY ua_family), ',') AS families,
         max(last_at) AS last_at
    FROM g
   GROUP BY user_id
), us AS (
  SELECT ui.user_id, count(*)::int AS shared_ips
    FROM ui JOIN shared s ON s.ip_hash = ui.ip_hash
   GROUP BY ui.user_id
), ranked AS (
  SELECT u.*, COALESCE(us.shared_ips, 0) AS shared_ips,
         row_number() OVER (ORDER BY %s DESC, u.last_at DESC, u.user_id) AS rk
    FROM u LEFT JOIN us ON us.user_id = u.user_id
), picked AS (
  SELECT * FROM ranked WHERE rk <= ? OR user_id = ANY(CAST(? AS uuid[]))
), ips AS (
  SELECT ui.user_id, ui.ip_enc, ui.n, (s.ip_hash IS NOT NULL) AS shared,
         row_number() OVER (PARTITION BY ui.user_id ORDER BY ui.n DESC, ui.last_at DESC, ui.ip_hash) AS ip_rk
    FROM ui
    JOIN picked p ON p.user_id = ui.user_id
    LEFT JOIN shared s ON s.ip_hash = ui.ip_hash
)
SELECT p.user_id, p.fetches, p.failures, p.ips, p.ua_kinds, p.families, p.shared_ips, p.last_at,
       i.ip_enc, i.n AS ip_count, i.shared AS ip_shared
  FROM picked p
  LEFT JOIN ips i ON i.user_id = p.user_id AND i.ip_rk <= 100
 ORDER BY p.rk, i.ip_rk`

// traceUsersOnlySQL 只补查指定用户：先取这些用户的凭据，按凭据走 subscription_fetch_log_cred_idx，不排名。
// 「与别人共用」：只把这些用户用过的 ip_hash 拿去和同窗口里别的用户的拉取比（时间窗扫描，只过滤不分组），口径与头部 SQL 的 shared 相同。
// other 必须 MATERIALIZED：它只被引用一次，不写就被内联进 uis 的 EXISTS，而拉取表没有 ip_hash 索引，
// 每个 IP 全表扫一遍（30 天 400 万行、1000 个 IP 实测 24 分钟；物化后整窗扫一遍，3.4 秒）。
const traceUsersOnlySQL = `
WITH cred AS (
  SELECT id, user_id FROM subscription_credentials WHERE user_id = ANY(CAST(? AS uuid[]))
), g AS (
  SELECT c.user_id, f.ip_hash, f.ua_hash, f.ua_family,
         count(*)::int AS n,
         (count(*) FILTER (WHERE f.result <> 'ok'))::int AS failed,
         max(f.fetched_at) AS last_at,
         max(f.ip_enc) AS ip_enc
    FROM subscription_fetch_log f
    JOIN cred c ON c.id = f.credential_id
   WHERE f.fetched_at > ? AND f.credential_id IS NOT NULL
   GROUP BY c.user_id, f.ip_hash, f.ua_hash, f.ua_family
), ui AS (
  SELECT user_id, ip_hash, sum(n)::int AS n, max(last_at) AS last_at, max(ip_enc) AS ip_enc
    FROM g
   WHERE ip_hash IS NOT NULL
   GROUP BY user_id, ip_hash
), other AS MATERIALIZED (
  SELECT DISTINCT f.ip_hash, oc.user_id
    FROM subscription_fetch_log f
    JOIN subscription_credentials oc ON oc.id = f.credential_id
   WHERE f.fetched_at > ? AND f.ip_hash IN (SELECT ip_hash FROM ui)
), uis AS (
  SELECT ui.*,
         EXISTS (SELECT 1 FROM other o WHERE o.ip_hash = ui.ip_hash AND o.user_id <> ui.user_id) AS shared
    FROM ui
), u AS (
  SELECT user_id,
         sum(n)::int AS fetches,
         sum(failed)::int AS failures,
         count(DISTINCT ip_hash)::int AS ips,
         count(DISTINCT ua_hash)::int AS ua_kinds,
         array_to_string(array_agg(DISTINCT ua_family ORDER BY ua_family), ',') AS families,
         max(last_at) AS last_at
    FROM g
   GROUP BY user_id
), us AS (
  SELECT user_id, (count(*) FILTER (WHERE shared))::int AS shared_ips FROM uis GROUP BY user_id
), ips AS (
  SELECT user_id, ip_enc, n, shared,
         row_number() OVER (PARTITION BY user_id ORDER BY n DESC, last_at DESC, ip_hash) AS ip_rk
    FROM uis
)
SELECT u.user_id, u.fetches, u.failures, u.ips, u.ua_kinds, u.families, COALESCE(us.shared_ips, 0) AS shared_ips, u.last_at,
       i.ip_enc, i.n AS ip_count, i.shared AS ip_shared
  FROM u
  LEFT JOIN us ON us.user_id = u.user_id
  LEFT JOIN ips i ON i.user_id = u.user_id AND i.ip_rk <= 100
 ORDER BY u.user_id, i.ip_rk`

// TraceUsers 按用户（经凭据）聚合 since 之后的拉取：limit > 0 时按 sort 取头部 limit 个、再带上 ids；
// limit = 0 时只补查 ids（另一条窄 SQL）。每个用户最多带 100 个 IP。
func (r *Repo) TraceUsers(ctx context.Context, since time.Time, sort string, limit int, ids []uuid.UUID) ([]TraceUserRow, error) {
	var rows []TraceUserRow
	var err error
	switch {
	case limit > 0:
		key, ok := traceUserSort[sort]
		if !ok {
			key = traceUserSort["fetches"]
		}
		err = r.d.Conn(ctx).Raw(fmt.Sprintf(traceUsersSQL, key), since, limit, uuidArray(ids)).Scan(&rows).Error
	case len(ids) > 0:
		err = r.d.Conn(ctx).Raw(traceUsersOnlySQL, uuidArray(ids), since, since).Scan(&rows).Error
	}
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []TraceUserRow{}, nil
	}
	return rows, nil
}
