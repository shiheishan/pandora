// 本文件归 p5-routing：出站与分流规则的范围读写、锁、悬空引用与计数。
package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

var (
	// ErrOutboundTagTaken 是同一范围内出站 tag 不分大小写重名（三个 node_outbounds_*_tag_key 之一）。
	ErrOutboundTagTaken = errors.New("node: 出站 tag 重复")
	// ErrRouteGroupNameTaken 是路由组重名（route_groups_name_key）。
	ErrRouteGroupNameTaken = errors.New("node: 路由组名称已存在")
	// ErrRoutingCheck 是写入不满足路由表的 CHECK 约束（23514）。
	ErrRoutingCheck = errors.New("node: 路由数据不满足库约束")
)

// routingErrs 把写路由相关表时的约束错误译成本包哨兵：路由组重名按约束名报，其余唯一冲突是出站 tag 重复。
var routingErrs = db.ConstraintErrors{
	ByCode: map[string]error{db.CodeUnique: ErrOutboundTagTaken, db.CodeCheck: ErrRoutingCheck},
	ByName: map[string]error{"route_groups_name_key": ErrRouteGroupNameTaken},
}

// routingErr 把写路由相关表时的约束错误译成本包哨兵。
func routingErr(err error) error { return routingErrs.Map(err) }

// RoutingScope 是 node_outbounds / node_routes 的一个范围：两个都 nil 是全局，至多一个非 nil。
type RoutingScope struct{ NodeID, GroupID *uuid.UUID }

func scopeCond(sc RoutingScope) (string, []any) {
	if sc.NodeID != nil {
		return "node_id = ?", []any{*sc.NodeID}
	}
	if sc.GroupID != nil {
		return "group_id = ?", []any{*sc.GroupID}
	}
	return "node_id IS NULL AND group_id IS NULL", nil
}

// LockRoutingWrites 串行化所有改路由的写：悬空引用检查要跨范围读，不串行会并发造出悬空引用。
func (r *Repo) LockRoutingWrites(ctx context.Context) error {
	return r.d.Conn(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended('node:routing', 0))",
	).Error
}

// ScopeRouting 读取一个范围内的全部出站与规则（未启用规则也返回）。
func (r *Repo) ScopeRouting(ctx context.Context, sc RoutingScope) ([]model.NodeOutbound, []model.NodeRoute, error) {
	cond, args := scopeCond(sc)
	var outs []model.NodeOutbound
	if err := r.d.Conn(ctx).Raw(
		`SELECT id, node_id, group_id, tag, type, settings::text AS settings, sort_order, created_at
		 FROM node_outbounds WHERE `+cond+` ORDER BY sort_order, tag`, args...,
	).Scan(&outs).Error; err != nil {
		return nil, nil, err
	}
	var routes []model.NodeRoute
	if err := r.d.Conn(ctx).Raw(
		`SELECT id, node_id, group_id, priority, sort_order, matcher::text AS matcher, outbound_tag, enabled, note, created_at
		 FROM node_routes WHERE `+cond+` ORDER BY priority, sort_order`, args...,
	).Scan(&routes).Error; err != nil {
		return nil, nil, err
	}
	if outs == nil {
		outs = []model.NodeOutbound{}
	}
	if routes == nil {
		routes = []model.NodeRoute{}
	}
	return outs, routes, nil
}

// ReplaceScopeRouting 整体替换一个范围内的出站与规则。
func (r *Repo) ReplaceScopeRouting(ctx context.Context, sc RoutingScope, outs []model.NodeOutbound, routes []model.NodeRoute) error {
	cond, args := scopeCond(sc)
	if err := r.d.Conn(ctx).Exec("DELETE FROM node_routes WHERE "+cond, args...).Error; err != nil {
		return err
	}
	if err := r.d.Conn(ctx).Exec("DELETE FROM node_outbounds WHERE "+cond, args...).Error; err != nil {
		return err
	}
	if len(outs) > 0 {
		if err := r.insertScopeOutbounds(ctx, sc, outs); err != nil {
			return err
		}
	}
	if len(routes) > 0 {
		if err := r.insertScopeRoutes(ctx, sc, routes); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) insertScopeOutbounds(ctx context.Context, sc RoutingScope, outs []model.NodeOutbound) error {
	var b strings.Builder
	b.WriteString("INSERT INTO node_outbounds (node_id, group_id, tag, type, settings, sort_order) VALUES ")
	args := make([]any, 0, len(outs)*6)
	for i, o := range outs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?, CAST(? AS jsonb), ?)")
		args = append(args, sc.NodeID, sc.GroupID, o.Tag, o.Type, o.Settings, o.SortOrder)
	}
	if err := r.d.Conn(ctx).Exec(b.String(), args...).Error; err != nil {
		return routingErr(err)
	}
	return nil
}

func (r *Repo) insertScopeRoutes(ctx context.Context, sc RoutingScope, routes []model.NodeRoute) error {
	var b strings.Builder
	b.WriteString("INSERT INTO node_routes (node_id, group_id, priority, sort_order, matcher, outbound_tag, enabled, note) VALUES ")
	args := make([]any, 0, len(routes)*8)
	for i, rt := range routes {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?, CAST(? AS jsonb), ?, ?, ?)")
		args = append(args, sc.NodeID, sc.GroupID, rt.Priority, rt.SortOrder, rt.Matcher, rt.OutboundTag, rt.Enabled, rt.Note)
	}
	if err := r.d.Conn(ctx).Exec(b.String(), args...).Error; err != nil {
		return routingErr(err)
	}
	return nil
}

// DanglingRef 是一条指向看不见的出站的规则：引用方（Kind 为 "group" 或 "node"）、引用方名字、指向的 tag。
type DanglingRef struct {
	Kind string `gorm:"column:kind"`
	Name string `gorm:"column:name"`
	Tag  string `gorm:"column:tag"`
}

// DanglingRefs 列出悬空出站引用（含停用规则）。软删节点的路由行留在库里但不再参与任何判断。
func (r *Repo) DanglingRefs(ctx context.Context) ([]DanglingRef, error) {
	var rows []DanglingRef
	err := r.d.Conn(ctx).Raw(`
		SELECT 'group' AS kind, g.name AS name, r.outbound_tag AS tag
		  FROM node_routes r
		  JOIN route_groups g ON g.id = r.group_id
		 WHERE lower(r.outbound_tag) NOT IN ('direct','block')
		   AND NOT EXISTS (
			 SELECT 1 FROM node_outbounds o
			  WHERE o.tag = r.outbound_tag AND o.node_id IS NULL
			    AND (o.group_id IS NULL OR o.group_id = r.group_id))
		UNION
		SELECT 'node', coalesce(n.display_name, n.name), r.outbound_tag
		  FROM node_routes r
		  JOIN nodes n ON n.id = r.node_id AND n.deleted_at IS NULL
		 WHERE lower(r.outbound_tag) NOT IN ('direct','block')
		   AND NOT EXISTS (
			 SELECT 1 FROM node_outbounds o
			  WHERE o.tag = r.outbound_tag
			    AND (o.node_id = r.node_id
			         OR (o.node_id IS NULL AND o.group_id IS NULL)
			         OR o.group_id IN (SELECT m.group_id FROM route_group_members m WHERE m.node_id = r.node_id)))`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []DanglingRef{}, nil
	}
	return rows, nil
}

// VisibleOutboundTags 返回节点能看见的出站 tag（原样）；nodeID 为 nil 时只有全局。
func (r *Repo) VisibleOutboundTags(ctx context.Context, nodeID *uuid.UUID) ([]string, error) {
	var tags []string
	err := r.d.Conn(ctx).Raw(`
		SELECT o.tag FROM node_outbounds o
		 WHERE o.node_id IS NULL
		   AND (o.group_id IS NULL OR o.group_id IN (
			 SELECT m.group_id FROM route_group_members m WHERE m.node_id = ?))`, nodeID,
	).Scan(&tags).Error
	if err != nil {
		return nil, err
	}
	if tags == nil {
		return []string{}, nil
	}
	return tags, nil
}

// BumpNodeRowVersions 推进节点 row_version；成员关系与节点私有路由是节点属性，后台节点抽屉用节点行版本做乐观并发。
func (r *Repo) BumpNodeRowVersions(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	// 先按 id 顺序锁行再更新，并发保存不同路由组时不会互相死锁
	return r.d.Conn(ctx).Exec(
		`WITH locked AS (SELECT id FROM nodes WHERE id IN ? ORDER BY id FOR UPDATE)
		 UPDATE nodes n SET row_version = n.row_version + 1, updated_at = now() FROM locked WHERE n.id = locked.id`, ids,
	).Error
}

// LiveNodeCount 统计 ids 里未删除且未退役的节点个数。
func (r *Repo) LiveNodeCount(ctx context.Context, ids []uuid.UUID) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var n int
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*)::int FROM nodes WHERE id IN ? AND deleted_at IS NULL AND serving_status <> 'retired'`, ids,
	).Scan(&n).Error
	return n, err
}

// AllLiveNodeCount 统计全部未删除且未退役的节点数。
func (r *Repo) AllLiveNodeCount(ctx context.Context) (int, error) {
	var n int
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*)::int FROM nodes WHERE deleted_at IS NULL AND serving_status <> 'retired'`,
	).Scan(&n).Error
	return n, err
}

// OnlineServedNodeCount 统计在 since 之后有心跳、且落在 ServedOnServerCond 上的节点数。
func (r *Repo) OnlineServedNodeCount(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*) FROM nodes n JOIN servers s ON s.id = n.server_id WHERE `+ServedOnServerCond+` AND s.last_heartbeat_at >= ?`,
		since,
	).Scan(&n).Error
	return n, err
}
