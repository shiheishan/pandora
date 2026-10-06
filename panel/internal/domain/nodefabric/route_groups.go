// [INPUT]: 依赖 config_publish.go 的 lockLegacyConfigRelease，依赖 routing_refs.go 的悬空引用校验，依赖 routing_merge.go 的 groupOrder，依赖 platform 的 db/audit/httpx；读写 route_groups / route_group_members / nodes（00096）
// [OUTPUT]: 对外提供 RouteGroup / RouteGroupMember / RouteGroupRef、RouteGroupWrite，Service 的 ListRouteGroups / CreateRouteGroup / UpdateRouteGroup / DeleteRouteGroup 及其输入类型；包内 lockRouteGroupTx、bumpRoutingNodesTx、parseRouteGroupID
// [POS]: domain/nodefabric 的路由组本身（元信息、列表、增删改）：组是 node_outbounds / node_routes 的第三个范围，组内路由与成员的写在 route_group_routing.go；凡改变成员节点生效配置的写（改组序、删组）都持 node-config-release 锁、同事务推进成员节点 generation，返回节点由 handler 提交后通知

package nodefabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// RouteGroupMember 是组里的一个节点。
type RouteGroupMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RouteGroupRef 是节点所在的一个组，按生效顺序排列。
type RouteGroupRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// RouteGroup 是后台看到的一个路由组。
type RouteGroup struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	SortOrder     int                `json:"sort_order"`
	RowVersion    int64              `json:"row_version"`
	OutboundCount int                `json:"outbound_count"`
	RuleCount     int                `json:"rule_count"`
	Members       []RouteGroupMember `json:"members"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// RouteGroupWrite 是改变了节点生效配置的组写操作的结果：新的行版本（组或节点的），
// 以及 generation 被推进、需要在提交后通知的节点。
type RouteGroupWrite struct {
	RowVersion int64
	NodeIDs    []string
}

// parseRouteGroupID 把路径里的 id 规范成 uuid 文本；不是 uuid 回中性 404，不让它落到 SQL 报 500。
func parseRouteGroupID(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", httpx.NotFoundOrForbidden()
	}
	return parsed.String(), nil
}

func routeGroupConflict(current int64) error {
	return &httpx.Error{Code: httpx.CodeConflict, Message: "路由组已被其他管理员修改，请刷新后重试",
		Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)}}
}

// lockRouteGroupTx 锁住组行并核对行版本。调用方已持 node-config-release 锁。
func lockRouteGroupTx(ctx context.Context, tx pgx.Tx, tenantID, groupID string, want int64) error {
	var current int64
	if err := tx.QueryRow(ctx, `SELECT row_version FROM route_groups
		WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, groupID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	}
	if current != want {
		return routeGroupConflict(current)
	}
	return nil
}

// bumpRoutingNodesTx 推进节点的 config_source_generation，让有效发布物重新物化；
// withRowVersion 时连节点行版本一起推（成员关系是节点自己的属性，节点抽屉拿行版本做乐观并发）。
// 已退役 / 已销毁的节点不推 generation，也不回给调用方通知，与全局发布同一口径。
func bumpRoutingNodesTx(ctx context.Context, tx pgx.Tx, tenantID string, nodeIDs []string, withRowVersion bool) ([]string, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		UPDATE nodes SET
		       config_source_generation = config_source_generation +
		         CASE WHEN status NOT IN ('destroyed','retired') AND serving_status <> 'retired' THEN 1 ELSE 0 END,
		       row_version = row_version + CASE WHEN $3 THEN 1 ELSE 0 END
		 WHERE tenant_id = $1 AND id = ANY($2::uuid[])
		RETURNING id::text, (status NOT IN ('destroyed','retired') AND serving_status <> 'retired')`,
		tenantID, nodeIDs, withRowVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var live []string
	for rows.Next() {
		var id string
		var serving bool
		if err := rows.Scan(&id, &serving); err != nil {
			return nil, err
		}
		if serving {
			live = append(live, id)
		}
	}
	return live, rows.Err()
}

// groupMemberIDsTx 取组的成员节点 id。
func groupMemberIDsTx(ctx context.Context, tx pgx.Tx, tenantID, groupID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT node_id::text FROM route_group_members
		WHERE tenant_id = $1 AND group_id = $2::uuid ORDER BY node_id`, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

//------------------------------------------------------------------------------
// 读
//------------------------------------------------------------------------------

const routeGroupColumns = `g.id::text, g.name, g.description, g.sort_order, g.row_version, g.created_at, g.updated_at,
	(SELECT count(*) FROM node_outbounds o WHERE o.tenant_id = g.tenant_id AND o.group_id = g.id),
	(SELECT count(*) FROM node_routes r WHERE r.tenant_id = g.tenant_id AND r.group_id = g.id)`

func scanRouteGroups(ctx context.Context, tx pgx.Tx, tenantID, where string, args ...any) ([]RouteGroup, error) {
	rows, err := tx.Query(ctx, `SELECT `+routeGroupColumns+` FROM route_groups g
		WHERE g.tenant_id = $1`+where+` ORDER BY `+groupOrder, append([]any{tenantID}, args...)...)
	if err != nil {
		return nil, err
	}
	out := []RouteGroup{}
	at := map[string]int{}
	for rows.Next() {
		g := RouteGroup{Members: []RouteGroupMember{}}
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.SortOrder, &g.RowVersion,
			&g.CreatedAt, &g.UpdatedAt, &g.OutboundCount, &g.RuleCount); err != nil {
			rows.Close()
			return nil, err
		}
		at[g.ID] = len(out)
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mrows, err := tx.Query(ctx, `
		SELECT m.group_id::text, n.id::text, n.name
		  FROM route_group_members m
		  JOIN nodes n ON n.tenant_id = m.tenant_id AND n.id = m.node_id
		 WHERE m.tenant_id = $1
		 ORDER BY n.sort_order, n.node_no`, tenantID)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var groupID string
		var m RouteGroupMember
		if err := mrows.Scan(&groupID, &m.ID, &m.Name); err != nil {
			return nil, err
		}
		if i, ok := at[groupID]; ok {
			out[i].Members = append(out[i].Members, m)
		}
	}
	return out, mrows.Err()
}

// ListRouteGroups 按生效顺序列出租户的路由组，带成员与出站 / 规则条数。
func (s *Service) ListRouteGroups(ctx context.Context, tenantID string) ([]RouteGroup, error) {
	var out []RouteGroup
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		out, err = scanRouteGroups(ctx, tx, tenantID, "")
		return err
	})
	return out, err
}

func getRouteGroupTx(ctx context.Context, tx pgx.Tx, tenantID, groupID string) (*RouteGroup, error) {
	groups, err := scanRouteGroups(ctx, tx, tenantID, ` AND g.id = $2::uuid`, groupID)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, httpx.NotFoundOrForbidden()
	}
	return &groups[0], nil
}

//------------------------------------------------------------------------------
// 写：新建、改元信息、删除
//------------------------------------------------------------------------------

// maxRouteGroupSortOrder 限住组序的取值，远在 int4 之内，后台排序用不到更大的数
const maxRouteGroupSortOrder = 1_000_000

// normalizeRouteGroupFields 校验并规范名称、说明与组序（只校验传了的）。
func normalizeRouteGroupFields(name, description *string, sortOrder *int) error {
	if sortOrder != nil && (*sortOrder < -maxRouteGroupSortOrder || *sortOrder > maxRouteGroupSortOrder) {
		return httpx.Invalid(map[string]string{"sort_order": fmt.Sprintf("排序取值 %d 到 %d", -maxRouteGroupSortOrder, maxRouteGroupSortOrder)})
	}
	if name != nil {
		*name = strings.TrimSpace(*name)
		if n := utf8.RuneCountInString(*name); n < 1 || n > 64 {
			return httpx.Invalid(map[string]string{"name": "名称为 1 到 64 个字符"})
		}
	}
	if description != nil {
		*description = strings.TrimSpace(*description)
		if utf8.RuneCountInString(*description) > 500 {
			return httpx.Invalid(map[string]string{"description": "说明最多 500 个字符"})
		}
	}
	return nil
}

func routeGroupWriteError(err error) error {
	if db.IsUniqueViolation(err) {
		return httpx.New(httpx.CodeConflict, "路由组名称已存在")
	}
	return err
}

// CreateRouteGroupInput 是新建路由组的请求。
type CreateRouteGroupInput struct {
	TenantID, ActorID string
	Name, Description string
	SortOrder         int
}

// CreateRouteGroup 新建一个空组。空组不影响任何节点，不推进 generation。
func (s *Service) CreateRouteGroup(ctx context.Context, in CreateRouteGroupInput) (*RouteGroup, error) {
	if err := normalizeRouteGroupFields(&in.Name, &in.Description, &in.SortOrder); err != nil {
		return nil, err
	}
	var out *RouteGroup
	err := s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO route_groups (tenant_id, name, description, sort_order)
			VALUES ($1, $2, $3, $4) RETURNING id::text`,
			in.TenantID, in.Name, in.Description, in.SortOrder).Scan(&id); err != nil {
			return routeGroupWriteError(err)
		}
		var err error
		if out, err = getRouteGroupTx(ctx, tx, in.TenantID, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "route_group.create", ResourceType: "route_group", ResourceID: &id,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"name": in.Name, "sort_order": in.SortOrder},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateRouteGroupInput 是改组元信息的请求，nil 字段不改。
type UpdateRouteGroupInput struct {
	TenantID, ActorID, GroupID string
	RowVersion                 int64
	Name, Description          *string
	SortOrder                  *int
}

// UpdateRouteGroup 改名称、说明或组序。组序决定多组节点的合并顺序，变了就推进成员节点。
func (s *Service) UpdateRouteGroup(ctx context.Context, in UpdateRouteGroupInput) (*RouteGroup, []string, error) {
	groupID, err := parseRouteGroupID(in.GroupID)
	if err != nil {
		return nil, nil, err
	}
	if in.RowVersion <= 0 {
		return nil, nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if err := normalizeRouteGroupFields(in.Name, in.Description, in.SortOrder); err != nil {
		return nil, nil, err
	}
	var out *RouteGroup
	var notify []string
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		if err := lockRouteGroupTx(ctx, tx, in.TenantID, groupID, in.RowVersion); err != nil {
			return err
		}
		before, err := getRouteGroupTx(ctx, tx, in.TenantID, groupID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE route_groups SET
			name = coalesce($3, name), description = coalesce($4, description),
			sort_order = coalesce($5, sort_order), row_version = row_version + 1
			WHERE tenant_id = $1 AND id = $2::uuid`,
			in.TenantID, groupID, in.Name, in.Description, in.SortOrder); err != nil {
			return routeGroupWriteError(err)
		}
		if in.SortOrder != nil && *in.SortOrder != before.SortOrder {
			members, err := groupMemberIDsTx(ctx, tx, in.TenantID, groupID)
			if err != nil {
				return err
			}
			if notify, err = bumpRoutingNodesTx(ctx, tx, in.TenantID, members, false); err != nil {
				return err
			}
		}
		if out, err = getRouteGroupTx(ctx, tx, in.TenantID, groupID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "route_group.update", ResourceType: "route_group", ResourceID: &groupID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": before.Name, "sort_order": before.SortOrder},
			AfterDigest:  map[string]any{"name": out.Name, "sort_order": out.SortOrder, "affected_nodes": len(notify)},
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return out, notify, nil
}

// DeleteRouteGroupInput 是删组请求。
type DeleteRouteGroupInput struct {
	TenantID, ActorID, GroupID string
	RowVersion                 int64
}

// DeleteRouteGroup 删组：组内出站、规则与成员行随外键级联删除，成员节点同事务推进
// generation 并由调用方在提交后通知。成员节点的私有规则若还指向组内出站，拒绝并列出。
func (s *Service) DeleteRouteGroup(ctx context.Context, in DeleteRouteGroupInput) ([]string, error) {
	groupID, err := parseRouteGroupID(in.GroupID)
	if err != nil {
		return nil, err
	}
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	var notify []string
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		if err := lockRouteGroupTx(ctx, tx, in.TenantID, groupID, in.RowVersion); err != nil {
			return err
		}
		group, err := getRouteGroupTx(ctx, tx, in.TenantID, groupID)
		if err != nil {
			return err
		}
		members, err := groupMemberIDsTx(ctx, tx, in.TenantID, groupID)
		if err != nil {
			return err
		}
		before, err := danglingRefsTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM route_groups WHERE tenant_id = $1 AND id = $2::uuid`,
			in.TenantID, groupID); err != nil {
			return err
		}
		if err := refuseNewDanglingTx(ctx, tx, in.TenantID, before, "组内出站仍被成员节点的规则引用"); err != nil {
			return err
		}
		if notify, err = bumpRoutingNodesTx(ctx, tx, in.TenantID, members, true); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "route_group.delete", ResourceType: "route_group", ResourceID: &groupID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": group.Name, "members": len(members),
				"outbounds": group.OutboundCount, "routes": group.RuleCount},
			AfterDigest: map[string]any{"affected_nodes": len(notify)},
		})
	})
	if err != nil {
		return nil, err
	}
	return notify, nil
}
