// [INPUT]: 依赖 uniproxy_config.go 的 NodeOutbound / NodeRoute，依赖 pgx 在调用方事务里读 node_outbounds / node_routes
// [OUTPUT]: 对外提供 RoutingLayer、MergeRouting；包内 loadNodeRoutingTx
// [POS]: domain/nodefabric 的生效路由唯一口径：UniProxy 下发（LoadRouting）、长连接推送与有效发布物（FetchEffectiveConfig）都经 loadNodeRoutingTx 读层、经 MergeRouting 合并，不再各写一份
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"

	"github.com/jackc/pgx/v5"
)

//------------------------------------------------------------------------------
// 生效路由的合并口径
//
// 一个节点能用的路由来自若干「层」，每层是一个范围的出站与规则，各自已按存储
// 顺序排好。层按优先级从高到低排：越具体的范围越靠前（节点私有在最前、全局在最后）。
//
//   规则：按层顺序直接拼接 —— 具体范围的规则先匹配，能截住宽泛范围的同类流量。
//   出站：按层逆序铺开、同 tag 后来者覆盖 —— 宽泛范围先占位，具体范围就地替换，
//         被覆盖的出站保留原位置，下发顺序不因覆盖而跳动。
//
// 规则与出站因此是同一份层序的两个方向，不会出现「规则先看 A、出站却以 B 为准」。
//------------------------------------------------------------------------------

// RoutingLayer 是一个范围（全局 / 节点）的出站与启用中的规则。
type RoutingLayer struct {
	Scope     string
	Outbounds []NodeOutbound
	Routes    []NodeRoute
}

// MergeRouting 把按优先级从高到低排好的层合成节点最终生效的出站与规则。
func MergeRouting(layers []RoutingLayer) ([]NodeOutbound, []NodeRoute) {
	var outs []NodeOutbound
	idx := make(map[string]int)
	for i := len(layers) - 1; i >= 0; i-- {
		for _, o := range layers[i].Outbounds {
			if at, dup := idx[o.Tag]; dup {
				outs[at] = o
				continue
			}
			idx[o.Tag] = len(outs)
			outs = append(outs, o)
		}
	}
	var routes []NodeRoute
	for _, l := range layers {
		routes = append(routes, l.Routes...)
	}
	return outs, routes
}

// loadNodeRoutingTx 在调用方事务里读出节点的各层并合并。
func loadNodeRoutingTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) ([]NodeOutbound, []NodeRoute, error) {
	layers := []RoutingLayer{{Scope: "node"}, {Scope: "global"}}
	at := func(scoped bool) *RoutingLayer {
		if scoped {
			return &layers[0]
		}
		return &layers[1]
	}

	rows, err := tx.Query(ctx, `
		SELECT tag, type, settings, (node_id IS NOT NULL)
		  FROM node_outbounds
		 WHERE tenant_id = $1 AND (node_id IS NULL OR node_id = $2::uuid)
		 ORDER BY sort_order, tag`, tenantID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var o NodeOutbound
		var scoped bool
		if err := rows.Scan(&o.Tag, &o.Type, &o.Settings, &scoped); err != nil {
			rows.Close()
			return nil, nil, err
		}
		l := at(scoped)
		l.Outbounds = append(l.Outbounds, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rrows, err := tx.Query(ctx, `
		SELECT matcher, outbound_tag, (node_id IS NOT NULL)
		  FROM node_routes
		 WHERE tenant_id = $1 AND (node_id IS NULL OR node_id = $2::uuid) AND enabled
		 ORDER BY priority, created_at`, tenantID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var r NodeRoute
		var scoped bool
		if err := rrows.Scan(&r.Matcher, &r.OutboundTag, &scoped); err != nil {
			return nil, nil, err
		}
		l := at(scoped)
		l.Routes = append(l.Routes, r)
	}
	if err := rrows.Err(); err != nil {
		return nil, nil, err
	}
	outs, routes := MergeRouting(layers)
	return outs, routes, nil
}
