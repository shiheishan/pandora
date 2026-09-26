// [INPUT]: 依赖 service.go 的 Service 与 AtomicEscalateResult，依赖 platform 的 db/audit、middleware 的幂等原子完成
// [OUTPUT]: 对外提供 Service 的 EscalateOverdue、EscalateOverdueAsAdminAtomic
// [POS]: domain/support 的 SLA 超时升级：定时任务以 system 身份幂等扫描（不依赖 HTTP 认领），后台人工升级走原子版本并把优先级提到至少 high
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// SLA 升级（OPS-001 验收「超时自动升级」）
//------------------------------------------------------------------------------

// EscalateOverdue 把首次响应超时的工单置为 escalated。
//
// 幂等（XBD-024）：条件里带 status <> 'escalated'，重复运行不会重复升级，
// 也不会覆盖已有的 escalated_at。返回本次升级的条数。
func (s *Service) EscalateOverdue(ctx context.Context, tenantID string) (int, error) {
	// api_domain is the surface whose policy owns the operation; the audit
	// schema intentionally has no separate "system" domain.
	return s.escalateOverdue(ctx, tenantID, "system", nil, "admin", nil)
}

func (s *Service) EscalateOverdueAsAdminAtomic(
	ctx context.Context,
	tenantID, actorID string,
	claim middleware.IdempotencyClaim,
) (AtomicEscalateResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, actorID, EscalateIdempotencyScope,
	); err != nil {
		return AtomicEscalateResult{}, fmt.Errorf("escalate overdue tickets: %w", err)
	}
	var prepared httpx.PreparedResponse
	n, err := s.escalateOverdue(
		ctx, tenantID, "admin", &actorID, "admin",
		func(tx pgx.Tx, count int) error {
			var err error
			prepared, err = prepareAtomicSuccess(http.StatusOK, map[string]any{"escalated": count})
			if err != nil {
				return err
			}
			return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
		},
	)
	return AtomicEscalateResult{
		Escalated: n, AtomicResult: AtomicResult{prepared: prepared},
	}, err
}

func (s *Service) escalateOverdue(
	ctx context.Context,
	tenantID, actorKind string,
	actorID *string,
	apiDomain string,
	complete func(pgx.Tx, int) error,
) (int, error) {
	var n int
	scope := db.Scope{TenantID: tenantID}
	if actorID != nil {
		scope.ActorID = *actorID
	}
	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH candidates AS MATERIALIZED (
				SELECT id, status AS before_status, priority AS before_priority
				  FROM tickets
				 WHERE tenant_id = $1
				   AND first_responded_at IS NULL
				   AND sla_first_response_due < now()
				   AND status NOT IN ('resolved', 'closed', 'escalated')
				 ORDER BY sla_first_response_due, id
				 FOR UPDATE
			), updated AS (
				UPDATE tickets AS t
				   SET status = 'escalated', escalated_at = now(),
				       priority = CASE t.priority
				                    WHEN 'low' THEN 'normal'
				                    WHEN 'normal' THEN 'high'
				                    ELSE 'urgent' END
				  FROM candidates AS c
				 WHERE t.id = c.id
				 RETURNING t.id, c.before_status, c.before_priority, t.priority
			)
			SELECT id, before_status, before_priority, priority FROM updated
			 ORDER BY id`, tenantID)
		if err != nil {
			return err
		}
		type escalatedTicket struct {
			id             string
			beforeStatus   string
			beforePriority string
			priority       string
		}
		var tickets []escalatedTicket
		for rows.Next() {
			var ticket escalatedTicket
			if err := rows.Scan(
				&ticket.id, &ticket.beforeStatus, &ticket.beforePriority, &ticket.priority,
			); err != nil {
				rows.Close()
				return err
			}
			tickets = append(tickets, ticket)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		n = len(tickets)

		for _, ticket := range tickets {
			if _, err := tx.Exec(ctx, `
				INSERT INTO ticket_messages (tenant_id, ticket_id, author_kind, body)
				VALUES ($1, $2, 'system', '首次响应已超过 SLA，工单自动升级')`,
				tenantID, ticket.id); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: actorKind, ActorID: actorID,
				Action: "ticket.sla_escalate", ResourceType: "ticket", ResourceID: &ticket.id,
				APIDomain: apiDomain, Outcome: "success",
				RequestID: httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{
					"status": ticket.beforeStatus, "priority": ticket.beforePriority,
				},
				AfterDigest: map[string]any{
					"status": "escalated", "priority": ticket.priority,
				},
			}); err != nil {
				return err
			}
		}
		if complete != nil {
			return complete(tx, n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
