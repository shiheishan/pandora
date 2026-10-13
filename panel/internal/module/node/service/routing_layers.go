package service

import (
	"context"

	"github.com/google/uuid"
)

// nodeRoutingLayers 一次读出这批节点各自的层（节点私有 → 所在各路由组按 sort_order, id → 全局）与所在组：
// 3 次读（成员与组、可见出站、可见启用规则），与节点个数无关。nodeIDs 为空时不查库。
func (s *Service) nodeRoutingLayers(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID][]routingLayer, map[uuid.UUID][]RouteGroupRef, error) {
	layersByNode := make(map[uuid.UUID][]routingLayer)
	groupsByNode := make(map[uuid.UUID][]RouteGroupRef)
	if len(nodeIDs) == 0 {
		return layersByNode, groupsByNode, nil
	}

	memberRows, err := s.repo.NodeRouteGroups(ctx, nodeIDs)
	if err != nil {
		return nil, nil, err
	}
	outs, err := s.repo.VisibleOutbounds(ctx, nodeIDs)
	if err != nil {
		return nil, nil, err
	}
	routes, err := s.repo.VisibleEnabledRoutes(ctx, nodeIDs)
	if err != nil {
		return nil, nil, err
	}

	// 三条读各自取快照；成员快照里没有的组，出站/规则行上的 group_id 必须丢掉，不能落进别的层。
	groupBelongs := map[uuid.UUID]map[uuid.UUID]bool{}
	for _, row := range memberRows {
		if groupBelongs[row.NodeID] == nil {
			groupBelongs[row.NodeID] = map[uuid.UUID]bool{}
		}
		groupBelongs[row.NodeID][row.GroupID] = true
		groupsByNode[row.NodeID] = append(groupsByNode[row.NodeID], RouteGroupRef{
			ID: row.GroupID, Name: row.Name, SortOrder: row.SortOrder,
		})
	}

	nodeSet := map[uuid.UUID]bool{}
	for _, id := range nodeIDs {
		nodeSet[id] = true
		layersByNode[id] = []routingLayer{
			{source: RoutingSource{Scope: routingScopeNode}, outbounds: nil, routes: nil},
		}
		for _, g := range groupsByNode[id] {
			gid := g.ID
			layersByNode[id] = append(layersByNode[id], routingLayer{
				source: RoutingSource{Scope: routingScopeGroup, GroupID: &gid, GroupName: g.Name},
			})
		}
		layersByNode[id] = append(layersByNode[id], routingLayer{
			source: RoutingSource{Scope: routingScopeGlobal},
		})
	}

	appendOutbound := func(nodeID uuid.UUID, layerIdx int, o RoutingOutbound) {
		ls := layersByNode[nodeID]
		if layerIdx < 0 || layerIdx >= len(ls) {
			return
		}
		ls[layerIdx].outbounds = append(ls[layerIdx].outbounds, o)
		layersByNode[nodeID] = ls
	}
	appendRoute := func(nodeID uuid.UUID, layerIdx int, r RoutingRule) {
		ls := layersByNode[nodeID]
		if layerIdx < 0 || layerIdx >= len(ls) {
			return
		}
		ls[layerIdx].routes = append(ls[layerIdx].routes, r)
		layersByNode[nodeID] = ls
	}

	layerIndex := func(nodeID uuid.UUID, sc RoutingSource) int {
		ls := layersByNode[nodeID]
		for i, l := range ls {
			if l.source.Scope != sc.Scope {
				continue
			}
			switch sc.Scope {
			case routingScopeNode, routingScopeGlobal:
				return i
			case routingScopeGroup:
				if sc.GroupID != nil && l.source.GroupID != nil && *l.source.GroupID == *sc.GroupID {
					return i
				}
			}
		}
		return -1
	}

	for _, m := range outs {
		ro := outboundFromModel(m)
		if m.NodeID != nil {
			if !nodeSet[*m.NodeID] {
				continue
			}
			idx := layerIndex(*m.NodeID, RoutingSource{Scope: routingScopeNode})
			appendOutbound(*m.NodeID, idx, ro)
			continue
		}
		if m.GroupID != nil {
			gid := *m.GroupID
			for nodeID, gs := range groupBelongs {
				if !gs[gid] {
					continue
				}
				idx := layerIndex(nodeID, RoutingSource{Scope: routingScopeGroup, GroupID: &gid})
				appendOutbound(nodeID, idx, ro)
			}
			continue
		}
		for nodeID := range nodeSet {
			idx := layerIndex(nodeID, RoutingSource{Scope: routingScopeGlobal})
			appendOutbound(nodeID, idx, ro)
		}
	}

	for _, m := range routes {
		rr := ruleFromModel(m)
		if !rr.Enabled {
			continue
		}
		if m.NodeID != nil {
			if !nodeSet[*m.NodeID] {
				continue
			}
			idx := layerIndex(*m.NodeID, RoutingSource{Scope: routingScopeNode})
			appendRoute(*m.NodeID, idx, rr)
			continue
		}
		if m.GroupID != nil {
			gid := *m.GroupID
			for nodeID, gs := range groupBelongs {
				if !gs[gid] {
					continue
				}
				idx := layerIndex(nodeID, RoutingSource{Scope: routingScopeGroup, GroupID: &gid})
				appendRoute(nodeID, idx, rr)
			}
			continue
		}
		for nodeID := range nodeSet {
			idx := layerIndex(nodeID, RoutingSource{Scope: routingScopeGlobal})
			appendRoute(nodeID, idx, rr)
		}
	}

	for id := range nodeSet {
		if groupsByNode[id] == nil {
			groupsByNode[id] = []RouteGroupRef{}
		}
	}
	return layersByNode, groupsByNode, nil
}
