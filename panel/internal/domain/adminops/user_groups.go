// [INPUT]: 依赖 platform 的 db 租户事务与约束判定（IsUniqueViolation / IsForeignKeyViolation）、audit 同事务审计、httpx 的错误模型与请求 ID；读写 user_groups 与 users.user_group_id，读 plans / prices / coupons 的组引用与 node_pool_user_groups（00093）
// [OUTPUT]: 对外提供 Service.ListUserGroups / SaveUserGroup / DeleteUserGroup / AssignUserGroup 与 UserGroup、UserGroupPoolRef
// [POS]: domain/adminops 的用户分组存取（从 api/admin/usergroup.go 下沉）：套餐可见、专属价格、优惠券限定、公告定向与节点池限定（R104）共用的分组实体；删组前逐项数引用并按「池名单 > 用户 > 套餐 > 价格 > 优惠券」给 409，换组后的 node.users.changed 通知仍由 handler 在提交后发
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// UserGroupPoolRef 是把某个用户组列入限定名单的节点池。
type UserGroupPoolRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UserGroup 是后台用户组列表的一行，带「正在被谁用」的引用计数。
type UserGroup struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Desc  string `json:"description"`
	Users int    `json:"users"`
	// 下面三个是「这个组正在被谁用」。删组之前要让人看见影响面，
	// 而不是删完才发现有套餐从此没人看得到
	Plans   int `json:"plans"`
	Prices  int `json:"prices"`
	Coupons int `json:"coupons"`
	// ExclusivePools 是把这个组列入限定名单的节点池（R104），只读；
	// 空表示这个组只能用未限定的池
	ExclusivePools []UserGroupPoolRef `json:"exclusive_pools"`
}

// ListUserGroups 按建组时间列出本租户全部用户组。
func (s *Service) ListUserGroups(ctx context.Context, tenantID string) ([]UserGroup, error) {
	out := []UserGroup{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT g.id::text, g.code, g.name, COALESCE(g.description,''),
			       (SELECT count(*) FROM users u WHERE u.user_group_id = g.id),
			       (SELECT count(*) FROM plans p WHERE g.id = ANY(p.visible_group_ids)),
			       (SELECT count(*) FROM prices pr WHERE pr.user_group_id = g.id),
			       (SELECT count(*) FROM coupons c WHERE g.id = ANY(c.applicable_user_group_ids)),
			       coalesce((SELECT jsonb_agg(jsonb_build_object('id', np.id, 'name', np.name)
			                                  ORDER BY np.name, np.id)
			                   FROM node_pool_user_groups npug
			                   JOIN node_pools np ON np.tenant_id = npug.tenant_id AND np.id = npug.pool_id
			                  WHERE npug.tenant_id = g.tenant_id AND npug.user_group_id = g.id), '[]')
			  FROM user_groups g
			 WHERE g.tenant_id = $1
			 ORDER BY g.created_at`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g UserGroup
			if err := rows.Scan(&g.ID, &g.Code, &g.Name, &g.Desc,
				&g.Users, &g.Plans, &g.Prices, &g.Coupons, &g.ExclusivePools); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SaveUserGroup 新建（id 为空）或改名一个用户组，返回组 id；同事务写 user_group.saved 审计。
// code 只在新建时写入：它已经被套餐、价格、优惠券按 ID 引用，不给改。标识重复回 409。
func (s *Service) SaveUserGroup(ctx context.Context, tenantID string, actorID *string, id, code, name, desc string) (string, error) {
	var newID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if id == "" {
			if err := tx.QueryRow(ctx, `
				INSERT INTO user_groups (tenant_id, code, name, description, policy)
				VALUES ($1,$2,$3,NULLIF($4,''),'{}'::jsonb)
				RETURNING id::text`,
				tenantID, code, name, desc).Scan(&newID); err != nil {
				return err
			}
		} else {
			newID = id
			// code 不给改：它已经被套餐、价格、优惠券按 ID 引用，
			// 改名是运营需求，改标识只会制造对不上的引用
			tag, err := tx.Exec(ctx, `
				UPDATE user_groups SET name = $3, description = NULLIF($4,''), updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, id, name, desc)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return httpx.NotFoundOrForbidden()
			}
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "user_group.saved", ResourceType: "user_group", ResourceID: &newID,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"code": code, "name": name},
		})
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return "", httpx.New(httpx.CodeConflict, "这个分组标识已存在")
		}
		return "", err
	}
	return newID, nil
}

// DeleteUserGroup 删除一个没有任何引用的用户组；同事务写 user_group.deleted 审计。
func (s *Service) DeleteUserGroup(ctx context.Context, tenantID string, actorID *string, id string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 还有东西挂着的组不能删。
		//
		// 尤其是套餐：删掉组之后 visibility='group' 的套餐会变成
		// 谁都看不见 —— 它还在售，订单接口也还认，但没人能找到它。
		//
		// 节点池的限定名单同理，而且更危险：名单里只剩这一个组时，删掉它会让
		// 名单变空，池就从「只给这个组」悄悄变成「谁都能用」（R104）。
		var users, plans, prices, coupons int
		var pools string
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM users  WHERE user_group_id = $1::uuid),
			       (SELECT count(*) FROM plans  WHERE $1::uuid = ANY(visible_group_ids)),
			       (SELECT count(*) FROM prices WHERE user_group_id = $1::uuid),
			       (SELECT count(*) FROM coupons WHERE $1::uuid = ANY(applicable_user_group_ids)),
			       coalesce((SELECT string_agg('「' || np.name || '」', '' ORDER BY np.name, np.id)
			                   FROM node_pool_user_groups npug
			                   JOIN node_pools np ON np.tenant_id = npug.tenant_id AND np.id = npug.pool_id
			                  WHERE npug.tenant_id = $2 AND npug.user_group_id = $1::uuid), '')`,
			id, tenantID).Scan(&users, &plans, &prices, &coupons, &pools); err != nil {
			return err
		}
		switch {
		case pools != "":
			return httpx.New(httpx.CodeConflict,
				"节点池"+pools+"限定了这个分组，先把它从这些池的名单里移除")
		case users > 0:
			return httpx.New(httpx.CodeConflict, "这个分组下还有用户，先把他们移出去")
		case plans > 0:
			return httpx.New(httpx.CodeConflict, "还有套餐按这个分组控制可见性，先解除")
		case prices > 0:
			return httpx.New(httpx.CodeConflict, "还有分组专属价格挂在这里，先删掉那些价格")
		case coupons > 0:
			return httpx.New(httpx.CodeConflict, "还有优惠券限定了这个分组，先解除")
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM user_groups WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
		if err != nil {
			// 查完到删之间有人把它加进了某个池的名单：外键兜住（00093），照样回 409
			if db.IsForeignKeyViolation(err) {
				return httpx.New(httpx.CodeConflict, "这个分组刚被节点池或其他配置引用，刷新后再试")
			}
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "user_group.deleted", ResourceType: "user_group", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	return err
}

// AssignUserGroup 把一个用户放进某个分组（groupID 为空则移出）；同事务写 user.group_changed 审计。
// 用户或分组不在本租户回 422。
func (s *Service) AssignUserGroup(ctx context.Context, tenantID string, actorID *string, userID, groupID string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var tag interface{ RowsAffected() int64 }
		var err error
		if groupID == "" {
			tag, err = tx.Exec(ctx, `
				UPDATE users SET user_group_id = NULL, updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, userID)
		} else {
			tag, err = tx.Exec(ctx, `
				UPDATE users SET user_group_id = $3::uuid, updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid
				   AND EXISTS (SELECT 1 FROM user_groups g
				                WHERE g.id = $3::uuid AND g.tenant_id = $1)`,
				tenantID, userID, groupID)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.New(httpx.CodeValidationFailed, "用户或分组不存在")
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "user.group_changed", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"group_id": groupID},
		})
	})
	return err
}
