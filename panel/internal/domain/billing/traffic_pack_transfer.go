package billing

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 流量包转移（设计稿 2.7）：把「未分配」或「彻底停用」那份上还有余量的流量包，挂到同一用户
// 生效中或可救回的一份上，每笔写一条转移流水（traffic_pack_transfers，追加写）。来源还在用时
// 一律拒绝，不看余额是哪来的（用户 2026-10-09 删掉了升级前旧包可挪一次的例外）。
// 数据库在最后兜底：00157 版的改挂守卫只放行这两种来源，00137 的提交时检查要求同一事务有流水。

// liveOrRevivableSQL 是「生效中或可救回」，与 00137 守卫、subscriptionAcceptsPaidChange 同一组状态。
const liveOrRevivableSQL = `(s.status IN ('active','trialing','grace','past_due')
	OR (s.status = 'expired' AND s.renewal_closed_at IS NULL))`

// endedSQL 是「彻底停用」：已取消，或过期且原地续费窗口已关。
const endedSQL = `(s.status = 'cancelled' OR (s.status = 'expired' AND s.renewal_closed_at IS NOT NULL))`

var (
	errTransferTarget = httpx.New(httpx.CodeConflict, "只能转到一份还在用（或过期不满 30 天）的套餐上")
	// errTransferSource 是来源那份还在用（生效中或可救回）：上面的流量包要等它彻底停用后再转
	errTransferSource = httpx.New(httpx.CodeConflict, "这份还在用，流量包不能转走；等它停用后再转")
)

// TrafficPackTransferInput 是一次转移。From 为空表示「还没加到任何一份」的流量包。
type TrafficPackTransferInput struct {
	UserID string
	From   *string
	To     string
}

// TrafficPackTransferOutput 是转移结果；重复调用时第二次没有东西可转，MovedBytes=0。
type TrafficPackTransferOutput struct {
	MovedBytes int64 `json:"moved_bytes"`
}

// TransferTrafficPacks 是门户「把流量包转到这一份」。不需要幂等键：重复调用第二次 moved_bytes=0。
// 返回后由 handler 在提交后通知节点（纪元已由 00137 的触发器推进）。
func (s *Service) TransferTrafficPacks(ctx context.Context, tenantID string,
	in TrafficPackTransferInput) (*TrafficPackTransferOutput, error) {

	if _, err := uuid.Parse(in.UserID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if _, err := uuid.Parse(in.To); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if in.From != nil {
		if _, err := uuid.Parse(*in.From); err != nil {
			return nil, httpx.NotFoundOrForbidden()
		}
		if *in.From == in.To {
			return nil, httpx.Invalid(map[string]string{"to_subscription_id": "要转到另一份上"})
		}
	}
	var out TrafficPackTransferOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.UserID}, func(tx pgx.Tx) error {
		moved, err := transferTrafficPacksTx(ctx, tx, tenantID, in.UserID, in.From, in.To,
			"user", &in.UserID)
		if err != nil {
			return err
		}
		out.MovedBytes = moved
		if moved == 0 {
			return nil
		}
		userID := in.UserID
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "traffic_pack.transferred", ResourceType: "subscription", ResourceID: &in.To,
			AfterDigest: map[string]any{
				"from_subscription_id": in.From, "to_subscription_id": in.To,
				"moved_bytes": moved,
			},
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}

// transferTrafficPacksTx 在调用方事务里把 from（nil=未分配）上还有余量的流量包挂到 to，返回转走的字节数。
// actorKind 是 user / admin / system；履约时自动挂未分配的余额用 system。
//
// 锁序：先锁两份订阅（FOR KEY SHARE，只防删、不挡续费），再按 created_at, id 锁余额行。
func transferTrafficPacksTx(ctx context.Context, tx pgx.Tx, tenantID, userID string,
	from *string, to, actorKind string, actorID *string) (int64, error) {

	var ok bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM subscriptions s
		                WHERE s.tenant_id = $1 AND s.id = $2::uuid AND s.user_id = $3::uuid
		                  AND `+liveOrRevivableSQL+`
		                FOR KEY SHARE)`, tenantID, to, userID).Scan(&ok); err != nil {
		return 0, err
	}
	if !ok {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscriptions s
			WHERE s.tenant_id = $1 AND s.id = $2::uuid AND s.user_id = $3::uuid)`,
			tenantID, to, userID).Scan(&mine); err != nil {
			return 0, err
		}
		if !mine {
			return 0, httpx.NotFoundOrForbidden()
		}
		return 0, errTransferTarget
	}
	if from != nil {
		var ended, mine bool
		if err := tx.QueryRow(ctx, `
			SELECT true, `+endedSQL+`
			  FROM subscriptions s
			 WHERE s.tenant_id = $1 AND s.id = $2::uuid AND s.user_id = $3::uuid
			 FOR KEY SHARE`, tenantID, *from, userID).Scan(&mine, &ended); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, httpx.NotFoundOrForbidden()
			}
			return 0, err
		}
		if !ended {
			return 0, errTransferSource
		}
	}
	var moved int64
	err := tx.QueryRow(ctx, `
		WITH picked AS (
			SELECT g.id
			  FROM traffic_pack_grants g
			 WHERE g.tenant_id = $1 AND g.user_id = $2::uuid
			   AND g.subscription_id IS NOT DISTINCT FROM $3::uuid
			   AND g.consumed_bytes < g.granted_bytes
			 ORDER BY g.created_at, g.id
			 FOR UPDATE
		), moved AS (
			UPDATE traffic_pack_grants g
			   SET subscription_id = $4::uuid
			  FROM picked p
			 WHERE g.tenant_id = $1 AND g.id = p.id
			RETURNING g.id, g.granted_bytes - g.consumed_bytes AS remaining
		), logged AS (
			INSERT INTO traffic_pack_transfers
				(tenant_id, grant_id, user_id, from_subscription_id, to_subscription_id,
				 remaining_bytes, actor_kind, actor_id)
			SELECT $1, m.id, $2::uuid, $3::uuid, $4::uuid, m.remaining, $5, $6::uuid
			  FROM moved m
			RETURNING remaining_bytes
		)
		SELECT coalesce(sum(remaining_bytes), 0)::bigint FROM logged`,
		tenantID, userID, from, to, actorKind, actorID).Scan(&moved)
	return moved, err
}
