// 路由组内路由与成员关系写路径。
package service

import (
	"context"
	"sort"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
)

// GroupRouting 是某路由组的出站与规则，RowVersion 为组行版本。
type GroupRouting struct {
	RowVersion int64
	Outbounds  []RoutingOutbound
	Routes     []RoutingRule
}

// normalizeIDs 去重、按 compareUUID 排序，永远非 nil。
func normalizeIDs(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return []uuid.UUID{}
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return compareUUID(out[i], out[j]) < 0 })
	return out
}

// symmetricDiff 返回只在 a 或只在 b 里的 id（两边都已排序去重）。
func symmetricDiff(a, b []uuid.UUID) []uuid.UUID {
	i, j := 0, 0
	var out []uuid.UUID
	for i < len(a) && j < len(b) {
		c := compareUUID(a[i], b[j])
		switch {
		case c < 0:
			out = append(out, a[i])
			i++
		case c > 0:
			out = append(out, b[j])
			j++
		default:
			i++
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, b[j])
	}
	return out
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func missingIDs(want, have []uuid.UUID) []uuid.UUID {
	haveSet := make(map[uuid.UUID]bool, len(have))
	for _, id := range have {
		haveSet[id] = true
	}
	var missing []uuid.UUID
	for _, id := range want {
		if !haveSet[id] {
			missing = append(missing, id)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return compareUUID(missing[i], missing[j]) < 0 })
	return missing
}

// GetGroupRouting 读取某路由组的出站与规则。
func (s *Service) GetGroupRouting(ctx context.Context, id uuid.UUID) (GroupRouting, error) {
	rows, err := s.repo.RouteGroups(ctx, &id)
	if err != nil {
		return GroupRouting{}, err
	}
	if len(rows) == 0 {
		return GroupRouting{}, errs.New(errs.NotFound)
	}
	sc := repo.RoutingScope{GroupID: &id}
	outs, routes, err := loadScope(ctx, s, sc)
	if err != nil {
		return GroupRouting{}, err
	}
	return GroupRouting{RowVersion: rows[0].RowVersion, Outbounds: outs, Routes: routes}, nil
}

// SetGroupRouting 全量替换组内出站与规则。
func (s *Service) SetGroupRouting(ctx context.Context, id uuid.UUID, rowVersion int64, in RoutingInput) (RouteGroupWrite, error) {
	if rowVersion <= 0 {
		return RouteGroupWrite{}, errs.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	tags, err := validateRoutingInput(&in)
	if err != nil {
		return RouteGroupWrite{}, err
	}

	var out RouteGroupWrite
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoutingWrites(ctx); err != nil {
			return err
		}
		g, ok, err := s.repo.RouteGroupForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if g.RowVersion != rowVersion {
			return routeGroupConflict(g.RowVersion)
		}
		global, err := visibleTagMap(ctx, s, nil)
		if err != nil {
			return err
		}
		if err := checkRouteRefs(in.Routes, tags, global); err != nil {
			return err
		}
		before, err := s.repo.DanglingRefs(ctx)
		if err != nil {
			return err
		}
		sc := repo.RoutingScope{GroupID: &id}
		if err := s.repo.ReplaceScopeRouting(ctx, sc, outboundsToModel(in.Outbounds), rulesToModel(in.Routes)); err != nil {
			return routingWriteErr(err)
		}
		if err := refuseNewDangling(ctx, s, before, "要删除的组内出站仍被成员节点的规则引用"); err != nil {
			return err
		}
		if err := s.repo.BumpRouteGroupVersions(ctx, []uuid.UUID{id}); err != nil {
			return err
		}
		members, err := s.repo.GroupMemberIDs(ctx, id)
		if err != nil {
			return err
		}
		out.AffectedNodes, err = s.repo.LiveNodeCount(ctx, members)
		if err != nil {
			return err
		}
		if out.AffectedNodes > 0 {
			s.MarkConfigChanged(ctx)
		}
		out.RowVersion = rowVersion + 1
		return s.audit.Write(ctx, auditsvc.Event{
			Action: "node.routing.group_publish", ResourceType: "route_group", ResourceID: &id,
			Data: map[string]any{
				"outbounds": len(in.Outbounds), "routes": len(in.Routes), "affected_nodes": out.AffectedNodes,
			},
		})
	})
	if err != nil {
		return RouteGroupWrite{}, err
	}
	return out, nil
}

// SetNodeRouteGroups 整体替换节点所属的路由组。
func (s *Service) SetNodeRouteGroups(ctx context.Context, nodeID uuid.UUID, rowVersion int64, groupIDs []uuid.UUID) (RouteGroupWrite, error) {
	if rowVersion <= 0 {
		return RouteGroupWrite{}, errs.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	groupIDs = normalizeIDs(groupIDs)

	var out RouteGroupWrite
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoutingWrites(ctx); err != nil {
			return err
		}
		n, found, err := s.repo.NodeByID(ctx, nodeID, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if n.RowVersion != rowVersion {
			return nodeVersionConflict(n.RowVersion)
		}
		existing, err := s.repo.ExistingRouteGroupIDs(ctx, groupIDs)
		if err != nil {
			return err
		}
		if miss := missingIDs(groupIDs, existing); len(miss) > 0 {
			return errs.Invalid(map[string]string{
				"group_ids": "路由组不存在：" + strings.Join(uuidStrings(miss), "、"),
			})
		}
		rows, err := s.repo.NodeRouteGroups(ctx, []uuid.UUID{nodeID})
		if err != nil {
			return err
		}
		was := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			was = append(was, row.GroupID)
		}
		was = normalizeIDs(was)
		changed := symmetricDiff(was, groupIDs)
		if err := s.repo.LockRouteGroups(ctx, changed); err != nil {
			return err
		}
		before, err := s.repo.DanglingRefs(ctx)
		if err != nil {
			return err
		}
		if err := s.repo.ReplaceNodeGroups(ctx, nodeID, groupIDs); err != nil {
			return err
		}
		if err := refuseNewDangling(ctx, s, before, "本节点仍有规则指向要退出的组的出站"); err != nil {
			return err
		}
		if err := s.repo.BumpRouteGroupVersions(ctx, changed); err != nil {
			return err
		}
		if err := s.repo.BumpNodeRowVersions(ctx, []uuid.UUID{nodeID}); err != nil {
			return err
		}
		s.nodeChanged(ctx, realtime.OpUpdate, nodeID)
		if len(changed) > 0 && n.ServingStatus != NodeRetired {
			s.MarkConfigChanged(ctx)
			out.AffectedNodes = 1
		}
		out.RowVersion = rowVersion + 1
		return s.audit.Write(ctx, auditsvc.Event{
			Action: "node.route_groups_update", ResourceType: "node", ResourceID: &nodeID,
			Data: map[string]any{
				"before_groups": uuidStrings(was),
				"groups":        uuidStrings(groupIDs),
			},
		})
	})
	if err != nil {
		return RouteGroupWrite{}, err
	}
	return out, nil
}
