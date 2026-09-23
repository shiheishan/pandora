package support

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// WithdrawByUser 撤回自己提的工单（对标 Xboard ticket/withdraw）。
//
// 和 CloseByUser 的区别只在 closed_reason 一列，但那一列决定了
// 客服的解决率算不算这一单。用户自己撤掉的工单不该算作一次成功服务。
//
// 只有还没被客服回复过的工单能撤回：客服已经花时间看过、回过了，
// 再让它变成「当我没提过」对客服不公平，也让工单量统计失真。
// 这种情况下用户应该点关闭。
func (s *Service) WithdrawByUser(ctx context.Context, tenantID, userID,
	ticketID, reason string) error {

	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 500 {
		return httpx.Invalid(map[string]string{"reason": "撤回说明不能超过 500 字"})
	}
	// 非法 id 当作不存在处理，不要让它一路走到数据库变成 500。
	// 22P02 对调用方毫无意义，而且 500 和 404 的区别能被拿来探测。
	if _, err := uuid.Parse(ticketID); err != nil {
		return httpx.NotFoundOrForbidden()
	}

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var before string
		var agentReplies int
		if err := tx.QueryRow(ctx, `
			SELECT t.status,
			       (SELECT count(*) FROM ticket_messages m
			         WHERE m.tenant_id = t.tenant_id AND m.ticket_id = t.id
			           AND m.author_kind = 'agent')
			  FROM tickets t
			 WHERE t.tenant_id = $1 AND t.id = $2 AND t.user_id = $3
			 FOR UPDATE`, tenantID, ticketID, userID).Scan(&before, &agentReplies); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if before == "closed" {
			return httpx.New(httpx.CodeConflict, "这个工单已经关闭了")
		}
		if agentReplies > 0 {
			return httpx.New(httpx.CodeConflict,
				"客服已经回复过这个工单，不能再撤回。如果问题已解决，请直接关闭")
		}

		ct, err := tx.Exec(ctx, `
			UPDATE tickets
			   SET status = 'closed', closed_at = now(),
			       closed_reason = 'withdrawn', closed_note = NULLIF($4, '')
			 WHERE tenant_id = $1 AND id = $2 AND user_id = $3
			   AND status <> 'closed'`,
			tenantID, ticketID, userID, reason)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}

		note := "用户已撤回此工单"
		if reason != "" {
			note += "：" + reason
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_messages (tenant_id, ticket_id, author_kind, body)
			VALUES ($1, $2, 'system', $3)`, tenantID, ticketID, note); err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "ticket.withdraw", ResourceType: "ticket", ResourceID: &ticketID,
			APIDomain: "public", Outcome: "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": before},
			AfterDigest: map[string]any{
				"status": "closed", "closed_reason": "withdrawn", "note": reason,
			},
		})
	})
}
