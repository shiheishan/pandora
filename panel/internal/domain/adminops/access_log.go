package adminops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// AccessLogQuery 是访问明细的已解析筛选。Include / Exclude 已是非 nil 切片（空数组而非 NULL 进 SQL）；
// 各 any 字段为 nil 时不筛（SQL 里按 IS NULL 判断）。
type AccessLogQuery struct {
	// QueryAudit / QueryFetches 决定查哪张表
	QueryAudit   bool
	QueryFetches bool
	// FilterByCategory 为 true 时审计行要命中 Include 之一（空 = 不限）且不命中 Exclude 之一
	FilterByCategory bool
	Include          []string
	Exclude          []string
	AuditIPHash      any
	FetchIPHash      any
	ActorID          any
	EmailLike        any
	// Outcome 为 ""、success / failure / denied / partial 之一或 error（= 非 success）
	Outcome string
	// Limit 是每张表各取的条数（页大小 + 偏移）
	Limit int
}

// AccessAuditRow 是审计表的一行，来源 IP 是密文（AAD "audit"）。
type AccessAuditRow struct {
	Action      string
	Outcome     string
	SourceIPEnc []byte
	UserAgent   string
	ActorID     *string
	UserEmail   string
	OccurredAt  time.Time
}

// AccessFetchRow 是订阅拉取日志的一行，IP 与 UA 是密文（AAD "subfetch"）。
type AccessFetchRow struct {
	IPEnc     []byte
	UAEnc     []byte
	Result    string
	UserID    *string
	UserEmail string
	FetchedAt time.Time
}

// ListAccessLog 按筛选从两张表各取最近 q.Limit 条。
//
// 为什么不在 SQL 里 UNION：两边的时间列、结果列、关联用户的方式
// 都不一样，UNION 要写一长串 CAST 和 COALESCE，而且加了筛选条件
// 之后查询计划很难预测。分别查、各走各的索引，反而稳定。
func (s *Service) ListAccessLog(ctx context.Context, tenantID string, q AccessLogQuery) ([]AccessAuditRow, []AccessFetchRow, error) {
	var audits []AccessAuditRow
	var fetches []AccessFetchRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if q.QueryAudit {
			rows, err := tx.Query(ctx, `
				SELECT a.action, a.outcome, COALESCE(a.source_ip_enc, ''::bytea),
				       COALESCE(a.user_agent, ''), a.actor_id, COALESCE(u.email, ''),
				       a.occurred_at
				  FROM audit_events a
				  LEFT JOIN users u ON u.tenant_id = a.tenant_id AND u.id = a.actor_id
				 WHERE a.tenant_id = $1
				   AND (NOT $7::bool
				        OR ((cardinality($2::text[]) = 0
				             OR EXISTS (SELECT 1 FROM unnest($2::text[]) p WHERE starts_with(a.action, p)))
				            AND NOT EXISTS (SELECT 1 FROM unnest($8::text[]) p WHERE starts_with(a.action, p))))
				   AND ($3::bytea IS NULL OR a.source_ip_hash = $3)
				   AND ($4::uuid IS NULL OR a.actor_id = $4)
				   AND ($5::text IS NULL OR lower(u.email) LIKE $5)
				   AND ($9::text = '' OR ($9 = 'error' AND a.outcome <> 'success') OR a.outcome = $9)
				 ORDER BY a.occurred_at DESC
				 LIMIT $6`, tenantID, q.Include,
				q.AuditIPHash, q.ActorID, q.EmailLike, q.Limit, q.FilterByCategory, q.Exclude, q.Outcome)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var it AccessAuditRow
				if err := rows.Scan(&it.Action, &it.Outcome, &it.SourceIPEnc, &it.UserAgent,
					&it.ActorID, &it.UserEmail, &it.OccurredAt); err != nil {
					return err
				}
				audits = append(audits, it)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}

		if q.QueryFetches {
			rows, err := tx.Query(ctx, `
				SELECT COALESCE(f.ip_enc, ''::bytea), COALESCE(f.ua_enc, ''::bytea),
				       COALESCE(f.result, ''), s.user_id, COALESCE(u.email, ''),
				       f.fetched_at
				  FROM subscription_fetch_log f
				  LEFT JOIN subscriptions s
				         ON s.tenant_id = f.tenant_id AND s.id = f.subscription_id
				  LEFT JOIN users u ON u.tenant_id = f.tenant_id AND u.id = s.user_id
				 WHERE f.tenant_id = $1
				   AND ($2::bytea IS NULL OR f.ip_hash = $2)
				   AND ($3::uuid IS NULL OR s.user_id = $3)
				   AND ($4::text IS NULL OR lower(u.email) LIKE $4)
				   AND ($6::text = '' OR ($6 = 'success' AND f.result = 'ok') OR ($6 = 'error' AND f.result <> 'ok'))
				 ORDER BY f.fetched_at DESC
				 LIMIT $5`, tenantID, q.FetchIPHash, q.ActorID, q.EmailLike, q.Limit, q.Outcome)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var it AccessFetchRow
				if err := rows.Scan(&it.IPEnc, &it.UAEnc, &it.Result, &it.UserID,
					&it.UserEmail, &it.FetchedAt); err != nil {
					return err
				}
				fetches = append(fetches, it)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return audits, fetches, nil
}
