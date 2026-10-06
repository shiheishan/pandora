// [INPUT]: 依赖 node_pools_admin.go 的 optionalActor，依赖 platform 的 audit/httpx，读写 node_pool_user_groups（00093），读 user_groups
// [OUTPUT]: 包内提供 replacePoolUserGroupsTx
// [POS]: domain/nodefabric 节点池「仅限用户组」名单的整体替换（R104，从 api/admin 的 pool_user_groups.go 下沉）：新建 / 编辑分组在同一事务里调；字段级 reauth 与格式校验仍在 handler，下发规则本身在 PoolAdmitsUserSQL
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// replacePoolUserGroupsTx 把池的名单整体替换成 ids（已规范化；空 = 取消限定），
// 名单有变化时写审计并返回 changed=true。调用方须已持有该池的行锁（新建的
// 行或 UPDATE 过的行），同一个池的两次替换因此串行。
func replacePoolUserGroupsTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, poolID string, ids []string) (bool, error) {
	if len(ids) > 0 {
		// 不存在与跨租户一并按「不存在」回：RLS 下别的租户的组本来就查不到
		var found int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM user_groups
			 WHERE tenant_id = $1 AND id = ANY($2::uuid[])`,
			tenantID, ids).Scan(&found); err != nil {
			return false, err
		}
		if found != len(ids) {
			return false, httpx.Invalid(map[string]string{
				"allowed_user_group_ids": "包含不存在的用户组",
			})
		}
	}

	before := []string{}
	rows, err := tx.Query(ctx, `
		SELECT user_group_id::text FROM node_pool_user_groups
		 WHERE tenant_id = $1 AND pool_id = $2::uuid
		 ORDER BY user_group_id`, tenantID, poolID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		before = append(before, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if slices.Equal(before, ids) {
		return false, nil
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM node_pool_user_groups WHERE tenant_id = $1 AND pool_id = $2::uuid`,
		tenantID, poolID); err != nil {
		return false, err
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_pool_user_groups (tenant_id, pool_id, user_group_id)
			SELECT $1, $2::uuid, g FROM unnest($3::uuid[]) AS g`,
			tenantID, poolID, ids); err != nil {
			return false, err
		}
	}

	return true, audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: optionalActor(actorID),
		Action: "node_pool.user_groups_changed", ResourceType: "node_pool", ResourceID: &poolID,
		APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
		BeforeDigest: map[string]any{"allowed_user_group_ids": before},
		AfterDigest:  map[string]any{"allowed_user_group_ids": ids},
	})
}
