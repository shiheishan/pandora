// [INPUT]: 依赖 platform 的 db/audit/httpx、domain/nodefabric 的 NotifyUsersChanged，读写 node_pools、plan_node_pools，读 nodes / plan_versions / plans
// [OUTPUT]: 对外提供 handlers 的 listNodePools / createNodePool / updateNodePool / deleteNodePool / assignNodePool / planPools / setPlanPools（提交后发租户级 node.users.changed，R104）与 notifyNodeUsersChanged
// [POS]: api/admin 的节点分组：节点与套餐之间唯一的连接层；列表带组内节点 members 与绑定套餐名 plan_names
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 节点分组。
//
// 分组是「节点」和「套餐」之间唯一的连接点：节点归到分组里，
// 套餐绑定分组，用户能看到哪些节点由这两层关系决定。
//
// 在这之前这层关系只能靠手写 SQL 维护，后果是：新建的套餐一个分组都没绑，
// 卖出去之后用户拿到一份空订阅 —— 客户端里一个节点都没有，
// 而管理员那边没有任何异常提示。这是必须有界面的原因。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type poolRow struct {
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
	Members []poolMember `json:"members"`
	// PlanNames 是绑定了这个分组的套餐名（去重），卡片上的「绑定套餐」
	PlanNames []string `json:"plan_names"`
	// AllowedUserGroups 是池的「仅限用户组」名单（R104），空 = 不限定
	AllowedUserGroups []namedRef `json:"allowed_user_groups"`
}

type poolMember struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NodeNo int    `json:"node_no"`
}

func (h *handlers) listNodePools(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	out := []poolRow{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
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
			var p poolRow
			if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Region, &p.Status,
				&p.Nodes, &p.Active, &p.Plans, &p.Members, &p.PlanNames, &p.AllowedUserGroups); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"pools": out})
}

type poolReq struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
	// AllowedUserGroupIDs 省略（或 null）= 不改，[] = 取消限定；带了就要近期重认证（R104）
	AllowedUserGroupIDs *[]string `json:"allowed_user_group_ids"`
}

// poolUserGroups 是 poolReq 里名单字段的校验结果：present 为 false 时不碰名单。
func (req poolReq) poolUserGroups(r *http.Request) (ids []string, present bool, err error) {
	if req.AllowedUserGroupIDs == nil {
		return nil, false, nil
	}
	if err := requirePoolGroupsReauth(r, true); err != nil {
		return nil, true, err
	}
	ids, err = normalizePoolUserGroupIDs(*req.AllowedUserGroupIDs)
	return ids, true, err
}

func (h *handlers) createNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req poolReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	groupIDs, withGroups, err := req.poolUserGroups(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Code = strings.TrimSpace(req.Code)
	if req.Name == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"name": "分组名必填"}))
		return
	}
	if req.Code == "" {
		// code 是给接口和脚本用的稳定标识，不填就从名字派生。
		// 名字全是中文时派生不出东西，退回用时间戳兜底
		req.Code = slugify(req.Name)
	}

	var newID string
	var groupsChanged bool
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			INSERT INTO node_pools (tenant_id, code, name, region, status)
			VALUES ($1, $2, $3, NULLIF($4,''), 'active')
			RETURNING id::text`,
			tenantID, req.Code, req.Name, req.Region).Scan(&newID)
		if err != nil {
			return err
		}
		if err := auditPool(r, tx, tenantID, "node_pool.created", newID,
			map[string]any{"code": req.Code, "name": req.Name}); err != nil {
			return err
		}
		if withGroups {
			groupsChanged, err = replacePoolUserGroupsTx(r.Context(), tx, tenantID, newID, groupIDs)
		}
		return err
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeConflict, "这个分组标识已存在"))
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if groupsChanged {
		h.notifyNodeUsersChanged(r)
	}
	httpx.OK(w, map[string]any{"id": newID})
}

func (h *handlers) updateNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	var req poolReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	groupIDs, withGroups, err := req.poolUserGroups(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Status != "" && req.Status != "active" &&
		req.Status != "draining" && req.Status != "disabled" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"状态只能是 active / draining / disabled"))
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	var groupsChanged bool
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 传空的字段保持原值，这样前端可以只提交改动的部分
		tag, err := tx.Exec(r.Context(), `
			UPDATE node_pools
			   SET name   = COALESCE(NULLIF($3,''), name),
			       region = CASE WHEN $4 = '' THEN region ELSE $4 END,
			       status = COALESCE(NULLIF($5,''), status),
			       updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, id, strings.TrimSpace(req.Name), req.Region, req.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		if err := auditPool(r, tx, tenantID, "node_pool.updated", id,
			map[string]any{"name": req.Name, "status": req.Status}); err != nil {
			return err
		}
		// UPDATE 已锁住这一行，同一个池的两次名单替换在这里串行
		if withGroups {
			groupsChanged, err = replacePoolUserGroupsTx(r.Context(), tx, tenantID, id, groupIDs)
		}
		return err
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if groupsChanged {
		h.notifyNodeUsersChanged(r)
	}
	httpx.OK(w, map[string]any{"ok": true})
}

func (h *handlers) deleteNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(),
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			"node-config-release/"+tenantID); err != nil {
			return err
		}
		// Lock the parent before checking dependencies. Node creation/clone and
		// config publication take a compatible SHARE lock, so an ON DELETE SET
		// NULL cascade cannot slip between count=0 and DELETE and bypass the
		// pre-00048 pool-move freeze.
		var lockedID string
		if err := tx.QueryRow(r.Context(), `
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
		if err := tx.QueryRow(r.Context(), `
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
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM node_pools WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		return auditPool(r, tx, tenantID, "node_pool.deleted", id, nil)
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// assignNodePool 把一个节点归到某个分组。
func (h *handlers) assignNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	nodeID := chi.URLParam(r, "id")
	var req struct {
		PoolID string `json:"pool_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.PoolID = strings.TrimSpace(req.PoolID)
	if req.PoolID != "" {
		if _, err := uuid.Parse(req.PoolID); err != nil {
			httpx.Fail(w, r, h.d.Log,
				httpx.Invalid(map[string]string{"pool_id": "必须是 UUID"}))
			return
		}
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var current *string
		if err := tx.QueryRow(r.Context(), `
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
		if currentID != req.PoolID {
			return httpx.New(httpx.CodeConflict,
				"配置发布身份升级完成前暂不允许移动节点分组")
		}
		return nil // same-pool idempotent replay
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// planPools returns the mutable draft bindings when a draft exists. Without a
// draft it returns the current published snapshot as explicitly read-only.
func (h *handlers) planPools(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	planID := chi.URLParam(r, "id")
	if _, err := uuid.Parse(planID); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	type opt struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Active int    `json:"active_nodes"`
		Bound  bool   `json:"bound"`
	}
	out := []opt{}
	var versionID, versionStatus string
	var versionRowVersion int64
	var editable bool

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var currentVersionID string
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(current_version_id::text, '')
			  FROM plans WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, planID).
			Scan(&currentVersionID); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		err := tx.QueryRow(r.Context(), `
			SELECT id::text, status, row_version
			  FROM plan_versions
			 WHERE tenant_id=$1 AND plan_id=$2::uuid
			   AND status='draft' AND frozen_at IS NULL
			 ORDER BY version DESC LIMIT 1`, tenantID, planID).
			Scan(&versionID, &versionStatus, &versionRowVersion)
		if err == nil {
			editable = true
		} else if err != pgx.ErrNoRows {
			return err
		} else if currentVersionID != "" {
			if err := tx.QueryRow(r.Context(), `
				SELECT id::text, status, row_version
				  FROM plan_versions
				 WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid`,
				tenantID, planID, currentVersionID).
				Scan(&versionID, &versionStatus, &versionRowVersion); err != nil {
				return err
			}
		}

		var versionArg any
		if versionID != "" {
			versionArg = versionID
		}
		rows, err := tx.Query(r.Context(), `
			SELECT p.id::text, p.name,
			       (SELECT count(*) FROM nodes n
			         WHERE n.pool_id=p.id AND n.status='active' AND n.node_type IS NOT NULL),
			       EXISTS (SELECT 1 FROM plan_node_pools pnp
			                WHERE pnp.tenant_id=$1 AND pnp.pool_id=p.id
			                  AND pnp.plan_version_id=$2::uuid)
			  FROM node_pools p
			 WHERE p.tenant_id=$1 AND p.status<>'disabled'
			 ORDER BY p.name`, tenantID, versionArg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o opt
			if err := rows.Scan(&o.ID, &o.Name, &o.Active, &o.Bound); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"version_id": versionID, "version_status": versionStatus,
		"row_version": versionRowVersion, "editable": editable, "pools": out,
	})
}

type setPlanPoolsReq struct {
	VersionID                 string   `json:"version_id"`
	ExpectedVersionRowVersion int64    `json:"expected_version_row_version"`
	PoolIDs                   []string `json:"pool_ids"`
}

func validateSetPlanPoolsRequest(planID string, req setPlanPoolsReq) ([]string, error) {
	if _, err := uuid.Parse(planID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	fields := map[string]string{}
	if _, err := uuid.Parse(req.VersionID); err != nil {
		fields["version_id"] = "必须是有效 UUID"
	}
	if req.ExpectedVersionRowVersion <= 0 {
		fields["expected_version_row_version"] = "必须是正整数"
	}
	if len(req.PoolIDs) > 500 {
		fields["pool_ids"] = "一次最多绑定 500 个节点分组"
	}
	poolIDs := append([]string(nil), req.PoolIDs...)
	sort.Strings(poolIDs)
	for i, id := range poolIDs {
		if _, err := uuid.Parse(id); err != nil || (i > 0 && id == poolIDs[i-1]) {
			fields["pool_ids"] = "必须是无重复的 UUID 列表"
			break
		}
	}
	if len(fields) != 0 {
		return nil, httpx.Invalid(fields)
	}
	return poolIDs, nil
}

func validateEditablePlanPoolVersion(status string, frozen bool, current, expected int64) error {
	if status != "draft" || frozen {
		return httpx.New(httpx.CodeConflict, "只有未发布的草稿版本可以修改节点分组")
	}
	if current != expected {
		return &httpx.Error{
			Code: httpx.CodeConflict, Message: "套餐版本已被其他管理员修改，请刷新后重试",
			Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
		}
	}
	return nil
}

// setPlanPools atomically replaces a draft plan version's pool bindings.
func (h *handlers) setPlanPools(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	planID := chi.URLParam(r, "id")
	var req setPlanPoolsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	poolIDs, err := validateSetPlanPoolsRequest(planID, req)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	actorID := httpx.PrincipalFrom(r.Context()).UserID
	var next int64
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status string
		var frozen bool
		var current int64
		if err := tx.QueryRow(r.Context(), `
			SELECT pv.status, pv.frozen_at IS NOT NULL, pv.row_version
			  FROM plan_versions pv
			  JOIN plans p ON p.tenant_id=pv.tenant_id AND p.id=pv.plan_id
			 WHERE pv.tenant_id=$1 AND pv.plan_id=$2::uuid AND pv.id=$3::uuid
			 FOR UPDATE OF pv`, tenantID, planID, req.VersionID).
			Scan(&status, &frozen, &current); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if err := validateEditablePlanPoolVersion(status, frozen, current, req.ExpectedVersionRowVersion); err != nil {
			return err
		}

		before := []string{}
		rows, err := tx.Query(r.Context(), `
			SELECT pool_id::text FROM plan_node_pools
			 WHERE tenant_id=$1 AND plan_version_id=$2::uuid ORDER BY pool_id`,
			tenantID, req.VersionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			before = append(before, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// Deterministic locking prevents opposite request orders from deadlocking.
		for _, pid := range poolIDs {
			var locked string
			if err := tx.QueryRow(r.Context(), `
				SELECT id::text FROM node_pools
				 WHERE tenant_id=$1 AND id=$2::uuid AND status<>'disabled'
				 FOR KEY SHARE`, tenantID, pid).Scan(&locked); err != nil {
				if err == pgx.ErrNoRows {
					return httpx.Invalid(map[string]string{
						"pool_ids": "包含不存在或已禁用的节点分组",
					})
				}
				return err
			}
		}

		if _, err := tx.Exec(r.Context(), `
			DELETE FROM plan_node_pools
			 WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, req.VersionID); err != nil {
			return err
		}
		for _, pid := range poolIDs {
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id)
				VALUES ($1, $2::uuid, $3::uuid)`, tenantID, req.VersionID, pid); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(r.Context(), `
			UPDATE plan_versions SET row_version=row_version+1
			 WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid AND row_version=$4`,
			tenantID, planID, req.VersionID, req.ExpectedVersionRowVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return &httpx.Error{
				Code: httpx.CodeConflict, Message: "套餐版本已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
			}
		}
		next = current + 1
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "plan_version.pools_changed", ResourceType: "plan_version", ResourceID: &req.VersionID,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(r.Context()),
			BeforeDigest: map[string]any{"row_version": current, "pool_ids": before},
			AfterDigest:  map[string]any{"row_version": next, "pool_ids": poolIDs, "plan_id": planID},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyNodeUsersChanged(r)
	httpx.OK(w, map[string]any{
		"bound": len(poolIDs), "row_version": next, "version_id": req.VersionID,
	})
}

// notifyNodeUsersChanged 在改变交付集合的写操作提交后，发一次租户级
// node.users.changed，让节点立即重拉用户（R104）；只在事务成功后调，
// 失败的请求什么都没改，不该惊动节点。推送尽力而为，节点端轮询兜底。
func (h *handlers) notifyNodeUsersChanged(r *http.Request) {
	if h.d.Node != nil {
		h.d.Node.NotifyUsersChanged(r.Context(), httpx.TenantIDFrom(r.Context()))
	}
}

func auditPool(r *http.Request, tx pgx.Tx, tenantID, action, resourceID string,
	after map[string]any) error {
	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}
	return audit.Write(r.Context(), tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: actorID,
		Action: action, ResourceType: "node_pool", ResourceID: &resourceID,
		APIDomain: "admin", Outcome: "success",
		RequestID: httpx.RequestIDFrom(r.Context()), AfterDigest: after,
	})
}

// slugify 从名字派生一个稳定标识。
//
// 中文名派生不出可读的 slug，这时退回时间戳 —— 标识只要唯一稳定就够了，
// 好不好看是次要的，而让管理员为了建个分组先想一个英文代号是多余的负担。
func slugify(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "pool"
		}
		return "pool-" + hex.EncodeToString(buf)
	}
	return s
}
