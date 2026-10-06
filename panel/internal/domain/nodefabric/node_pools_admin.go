package nodefabric

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// NodePool 是节点分组列表的一行，json 标签即响应键。
type NodePool struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
	// Nodes 是分组里的节点总数，Active 是其中还在服务的
	Nodes  int `json:"nodes"`
	Active int `json:"active_nodes"`
	// Plans 是绑定了这个分组的套餐版本数。为零说明这组节点当前没被任何套餐用到
	Plans int `json:"plans"`
	// Members 是组内节点（不含已销毁），卡片上的节点标签
	Members []NodePoolMember `json:"members"`
	// PlanNames 是绑定了这个分组的套餐名（去重），卡片上的「绑定套餐」
	PlanNames []string `json:"plan_names"`
	// AllowedUserGroups 是池的「仅限用户组」名单（R104），空 = 不限定
	AllowedUserGroups []NodePoolUserGroup `json:"allowed_user_groups"`
}

// NodePoolMember 是组内的一个节点。
type NodePoolMember struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NodeNo int    `json:"node_no"`
}

// NodePoolUserGroup 是池名单里的一个用户组 { id, name }。
type NodePoolUserGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NodePoolInput 是新建 / 编辑节点分组的已校验输入。WithUserGroups 为 false 时不碰名单；
// 为 true 时 UserGroupIDs 已规范化（空 = 取消限定）。
type NodePoolInput struct {
	ActorID        string
	Code           string
	Name           string
	Region         string
	Status         string
	WithUserGroups bool
	UserGroupIDs   []string
}

// ErrNodePoolMoveFrozen 表示请求要把节点挪到另一个池：配置发布身份升级完成前不允许，
// handler 翻成 409。
var ErrNodePoolMoveFrozen = errors.New("nodefabric: direct node pool move is frozen")

func (s *Service) ListNodePools(ctx context.Context, tenantID string) ([]NodePool, error) {
	out := []NodePool{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id::text, p.code, p.name, COALESCE(p.region,''), p.status,
			       (SELECT count(*) FROM nodes n WHERE n.pool_id = p.id),
			       (SELECT count(*) FROM nodes n
			         WHERE n.pool_id = p.id AND n.status = 'active'
			           AND n.node_type IS NOT NULL),
			       (SELECT count(*) FROM plan_node_pools pnp WHERE pnp.pool_id = p.id),
			       coalesce((SELECT jsonb_agg(jsonb_build_object('id', n.id, 'name', n.name, 'node_no', n.node_no)
			                                  ORDER BY n.sort_order, n.node_no)
			                   FROM nodes n WHERE n.pool_id = p.id AND n.status <> 'destroyed'), '[]'),
			       coalesce((SELECT array_agg(DISTINCT pl.name ORDER BY pl.name)
			                   FROM plan_node_pools pnp
			                   JOIN plan_versions pv ON pv.tenant_id = pnp.tenant_id AND pv.id = pnp.plan_version_id
			                   JOIN plans pl ON pl.tenant_id = pv.tenant_id AND pl.id = pv.plan_id
			                  WHERE pnp.pool_id = p.id), '{}'),
			       coalesce((SELECT jsonb_agg(jsonb_build_object('id', g.id, 'name', g.name)
			                                  ORDER BY g.name, g.id)
			                   FROM node_pool_user_groups npug
			                   JOIN user_groups g ON g.tenant_id = npug.tenant_id AND g.id = npug.user_group_id
			                  WHERE npug.tenant_id = p.tenant_id AND npug.pool_id = p.id), '[]')
			  FROM node_pools p
			 WHERE p.tenant_id = $1
			 ORDER BY p.name, p.created_at`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p NodePool
			if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Region, &p.Status,
				&p.Nodes, &p.Active, &p.Plans, &p.Members, &p.PlanNames, &p.AllowedUserGroups); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateNodePool 新建分组，带了名单就同事务整体替换。返回新 id 与名单是否变化
// （变了由调用方在提交后发 node.users.changed）；标识重复的唯一约束错误原样返回。
func (s *Service) CreateNodePool(ctx context.Context, tenantID string, in NodePoolInput) (string, bool, error) {
	var newID string
	var groupsChanged bool
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO node_pools (tenant_id, code, name, region, status)
			VALUES ($1, $2, $3, NULLIF($4,''), 'active')
			RETURNING id::text`,
			tenantID, in.Code, in.Name, in.Region).Scan(&newID)
		if err != nil {
			return err
		}
		if err := auditPool(ctx, tx, tenantID, in.ActorID, "node_pool.created", newID,
			map[string]any{"code": in.Code, "name": in.Name}); err != nil {
			return err
		}
		if in.WithUserGroups {
			groupsChanged, err = replacePoolUserGroupsTx(ctx, tx, tenantID, in.ActorID, newID, in.UserGroupIDs)
		}
		return err
	})
	return newID, groupsChanged, err
}

// UpdateNodePool 改分组。传空的字段保持原值，前端可以只提交改动的部分；返回名单是否变化。
func (s *Service) UpdateNodePool(ctx context.Context, tenantID, id string, in NodePoolInput) (bool, error) {
	var groupsChanged bool
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 传空的字段保持原值，这样前端可以只提交改动的部分
		tag, err := tx.Exec(ctx, `
			UPDATE node_pools
			   SET name   = COALESCE(NULLIF($3,''), name),
			       region = CASE WHEN $4 = '' THEN region ELSE $4 END,
			       status = COALESCE(NULLIF($5,''), status),
			       updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, id, strings.TrimSpace(in.Name), in.Region, in.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		if err := auditPool(ctx, tx, tenantID, in.ActorID, "node_pool.updated", id,
			map[string]any{"name": in.Name, "status": in.Status}); err != nil {
			return err
		}
		// UPDATE 已锁住这一行，同一个池的两次名单替换在这里串行
		if in.WithUserGroups {
			groupsChanged, err = replacePoolUserGroupsTx(ctx, tx, tenantID, in.ActorID, id, in.UserGroupIDs)
		}
		return err
	})
	return groupsChanged, err
}

func (s *Service) DeleteNodePool(ctx context.Context, tenantID, actorID, id string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			"node-config-release/"+tenantID); err != nil {
			return err
		}
		// Lock the parent before checking dependencies. Node creation/clone and
		// config publication take a compatible SHARE lock, so an ON DELETE SET
		// NULL cascade cannot slip between count=0 and DELETE and bypass the
		// pre-00048 pool-move freeze.
		var lockedID string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM node_pools
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR UPDATE`, tenantID, id).Scan(&lockedID); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		// 还挂着节点的分组不能删。删掉的话那些节点会变成没有归属的孤儿，
		// 既不出现在任何套餐里，也不会有人注意到它们还在跑
		var nodes, plans, templates, configs, activeBootstrapTokens int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM nodes WHERE tenant_id=$1 AND pool_id=$2::uuid),
			       (SELECT count(*) FROM plan_node_pools WHERE tenant_id=$1 AND pool_id=$2::uuid),
			       (SELECT count(*) FROM node_templates WHERE tenant_id=$1 AND default_pool_id=$2::uuid),
			       (SELECT count(*) FROM node_configs
			         WHERE tenant_id=$1 AND scope='pool' AND scope_ref=$2::uuid),
			       (SELECT count(*) FROM bootstrap_tokens
			         WHERE tenant_id=$1 AND pool_id=$2::uuid
			           AND consumed_at IS NULL AND expires_at>now() AND used_count<max_uses)`,
			tenantID, id).Scan(&nodes, &plans, &templates, &configs, &activeBootstrapTokens); err != nil {
			return err
		}
		if nodes > 0 {
			return httpx.New(httpx.CodeConflict,
				"这个分组下还有节点，先把节点移到别的分组再删")
		}
		if plans > 0 {
			return httpx.New(httpx.CodeConflict,
				"还有套餐绑定着这个分组，先解除绑定再删")
		}
		if templates > 0 {
			return httpx.New(httpx.CodeConflict,
				"还有节点模板使用这个默认分组，先修改模板再删")
		}
		if configs > 0 {
			return httpx.New(httpx.CodeConflict,
				"还有配置发布记录引用这个分组，在有效发布迁移完成前不能删除")
		}
		if activeBootstrapTokens > 0 {
			return httpx.New(httpx.CodeConflict,
				"还有未使用的引导令牌绑定这个分组，请等待令牌过期后再删除")
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM node_pools WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		return auditPool(ctx, tx, tenantID, actorID, "node_pool.deleted", id, nil)
	})
}

// CheckNodePoolAssignment 锁住节点行，确认它已经在 poolID 这个池（空串 = 无池）：
// 同池是幂等重放，不同池回 ErrNodePoolMoveFrozen。什么都不写。
func (s *Service) CheckNodePoolAssignment(ctx context.Context, tenantID, nodeID, poolID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var current *string
		if err := tx.QueryRow(ctx, `
			SELECT pool_id::text FROM nodes
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR UPDATE`, tenantID, nodeID).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		currentID := ""
		if current != nil {
			currentID = *current
		}
		if currentID != poolID {
			return ErrNodePoolMoveFrozen
		}
		return nil // same-pool idempotent replay
	})
}

// auditPool 写节点分组的审计；actorID 为空串时记为无操作人。
func auditPool(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, resourceID string,
	after map[string]any) error {
	return audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: optionalActor(actorID),
		Action: action, ResourceType: "node_pool", ResourceID: &resourceID,
		APIDomain: "admin", Outcome: "success",
		RequestID: httpx.RequestIDFrom(ctx), AfterDigest: after,
	})
}

// optionalActor 把空操作人记成 NULL，与原先按主体是否存在取 actor 的写法等价。
func optionalActor(actorID string) *string {
	if actorID == "" {
		return nil
	}
	return &actorID
}
