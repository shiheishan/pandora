// [INPUT]: 依赖 audit_events（含 00080 的 auth_context 与 source_ip_enc 密文），按 resource_type 连 users / orders / nodes / plans / tickets 取可读名，依赖 platform 的 db/audit/httpx
// [OUTPUT]: 对外提供 AuditRow、AuditFilter、AuditExportMax、Service.ListAudit / ExportAudit
// [POS]: adminops 的审计日志读模型（后台-09「审计日志」与导出）；列表与导出共用 auditRowSelect 与 auditCond 一份形状，来源 IP 只给密文，由 api 层用信封解密
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

type AuditRow struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	ActorKind    string    `json:"actor_kind"`
	ActorEmail   *string   `json:"actor_email"`
	Action       string    `json:"action"`
	ResourceType *string   `json:"resource_type"`
	ResourceID   *string   `json:"resource_id"`
	APIDomain    *string   `json:"api_domain"`
	Outcome      string    `json:"outcome"`
	// Reason 从 after_digest 里取：审计表没有独立列，
	// 但「为什么这么改」是管理端排查时最想先看到的一列
	Reason *string `json:"reason"`
	// ResourceLabel 是对象的可读名（用户邮箱、订单号、节点名…），
	// 对象已删或类型不在映射里时为 null，前端退回类型 + 短 id
	ResourceLabel *string `json:"resource_label"`
	// AuthContext：session / reauth，00080 之前的记录与系统记录为 null
	AuthContext *string `json:"auth_context"`
	// SourceIP 由 api 层解密 SourceIPEnc 后填入；解不开（早于加密上线）为 null
	SourceIP    *string `json:"source_ip"`
	SourceIPEnc []byte  `json:"-"`
}

// AuditFilter 是审计日志的可选筛选条件。零值表示不筛。
//
// 自动任务（order.expired 之类）的量远大于人工操作——压测跑完这张表
// 一万五千条，翻开全是它。没有筛选的话，这份日志实际上没法用来追查。
type AuditFilter struct {
	// ActionPrefix 按动作前缀匹配。动作是 order.expired / payment.succeeded
	// 这种带命名空间的串，前缀能一次圈定一整类。
	ActionPrefix string
	// ActorKind 区分 system 与 user，把自动任务和人工操作分开看。
	ActorKind string
	Outcome   string
	// Query 是搜索框：动作与操作者邮箱做包含匹配，对象 id 做精确匹配
	// （设计稿的「操作人、动作或对象」）。
	Query string
	// From / To 是半开区间 [From, To)，只有导出用。
	From, To *time.Time
}

// AuditExportMax 是一次导出的行数上限。导出不走流式接口，受网关 25 秒
// 请求超时约束；超过就让人缩小时间范围，而不是导出一半静默截断。
const AuditExportMax = 50000

// auditCond 是审计查询的筛选条件，计数、列表与导出共用同一份。
// 分开写迟早会出现「总数按全量算、列表按筛选取」，翻到后面全是空页。
// 依赖 FROM 里的别名 a（audit_events）与 u（操作者）。
const auditCond = `
			 WHERE a.tenant_id = $1
			   AND ($2 = '' OR a.action LIKE $2 || '%')
			   AND ($3 = '' OR a.actor_kind = $3)
			   AND ($4 = '' OR a.outcome = $4)
			   AND ($5 = '' OR a.action ILIKE '%' || $5 || '%'
			        OR u.email::text ILIKE '%' || $5 || '%'
			        OR a.resource_id::text = $5)
			   AND ($6::timestamptz IS NULL OR a.occurred_at >= $6)
			   AND ($7::timestamptz IS NULL OR a.occurred_at < $7)`

const auditFrom = `
			  FROM audit_events a
			  LEFT JOIN users u ON u.id = a.actor_id`

// auditRowSelect 是 AuditRow 的唯一查询形状。对象可读名用按类型分派的标量
// 子查询：每行至多一次主键查找，而一次 LEFT JOIN 五张表会在任何一行上都付
// 五次连接的代价。
const auditRowSelect = `
			SELECT a.id, a.occurred_at, a.actor_kind, u.email, a.action,
			       a.resource_type, a.resource_id::text, a.api_domain, a.outcome,
			       a.after_digest->>'reason',
			       CASE a.resource_type
			         WHEN 'user'   THEN (SELECT x.email::text FROM users x
			                              WHERE x.tenant_id = a.tenant_id AND x.id = a.resource_id)
			         WHEN 'order'  THEN (SELECT x.order_no::text FROM orders x
			                              WHERE x.tenant_id = a.tenant_id AND x.id = a.resource_id)
			         WHEN 'node'   THEN (SELECT x.name::text FROM nodes x
			                              WHERE x.tenant_id = a.tenant_id AND x.id = a.resource_id)
			         WHEN 'plan'   THEN (SELECT x.name::text FROM plans x
			                              WHERE x.tenant_id = a.tenant_id AND x.id = a.resource_id)
			         WHEN 'ticket' THEN (SELECT x.ticket_no::text FROM tickets x
			                              WHERE x.tenant_id = a.tenant_id AND x.id = a.resource_id)
			       END,
			       a.auth_context, a.source_ip_enc` + auditFrom

func (f AuditFilter) args(tenantID string) []any {
	return []any{tenantID, f.ActionPrefix, f.ActorKind, f.Outcome,
		strings.TrimSpace(f.Query), f.From, f.To}
}

func scanAuditRows(rows pgx.Rows) ([]AuditRow, error) {
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.ActorKind, &r.ActorEmail,
			&r.Action, &r.ResourceType, &r.ResourceID, &r.APIDomain,
			&r.Outcome, &r.Reason, &r.ResourceLabel, &r.AuthContext,
			&r.SourceIPEnc); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) ListAudit(ctx context.Context, tenantID string, limit, offset int, f AuditFilter) ([]AuditRow, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []AuditRow
	var total int64

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*)`+auditFrom+auditCond,
			f.args(tenantID)...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, auditRowSelect+auditCond+`
			 ORDER BY a.occurred_at DESC, a.id DESC
			 LIMIT $8 OFFSET $9`, append(f.args(tenantID), limit, offset)...)
		if err != nil {
			return err
		}
		out, err = scanAuditRows(rows)
		return err
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}
	return out, total, nil
}

// ExportAudit 取出筛选命中的全部记录（至多 AuditExportMax 行），并在同一个
// 事务里记一条 audit.export：导出带走的是含明文来源 IP 的全量记录，
// 「谁在什么时候按什么条件导走了多少行」本身就必须留痕。
func (s *Service) ExportAudit(ctx context.Context, tenantID, actorID string, f AuditFilter) ([]AuditRow, error) {
	var out []AuditRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var total int64
		if err := tx.QueryRow(ctx, `SELECT count(*)`+auditFrom+auditCond,
			f.args(tenantID)...).Scan(&total); err != nil {
			return err
		}
		if total > AuditExportMax {
			return httpx.New(httpx.CodeValidationFailed, "超过 50000 行，请缩小时间范围")
		}
		rows, err := tx.Query(ctx, auditRowSelect+auditCond+`
			 ORDER BY a.occurred_at DESC, a.id DESC`, f.args(tenantID)...)
		if err != nil {
			return err
		}
		if out, err = scanAuditRows(rows); err != nil {
			return err
		}
		day := func(t *time.Time) any {
			if t == nil {
				return nil
			}
			return t.Format("2006-01-02")
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID, Action: "audit.export",
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"action": f.ActionPrefix, "actor_kind": f.ActorKind, "outcome": f.Outcome,
				"q": strings.TrimSpace(f.Query), "from": day(f.From), "to_exclusive": day(f.To),
				"rows": len(out),
			},
		})
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return out, nil
}
