// [INPUT]: 依赖 platform 的 audit/httpx，读写 node_pool_user_groups（00093），读 user_groups
// [OUTPUT]: 对外提供 handlers 内部用的 namedRef、requirePoolGroupsReauth、normalizePoolUserGroupIDs、replacePoolUserGroupsTx
// [POS]: api/admin 节点分组的「仅限用户组」名单（R104）：pools.go 的新建 / 编辑在请求带了 allowed_user_group_ids 时经这里做字段级 reauth、校验与整体替换；下发规则本身在 nodefabric.PoolAdmitsUserSQL
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 节点池的用户组限定名单。
//
// 名单决定「谁能连这组节点」，改它等于改交付集合：漏一个组就是一批用户
// 断网，多一个组就是有人连上了专属线路。所以三件事都比改池名严格：
// 带了这个字段就要近期重认证（路由上的 reauth 中间件只能整条挂，这里按
// 字段挂，不带字段的改名照旧不用重认证）、写一条前后对照的审计、名单真的
// 变了才在提交后通知节点重拉用户。

import (
	"context"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// namedRef 是列表里互相引用的 { id, name }：池的 allowed_user_groups、组的 exclusive_pools。
type namedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// maxPoolUserGroups 是一个池的名单上限。名单是「少数专属组」，上百个组的
// 限定说明该用可见性而不是线路来分，也免得一次请求拼出过大的数组。
const maxPoolUserGroups = 100

// requirePoolGroupsReauth 在请求带了 allowed_user_group_ids 时要求近期重认证，
// 与 middleware.RequireRecentReauth 同一个错误码与文案，前端的常驻对话框
// 据此弹框后用原请求重放。
func requirePoolGroupsReauth(r *http.Request, present bool) error {
	if !present {
		return nil
	}
	if p := httpx.PrincipalFrom(r.Context()); p == nil || !p.ReauthedRecently {
		return httpx.New(httpx.CodeReauthRequired, "此操作需要重新验证身份")
	}
	return nil
}

// normalizePoolUserGroupIDs 校验格式与重复并排序；存在性与租户归属在事务里查。
func normalizePoolUserGroupIDs(ids []string) ([]string, error) {
	invalid := func(msg string) error {
		return httpx.Invalid(map[string]string{"allowed_user_group_ids": msg})
	}
	if len(ids) > maxPoolUserGroups {
		return nil, invalid("一个节点池最多限定 100 个用户组")
	}
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, invalid("必须是无重复的用户组 UUID 列表")
		}
		out = append(out, id.String())
	}
	slices.Sort(out)
	if len(slices.Compact(slices.Clone(out))) != len(out) {
		return nil, invalid("必须是无重复的用户组 UUID 列表")
	}
	return out, nil
}

// replacePoolUserGroupsTx 把池的名单整体替换成 ids（已规范化；空 = 取消限定），
// 名单有变化时写审计并返回 changed=true。调用方须已持有该池的行锁（新建的
// 行或 UPDATE 过的行），同一个池的两次替换因此串行。
func replacePoolUserGroupsTx(ctx context.Context, tx pgx.Tx, tenantID, poolID string, ids []string) (bool, error) {
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

	var actorID *string
	if a := httpx.PrincipalFrom(ctx); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}
	return true, audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: actorID,
		Action: "node_pool.user_groups_changed", ResourceType: "node_pool", ResourceID: &poolID,
		APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
		BeforeDigest: map[string]any{"allowed_user_group_ids": before},
		AfterDigest:  map[string]any{"allowed_user_group_ids": ids},
	})
}
