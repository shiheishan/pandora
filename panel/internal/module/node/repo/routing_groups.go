// 路由组与成员的读写。
package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// RouteGroupRow 是后台列表里的一个路由组与它的出站、规则条数。
type RouteGroupRow struct {
	model.RouteGroup
	OutboundCount int `gorm:"column:outbound_count"`
	RuleCount     int `gorm:"column:rule_count"`
}

// RouteGroupMemberRow 是组里的一个未删除节点。
type RouteGroupMemberRow struct {
	GroupID uuid.UUID `gorm:"column:group_id"`
	NodeID  uuid.UUID `gorm:"column:node_id"`
	Name    string    `gorm:"column:name"`
}

// NodeGroupRow 是节点所在的一个组（按生效顺序）。
type NodeGroupRow struct {
	NodeID    uuid.UUID `gorm:"column:node_id"`
	GroupID   uuid.UUID `gorm:"column:group_id"`
	Name      string    `gorm:"column:name"`
	SortOrder int       `gorm:"column:sort_order"`
}

// 组间生效顺序，合并、预览、列表共用。
const routeGroupOrder = "g.sort_order, g.id"

// RouteGroups 列出路由组（id 非 nil 时只取该组）及出站、规则条数。
func (r *Repo) RouteGroups(ctx context.Context, id *uuid.UUID) ([]RouteGroupRow, error) {
	q := `SELECT g.id, g.name, g.description, g.sort_order, g.row_version, g.created_at, g.updated_at,
	       (SELECT count(*) FROM node_outbounds o WHERE o.group_id = g.id) AS outbound_count,
	       (SELECT count(*) FROM node_routes x WHERE x.group_id = g.id) AS rule_count
	  FROM route_groups g`
	var args []any
	if id != nil {
		q += ` WHERE g.id = ?`
		args = append(args, *id)
	}
	q += ` ORDER BY ` + routeGroupOrder
	var rows []RouteGroupRow
	if err := r.d.Conn(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		return []RouteGroupRow{}, nil
	}
	return rows, nil
}

// RouteGroupMembers 取若干组内的未删除节点成员。
func (r *Repo) RouteGroupMembers(ctx context.Context, groupIDs []uuid.UUID) ([]RouteGroupMemberRow, error) {
	if len(groupIDs) == 0 {
		return []RouteGroupMemberRow{}, nil
	}
	var rows []RouteGroupMemberRow
	err := r.d.Conn(ctx).Raw(`
		SELECT m.group_id, n.id AS node_id, n.name
		  FROM route_group_members m
		  JOIN nodes n ON n.id = m.node_id AND n.deleted_at IS NULL
		 WHERE m.group_id IN ?
		 ORDER BY n.sort_order, n.node_no`, groupIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []RouteGroupMemberRow{}, nil
	}
	return rows, nil
}

// RouteGroupForUpdate 按 id 锁住并读取一个路由组。
func (r *Repo) RouteGroupForUpdate(ctx context.Context, id uuid.UUID) (model.RouteGroup, bool, error) {
	var g model.RouteGroup
	err := r.d.Conn(ctx).Raw(
		`SELECT id, name, description, sort_order, row_version, created_at, updated_at
		 FROM route_groups WHERE id = ? FOR UPDATE`, id,
	).Scan(&g).Error
	if err != nil {
		return model.RouteGroup{}, false, err
	}
	if g.ID == uuid.Nil {
		return model.RouteGroup{}, false, nil
	}
	return g, true, nil
}

// InsertRouteGroup 插入路由组并回填 id、row_version、时间戳。
func (r *Repo) InsertRouteGroup(ctx context.Context, g *model.RouteGroup) error {
	var got struct {
		ID         uuid.UUID `gorm:"column:id"`
		RowVersion int64     `gorm:"column:row_version"`
		CreatedAt  time.Time `gorm:"column:created_at"`
		UpdatedAt  time.Time `gorm:"column:updated_at"`
	}
	err := r.d.Conn(ctx).Raw(
		`INSERT INTO route_groups (name, description, sort_order) VALUES (?, ?, ?)
		 RETURNING id, row_version, created_at, updated_at`,
		g.Name, g.Description, g.SortOrder,
	).Scan(&got).Error
	if err != nil {
		return routingErr(err)
	}
	g.ID, g.RowVersion, g.CreatedAt, g.UpdatedAt = got.ID, got.RowVersion, got.CreatedAt, got.UpdatedAt
	return nil
}

// UpdateRouteGroup 更新路由组元信息并推进 row_version。
func (r *Repo) UpdateRouteGroup(ctx context.Context, id uuid.UUID, name, description string, sortOrder int) error {
	if err := r.d.Conn(ctx).Exec(
		`UPDATE route_groups SET name = ?, description = ?, sort_order = ?,
		 row_version = row_version + 1, updated_at = now() WHERE id = ?`,
		name, description, sortOrder, id,
	).Error; err != nil {
		return routingErr(err)
	}
	return nil
}

// BumpRouteGroupVersions 推进若干路由组的 row_version。
func (r *Repo) BumpRouteGroupVersions(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return r.d.Conn(ctx).Exec(
		`UPDATE route_groups SET row_version = row_version + 1, updated_at = now() WHERE id IN ?`, ids,
	).Error
}

// DeleteRouteGroup 删除路由组（出站、规则、成员由外键级联）。
func (r *Repo) DeleteRouteGroup(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(`DELETE FROM route_groups WHERE id = ?`, id).Error
}

// GroupMemberIDs 返回组内全部 node_id（含软删节点）。
func (r *Repo) GroupMemberIDs(ctx context.Context, groupID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(
		`SELECT node_id FROM route_group_members WHERE group_id = ? ORDER BY node_id`, groupID,
	).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	return ids, nil
}

// uuidArray 把 uuid 切片编成 PostgreSQL uuid 数组字面量。
func uuidArray(ids []uuid.UUID) string {
	if len(ids) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String()
}

// ReplaceNodeGroups 整体替换一个节点所属的路由组列表。
func (r *Repo) ReplaceNodeGroups(ctx context.Context, nodeID uuid.UUID, groupIDs []uuid.UUID) error {
	if err := r.d.Conn(ctx).Exec(`DELETE FROM route_group_members WHERE node_id = ?`, nodeID).Error; err != nil {
		return err
	}
	if len(groupIDs) == 0 {
		return nil
	}
	return r.d.Conn(ctx).Exec(
		`INSERT INTO route_group_members (group_id, node_id) SELECT unnest(CAST(? AS uuid[])), ?`,
		uuidArray(groupIDs), nodeID,
	).Error
}

// NodeRouteGroups 批量查询节点所在的路由组（按生效顺序）。
func (r *Repo) NodeRouteGroups(ctx context.Context, nodeIDs []uuid.UUID) ([]NodeGroupRow, error) {
	if len(nodeIDs) == 0 {
		return []NodeGroupRow{}, nil
	}
	var rows []NodeGroupRow
	err := r.d.Conn(ctx).Raw(`
		SELECT m.node_id, g.id AS group_id, g.name, g.sort_order
		  FROM route_group_members m
		  JOIN route_groups g ON g.id = m.group_id
		 WHERE m.node_id IN ?
		 ORDER BY m.node_id, `+routeGroupOrder, nodeIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []NodeGroupRow{}, nil
	}
	return rows, nil
}

// ExistingRouteGroupIDs 返回 ids 里存在的路由组 id。
func (r *Repo) ExistingRouteGroupIDs(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return []uuid.UUID{}, nil
	}
	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(`SELECT id FROM route_groups WHERE id IN ?`, ids).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []uuid.UUID{}, nil
	}
	return out, nil
}

// LockRouteGroups 按 id 顺序锁住一批路由组。
func (r *Repo) LockRouteGroups(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	var discard []uuid.UUID
	return r.d.Conn(ctx).Raw(
		`SELECT id FROM route_groups WHERE id IN ? ORDER BY id FOR UPDATE`, ids,
	).Scan(&discard).Error
}
