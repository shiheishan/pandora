package nodefabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 组内出站与规则（GET / PUT v1/route-groups/{id}/routing）
//------------------------------------------------------------------------------

// GroupRouting 是某路由组的出站与规则，RowVersion 为组行版本。
type GroupRouting struct {
	RowVersion int64             `json:"row_version"`
	Outbounds  []RoutingOutbound `json:"outbounds"`
	Routes     []RoutingRule     `json:"routes"`
}

// GetGroupRouting 读取某路由组的出站与规则。
func (s *Service) GetGroupRouting(ctx context.Context, tenantID, groupID string) (*GroupRouting, error) {
	groupID, err := parseRouteGroupID(groupID)
	if err != nil {
		return nil, err
	}
	out := &GroupRouting{}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT row_version FROM route_groups
			WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, groupID).Scan(&out.RowVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		var err error
		out.Outbounds, out.Routes, err = loadScopeRoutingTx(ctx, tx, tenantID, routingScope{GroupID: groupID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetGroupRoutingInput 是组内路由的整体替换请求。
type SetGroupRoutingInput struct {
	TenantID, ActorID, GroupID string
	RowVersion                 int64
	Outbounds                  []RoutingOutbound
	Routes                     []RoutingRule
}

// SetGroupRouting 全量替换某路由组的出站与规则，推进全部成员节点。
// 组规则只能指向内置、本组与全局出站：节点私有出站不在组内每个节点上。
func (s *Service) SetGroupRouting(ctx context.Context, in SetGroupRoutingInput) (*RouteGroupWrite, error) {
	groupID, err := parseRouteGroupID(in.GroupID)
	if err != nil {
		return nil, err
	}
	if in.Outbounds == nil {
		in.Outbounds = []RoutingOutbound{}
	}
	if in.Routes == nil {
		in.Routes = []RoutingRule{}
	}
	tags, err := ValidateRoutingPayload(in.Outbounds, in.Routes)
	if err != nil {
		return nil, err
	}
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	out := &RouteGroupWrite{RowVersion: in.RowVersion + 1}
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		if err := lockRouteGroupTx(ctx, tx, in.TenantID, groupID, in.RowVersion); err != nil {
			return err
		}
		global, err := visibleOutboundTagsTx(ctx, tx, in.TenantID, "")
		if err != nil {
			return err
		}
		if err := checkRouteRefs(in.Routes, tags, global); err != nil {
			return err
		}
		before, err := danglingRefsTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		if err := replaceScopeRoutingTx(ctx, tx, in.TenantID, routingScope{GroupID: groupID}, in.Outbounds, in.Routes); err != nil {
			return err
		}
		if err := refuseNewDanglingTx(ctx, tx, in.TenantID, before, "要删除的组内出站仍被成员节点的规则引用"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE route_groups SET row_version = row_version + 1
			WHERE tenant_id = $1 AND id = $2::uuid`, in.TenantID, groupID); err != nil {
			return err
		}
		members, err := groupMemberIDsTx(ctx, tx, in.TenantID, groupID)
		if err != nil {
			return err
		}
		if out.NodeIDs, err = bumpRoutingNodesTx(ctx, tx, in.TenantID, members, false); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.routing.group_publish", ResourceType: "route_group", ResourceID: &groupID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"outbounds": len(in.Outbounds), "routes": len(in.Routes),
				"affected_nodes": len(out.NodeIDs)},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//------------------------------------------------------------------------------
// 成员：组侧（PUT v1/route-groups/{id}/members）与节点侧（PUT v1/nodes/{id}/route-groups）
//------------------------------------------------------------------------------

// normalizeIDs 校验一组 uuid、规范大小写并去重排序；field 是出错时的字段名。
func normalizeIDs(ids []string, field string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range ids {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return nil, httpx.Invalid(map[string]string{field: "包含无效的 id：" + raw})
		}
		id := parsed.String()
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// symmetricDiff 返回只在 a 或只在 b 里的元素（两边都已排序去重）。
func symmetricDiff(a, b []string) []string {
	in := map[string]int{}
	for _, x := range a {
		in[x]++
	}
	for _, x := range b {
		in[x]++
	}
	var out []string
	for x, n := range in {
		if n == 1 {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// requireExistingTx 要求 ids 都是 table 里本租户的行，缺的回 422 列出。
func requireExistingTx(ctx context.Context, tx pgx.Tx, tenantID, table, field, label string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM `+table+` WHERE tenant_id = $1 AND id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if missing := symmetricDiff(ids, found); len(missing) > 0 {
		return httpx.Invalid(map[string]string{field: label + "不存在：" + strings.Join(missing, "、")})
	}
	return nil
}

// SetRouteGroupMembersInput 是从组侧整体替换成员的请求。
type SetRouteGroupMembersInput struct {
	TenantID, ActorID, GroupID string
	RowVersion                 int64
	NodeIDs                    []string
}

// SetRouteGroupMembers 整体替换组的成员。进出组的节点推进 generation 与行版本
// （成员关系是节点的属性，节点抽屉的并发保护靠节点行版本），留在组里的节点配置不变、不推。
func (s *Service) SetRouteGroupMembers(ctx context.Context, in SetRouteGroupMembersInput) (*RouteGroupWrite, error) {
	groupID, err := parseRouteGroupID(in.GroupID)
	if err != nil {
		return nil, err
	}
	nodeIDs, err := normalizeIDs(in.NodeIDs, "node_ids")
	if err != nil {
		return nil, err
	}
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	out := &RouteGroupWrite{RowVersion: in.RowVersion + 1}
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		if err := lockRouteGroupTx(ctx, tx, in.TenantID, groupID, in.RowVersion); err != nil {
			return err
		}
		if err := requireExistingTx(ctx, tx, in.TenantID, "nodes", "node_ids", "节点", nodeIDs); err != nil {
			return err
		}
		current, err := groupMemberIDsTx(ctx, tx, in.TenantID, groupID)
		if err != nil {
			return err
		}
		before, err := danglingRefsTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM route_group_members WHERE tenant_id = $1 AND group_id = $2::uuid`,
			in.TenantID, groupID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO route_group_members (tenant_id, group_id, node_id)
			SELECT $1, $2::uuid, unnest($3::uuid[])`, in.TenantID, groupID, nodeIDs); err != nil {
			return err
		}
		if err := refuseNewDanglingTx(ctx, tx, in.TenantID, before, "移出组的节点仍有规则指向组内出站"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE route_groups SET row_version = row_version + 1
			WHERE tenant_id = $1 AND id = $2::uuid`, in.TenantID, groupID); err != nil {
			return err
		}
		changed := symmetricDiff(current, nodeIDs)
		if out.NodeIDs, err = bumpRoutingNodesTx(ctx, tx, in.TenantID, changed, true); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "route_group.members_update", ResourceType: "route_group", ResourceID: &groupID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"members": current},
			AfterDigest:  map[string]any{"members": nodeIDs, "affected_nodes": len(out.NodeIDs)},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nodeRouteGroupsTx 取节点所在的组，按生效顺序。
func nodeRouteGroupsTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]RouteGroupRef, error) {
	rows, err := tx.Query(ctx, `
		SELECT g.id::text, g.name, g.sort_order
		  FROM route_group_members m
		  JOIN route_groups g ON g.tenant_id = m.tenant_id AND g.id = m.group_id
		 WHERE m.tenant_id = $1 AND m.node_id = $2::uuid
		 ORDER BY `+groupOrder, tenantID, nodeID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RouteGroupRef, error) {
		var g RouteGroupRef
		err := row.Scan(&g.ID, &g.Name, &g.SortOrder)
		return g, err
	})
	if out == nil {
		out = []RouteGroupRef{}
	}
	return out, err
}

// SetNodeRouteGroupsInput 是从节点侧整体替换所在组的请求，RowVersion 为节点行版本。
type SetNodeRouteGroupsInput struct {
	TenantID, ActorID, NodeID string
	RowVersion                int64
	GroupIDs                  []string
}

// SetNodeRouteGroups 整体替换节点所在的组，返回新的节点行版本。进出的组推进组行版本，
// 与组侧改成员互相可见：任何一侧拿着旧版本写都会 409。
func (s *Service) SetNodeRouteGroups(ctx context.Context, in SetNodeRouteGroupsInput) (*RouteGroupWrite, error) {
	nodeID, err := uuid.Parse(in.NodeID)
	if err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	groupIDs, err := normalizeIDs(in.GroupIDs, "group_ids")
	if err != nil {
		return nil, err
	}
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	id := nodeID.String()
	out := &RouteGroupWrite{RowVersion: in.RowVersion + 1}
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// 与组侧写同一把发布锁：两侧锁行的顺序相反（组 → 节点 / 节点 → 组），先串行再锁行
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		var current int64
		if err := tx.QueryRow(ctx, `SELECT row_version FROM nodes
			WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, in.TenantID, id).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if current != in.RowVersion {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)}}
		}
		if err := requireExistingTx(ctx, tx, in.TenantID, "route_groups", "group_ids", "路由组", groupIDs); err != nil {
			return err
		}
		refs, err := nodeRouteGroupsTx(ctx, tx, in.TenantID, id)
		if err != nil {
			return err
		}
		var was []string
		for _, g := range refs {
			was = append(was, g.ID)
		}
		sort.Strings(was)
		changed := symmetricDiff(was, groupIDs)
		if len(changed) > 0 {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM route_groups WHERE tenant_id = $1 AND id = ANY($2::uuid[])
				ORDER BY id FOR UPDATE`, in.TenantID, changed); err != nil {
				return err
			}
		}
		before, err := danglingRefsTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM route_group_members WHERE tenant_id = $1 AND node_id = $2::uuid`,
			in.TenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO route_group_members (tenant_id, group_id, node_id)
			SELECT $1, unnest($3::uuid[]), $2::uuid`, in.TenantID, id, groupIDs); err != nil {
			return err
		}
		if err := refuseNewDanglingTx(ctx, tx, in.TenantID, before, "本节点仍有规则指向要退出的组的出站"); err != nil {
			return err
		}
		if len(changed) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE route_groups SET row_version = row_version + 1
				WHERE tenant_id = $1 AND id = ANY($2::uuid[])`, in.TenantID, changed); err != nil {
				return err
			}
		}
		if out.NodeIDs, err = bumpRoutingNodesTx(ctx, tx, in.TenantID, []string{id}, true); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.route_groups_update", ResourceType: "node", ResourceID: &id,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"groups": was},
			AfterDigest:  map[string]any{"groups": groupIDs},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//------------------------------------------------------------------------------
// 生效结果预览（GET v1/nodes/{id}/routing/effective）
//------------------------------------------------------------------------------

// RoutingSource 标注一条生效出站或规则来自哪一层。
type RoutingSource struct {
	Scope     string `json:"scope"`
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

// EffectiveOutbound 是一条生效出站及其来源（同 tag 被覆盖时只剩胜出的那条）。
type EffectiveOutbound struct {
	NodeOutbound
	Source RoutingSource `json:"source"`
}

// EffectiveRoute 是一条生效规则（只含启用的）及其来源，顺序即匹配顺序。
type EffectiveRoute struct {
	NodeRoute
	Source RoutingSource `json:"source"`
}

// EffectiveRouting 是节点合并后的生效路由，与下发给节点的内容同一口径（routing_merge.go）。
type EffectiveRouting struct {
	Groups    []RouteGroupRef     `json:"groups"`
	Outbounds []EffectiveOutbound `json:"outbounds"`
	Routes    []EffectiveRoute    `json:"routes"`
}

// PreviewNodeRouting 只读预览节点的生效路由，带每条的来源。
func (s *Service) PreviewNodeRouting(ctx context.Context, tenantID, nodeID string) (*EffectiveRouting, error) {
	parsed, err := uuid.Parse(nodeID)
	if err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	nodeID = parsed.String()
	out := &EffectiveRouting{Groups: []RouteGroupRef{}, Outbounds: []EffectiveOutbound{}, Routes: []EffectiveRoute{}}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE tenant_id = $1 AND id = $2::uuid)`,
			tenantID, nodeID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NotFoundOrForbidden()
		}
		var err error
		if out.Groups, err = nodeRouteGroupsTx(ctx, tx, tenantID, nodeID); err != nil {
			return err
		}
		layers, err := loadNodeRoutingLayersTx(ctx, tx, tenantID, nodeID)
		if err != nil {
			return err
		}
		source := func(i int) RoutingSource {
			l := layers[i]
			return RoutingSource{Scope: l.Scope, GroupID: l.GroupID, GroupName: l.GroupName}
		}
		m := mergeRoutingLayers(layers)
		for _, o := range m.outbounds {
			out.Outbounds = append(out.Outbounds, EffectiveOutbound{o.NodeOutbound, source(o.layer)})
		}
		for _, r := range m.routes {
			out.Routes = append(out.Routes, EffectiveRoute{r.NodeRoute, source(r.layer)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
