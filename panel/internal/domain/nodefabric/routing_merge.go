package nodefabric

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

//------------------------------------------------------------------------------
// 生效路由的合并口径
//
// 一个节点能用的路由来自若干「层」，每层是一个范围的出站与规则，各自已按存储
// 顺序排好。层按优先级从高到低排：越具体的范围越靠前——
//
//   节点私有 → 所在各路由组（按 route_groups.sort_order）→ 全局
//
//   规则：按层顺序直接拼接 —— 具体范围的规则先匹配，能截住宽泛范围的同类流量。
//         出站名按下发时的存储原样带走；内置名的小写规范只发生在保存入口。
//   出站：按层逆序铺开、同 tag 后来者覆盖 —— 宽泛范围先占位，具体范围就地替换，
//         被覆盖的出站保留原位置，下发顺序不因覆盖而跳动。即 全局 → 组（后排的组
//         先铺、先排的组覆盖它）→ 节点。
//
// 规则与出站因此是同一份层序的两个方向，不会出现「规则先看 A、出站却以 B 为准」。
//------------------------------------------------------------------------------

// RoutingLayer 是一个范围（节点 / 某个路由组 / 全局）的出站与启用中的规则。
// GroupID / GroupName 只在路由组层有值，生效预览用来标注来源。
type RoutingLayer struct {
	Scope     string
	GroupID   string
	GroupName string
	Outbounds []NodeOutbound
	Routes    []NodeRoute
}

// MergeRouting 把按优先级从高到低排好的层合成节点最终生效的出站与规则。
func MergeRouting(layers []RoutingLayer) ([]NodeOutbound, []NodeRoute) {
	m := mergeRoutingLayers(layers)
	var outs []NodeOutbound
	for _, o := range m.outbounds {
		outs = append(outs, o.NodeOutbound)
	}
	var routes []NodeRoute
	for _, r := range m.routes {
		routes = append(routes, r.NodeRoute)
	}
	return outs, routes
}

// layeredOutbound / layeredRoute 带着来源层的下标，生效预览据此标注来源。
type layeredOutbound struct {
	NodeOutbound
	layer int
}

type layeredRoute struct {
	NodeRoute
	layer int
}

type mergedRouting struct {
	outbounds []layeredOutbound
	routes    []layeredRoute
}

// canonicalRouteTag 把规则对内置出站的引用规范成小写的 direct / block（去空白、不分大小写），
// 自定义出站原样返回。只在保存（replaceScopeRoutingTx）时用：pdnd 只认小写的内置名，
// 下发按存储原样带走。
func canonicalRouteTag(tag string) string {
	if t := strings.ToLower(strings.TrimSpace(tag)); t == "direct" || t == "block" {
		return t
	}
	return tag
}

func mergeRoutingLayers(layers []RoutingLayer) mergedRouting {
	var m mergedRouting
	idx := make(map[string]int)
	for i := len(layers) - 1; i >= 0; i-- {
		for _, o := range layers[i].Outbounds {
			if at, dup := idx[o.Tag]; dup {
				m.outbounds[at] = layeredOutbound{o, i}
				continue
			}
			idx[o.Tag] = len(m.outbounds)
			m.outbounds = append(m.outbounds, layeredOutbound{o, i})
		}
	}
	for i, l := range layers {
		for _, r := range l.Routes {
			m.routes = append(m.routes, layeredRoute{r, i})
		}
	}
	return m
}

// groupOrder 是路由组之间的生效顺序：sort_order 小的先，同值按 id（uuidv7，即创建先后）。
// 合并、预览与后台列表共用，不依赖排序规则（collation）。
const groupOrder = `g.sort_order, g.id`

// loadNodeRoutingLayersTx 在调用方事务里读出节点的各层：节点私有、所在各路由组（按组顺序）、全局。
func loadNodeRoutingLayersTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]RoutingLayer, error) {
	layers := []RoutingLayer{{Scope: "node"}}
	groupAt := map[string]int{}
	grows, err := tx.Query(ctx, `
		SELECT g.id::text, g.name
		  FROM route_group_members m
		  JOIN route_groups g ON g.tenant_id = m.tenant_id AND g.id = m.group_id
		 WHERE m.tenant_id = $1 AND m.node_id = $2::uuid
		 ORDER BY `+groupOrder, tenantID, nodeID)
	if err != nil {
		return nil, err
	}
	for grows.Next() {
		l := RoutingLayer{Scope: "group"}
		if err := grows.Scan(&l.GroupID, &l.GroupName); err != nil {
			grows.Close()
			return nil, err
		}
		groupAt[l.GroupID] = len(layers)
		layers = append(layers, l)
	}
	grows.Close()
	if err := grows.Err(); err != nil {
		return nil, err
	}
	layers = append(layers, RoutingLayer{Scope: "global"})
	// 读成员与读出站、规则是三条语句，READ COMMITTED 下各自取快照：期间新加入的组
	// 在成员快照里没有，它的行跳过（返回 nil），绝不能落进别的层。下一次 generation 推进会补上
	at := func(nodeScoped bool, groupID string) *RoutingLayer {
		switch {
		case nodeScoped:
			return &layers[0]
		case groupID != "":
			if i, ok := groupAt[groupID]; ok {
				return &layers[i]
			}
			return nil
		default:
			return &layers[len(layers)-1]
		}
	}
	// 只取本节点看得见的三类范围：自己的、所在组的、全局的（两列都空）
	const visible = `tenant_id = $1 AND (node_id = $2::uuid
		    OR (node_id IS NULL AND group_id IS NULL)
		    OR group_id IN (SELECT group_id FROM route_group_members WHERE tenant_id = $1 AND node_id = $2::uuid))`

	rows, err := tx.Query(ctx, `
		SELECT tag, type, settings, (node_id IS NOT NULL), coalesce(group_id::text, '')
		  FROM node_outbounds
		 WHERE `+visible+`
		 ORDER BY sort_order, tag`, tenantID, nodeID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o NodeOutbound
		var scoped bool
		var groupID string
		if err := rows.Scan(&o.Tag, &o.Type, &o.Settings, &scoped, &groupID); err != nil {
			rows.Close()
			return nil, err
		}
		if l := at(scoped, groupID); l != nil {
			l.Outbounds = append(l.Outbounds, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rrows, err := tx.Query(ctx, `
		SELECT matcher, outbound_tag, (node_id IS NOT NULL), coalesce(group_id::text, '')
		  FROM node_routes
		 WHERE `+visible+` AND enabled
		 ORDER BY priority, created_at`, tenantID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var r NodeRoute
		var scoped bool
		var groupID string
		if err := rrows.Scan(&r.Matcher, &r.OutboundTag, &scoped, &groupID); err != nil {
			return nil, err
		}
		if l := at(scoped, groupID); l != nil {
			l.Routes = append(l.Routes, r)
		}
	}
	return layers, rrows.Err()
}

// loadNodeRoutingTx 在调用方事务里读出节点的各层并合并成下发用的出站与规则。
func loadNodeRoutingTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]NodeOutbound, []NodeRoute, error) {
	layers, err := loadNodeRoutingLayersTx(ctx, tx, tenantID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	outs, routes := MergeRouting(layers)
	return outs, routes, nil
}
