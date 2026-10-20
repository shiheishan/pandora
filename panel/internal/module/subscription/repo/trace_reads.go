package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 本文件是后台「安全与运维 › 溯源总览」的拉取日志聚合读（由 subscription/service/trace_reads.go 调）。
// 两个方法各一条 SQL：先按时间窗做一次扫描取头部，再把调用方指定的 IP / 用户一并带回。

// TraceIPRow 是拉取日志按来源 IP 聚合的一行（含整个时间窗的总数，每行都一样）。
type TraceIPRow struct {
	IPHash        []byte    `gorm:"column:ip_hash"`
	IPEnc         []byte    `gorm:"column:ip_enc"`
	Fetches       int       `gorm:"column:fetches"`
	Failures      int       `gorm:"column:failures"`
	UAKinds       int       `gorm:"column:ua_kinds"`
	Accounts      int       `gorm:"column:accounts"`
	AccountIDs    string    `gorm:"column:account_ids"`
	LastAt        time.Time `gorm:"column:last_at"`
	DistinctIPs   int       `gorm:"column:distinct_ips"`
	TotalFetches  int       `gorm:"column:total_fetches"`
	TotalFailures int       `gorm:"column:total_failures"`
}

// TraceUserRow 是拉取日志按用户聚合的一行与它的一个来源 IP（同一用户多行，按 rk、ip_rk 排好；没有 IP 的用户 IP 列为空）。
type TraceUserRow struct {
	UserID    uuid.UUID `gorm:"column:user_id"`
	Fetches   int       `gorm:"column:fetches"`
	Failures  int       `gorm:"column:failures"`
	IPs       int       `gorm:"column:ips"`
	UAKinds   int       `gorm:"column:ua_kinds"`
	Families  string    `gorm:"column:families"`
	SharedIPs int       `gorm:"column:shared_ips"`
	LastAt    time.Time `gorm:"column:last_at"`
	Rk        int       `gorm:"column:rk"`
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
		SELECT ip_hash, ip_enc, fetches, failures, ua_kinds, accounts, account_ids, last_at,
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

// TraceUsers 按用户（经凭据）聚合 since 之后的拉取：按 sort 取头部 limit 个，再带上 ids；每个用户最多带 100 个 IP。
func (r *Repo) TraceUsers(ctx context.Context, since time.Time, sort string, limit int, ids []uuid.UUID) ([]TraceUserRow, error) {
	key, ok := traceUserSort[sort]
	if !ok {
		key = traceUserSort["fetches"]
	}
	var rows []TraceUserRow
	err := r.d.Conn(ctx).Raw(`
		WITH w AS (
		  SELECT c.user_id, f.ip_hash, f.ua_hash, f.ua_family, f.result, f.fetched_at, f.ip_enc
		    FROM subscription_fetch_log f
		    JOIN subscription_credentials c ON c.id = f.credential_id
		   WHERE f.fetched_at > ?
		), ui AS (
		  SELECT user_id, ip_hash, count(*)::int AS n, max(fetched_at) AS last_at, max(ip_enc) AS ip_enc
		    FROM w
		   WHERE ip_hash IS NOT NULL
		   GROUP BY user_id, ip_hash
		), shared AS (
		  SELECT ip_hash FROM ui GROUP BY ip_hash HAVING count(*) > 1
		), u AS (
		  SELECT user_id,
		         count(*)::int AS fetches,
		         (count(*) FILTER (WHERE result <> 'ok'))::int AS failures,
		         count(DISTINCT ip_hash)::int AS ips,
		         count(DISTINCT ua_hash)::int AS ua_kinds,
		         array_to_string(array_agg(DISTINCT ua_family ORDER BY ua_family), ',') AS families,
		         max(fetched_at) AS last_at
		    FROM w
		   GROUP BY user_id
		), us AS (
		  SELECT ui.user_id, count(*)::int AS shared_ips
		    FROM ui JOIN shared s ON s.ip_hash = ui.ip_hash
		   GROUP BY ui.user_id
		), ranked AS (
		  SELECT u.*, COALESCE(us.shared_ips, 0) AS shared_ips,
		         row_number() OVER (ORDER BY `+key+` DESC, u.last_at DESC, u.user_id) AS rk
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
		SELECT p.user_id, p.fetches, p.failures, p.ips, p.ua_kinds, p.families, p.shared_ips, p.last_at, p.rk,
		       i.ip_enc, i.n AS ip_count, i.shared AS ip_shared
		  FROM picked p
		  LEFT JOIN ips i ON i.user_id = p.user_id AND i.ip_rk <= 100
		 ORDER BY p.rk, i.ip_rk`,
		since, limit, uuidArray(ids)).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []TraceUserRow{}, nil
	}
	return rows, nil
}
