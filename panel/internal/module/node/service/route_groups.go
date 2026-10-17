// 路由组的增删改查。
package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

const maxRouteGroupSortOrder = 1_000_000

// RouteGroupMember 是组里的一个节点。
type RouteGroupMember struct {
	ID   uuid.UUID
	Name string
}

// RouteGroup 是后台看到的一个路由组。
type RouteGroup struct {
	ID            uuid.UUID
	Name          string
	Description   string
	SortOrder     int
	RowVersion    int64
	OutboundCount int
	RuleCount     int
	Members       []RouteGroupMember
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// RouteGroupCreate 是新建路由组的输入。
type RouteGroupCreate struct {
	Name        string
	Description string
	SortOrder   int
}

// RouteGroupPatch 是改路由组元信息的输入，nil 字段不改。
type RouteGroupPatch struct {
	RowVersion  int64
	Name        *string
	Description *string
	SortOrder   *int
}

// RouteGroupWrite 是改变了节点生效配置的组写操作的结果：新的行版本（组或节点的）与受影响的在役节点数。
type RouteGroupWrite struct {
	RowVersion    int64
	AffectedNodes int
}

// routeGroupConflict 是路由组乐观锁冲突。
func routeGroupConflict(current int64) error {
	return &errs.Error{
		Code:    errs.Conflict,
		Message: "路由组已被其他管理员修改，请刷新后重试",
		Fields:  map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
	}
}

// normalizeRouteGroupFields 校验并规范名称、说明与组序（只校验传了的）。
func normalizeRouteGroupFields(name, description *string, sortOrder *int) error {
	if sortOrder != nil && (*sortOrder < -maxRouteGroupSortOrder || *sortOrder > maxRouteGroupSortOrder) {
		return errs.Invalid(map[string]string{"sort_order": "排序取值 -1000000 到 1000000"})
	}
	if name != nil {
		*name = strings.TrimSpace(*name)
		if n := utf8.RuneCountInString(*name); n < 1 || n > 64 || strings.ContainsRune(*name, '\x00') {
			return errs.Invalid(map[string]string{"name": "名称为 1 到 64 个字符"})
		}
	}
	if description != nil {
		*description = strings.TrimSpace(*description)
		if utf8.RuneCountInString(*description) > 500 || strings.ContainsRune(*description, '\x00') {
			return errs.Invalid(map[string]string{"description": "说明最多 500 个字符"})
		}
	}
	return nil
}

func routeGroupFromRow(row repo.RouteGroupRow, members []RouteGroupMember) RouteGroup {
	if members == nil {
		members = []RouteGroupMember{}
	}
	return RouteGroup{
		ID: row.ID, Name: row.Name, Description: row.Description, SortOrder: row.SortOrder,
		RowVersion: row.RowVersion, OutboundCount: row.OutboundCount, RuleCount: row.RuleCount,
		Members: members, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *Service) routeGroupsByID(ctx context.Context, id *uuid.UUID) ([]RouteGroup, error) {
	rows, err := s.repo.RouteGroups(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []RouteGroup{}, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	memberRows, err := s.repo.RouteGroupMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[uuid.UUID][]RouteGroupMember)
	for _, m := range memberRows {
		byGroup[m.GroupID] = append(byGroup[m.GroupID], RouteGroupMember{ID: m.NodeID, Name: m.Name})
	}
	out := make([]RouteGroup, len(rows))
	for i, r := range rows {
		out[i] = routeGroupFromRow(r, byGroup[r.ID])
	}
	return out, nil
}

// ListRouteGroups 按生效顺序列出全部路由组。
func (s *Service) ListRouteGroups(ctx context.Context) ([]RouteGroup, error) {
	return s.routeGroupsByID(ctx, nil)
}

// CreateRouteGroup 新建空路由组；空组不影响任何节点，不拿路由锁、不推进配置纪元。
func (s *Service) CreateRouteGroup(ctx context.Context, in RouteGroupCreate) (RouteGroup, error) {
	if err := normalizeRouteGroupFields(&in.Name, &in.Description, &in.SortOrder); err != nil {
		return RouteGroup{}, err
	}
	var newID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		g := &model.RouteGroup{Name: in.Name, Description: in.Description, SortOrder: in.SortOrder}
		if err := s.repo.InsertRouteGroup(ctx, g); err != nil {
			return routingWriteErr(err)
		}
		newID = g.ID
		return s.audit.Write(ctx, auditsvc.Event{
			Action: "route_group.create", ResourceType: "route_group", ResourceID: &newID,
			Data: map[string]any{"name": in.Name, "sort_order": in.SortOrder},
		})
	})
	if err != nil {
		return RouteGroup{}, err
	}
	groups, err := s.routeGroupsByID(ctx, &newID)
	if err != nil {
		return RouteGroup{}, err
	}
	if len(groups) == 0 {
		return RouteGroup{}, errs.New(errs.NotFound)
	}
	return groups[0], nil
}

// UpdateRouteGroup 改路由组元信息；组序真变时推进成员节点的配置纪元。
func (s *Service) UpdateRouteGroup(ctx context.Context, id uuid.UUID, in RouteGroupPatch) (RouteGroup, int, error) {
	if in.RowVersion <= 0 {
		return RouteGroup{}, 0, errs.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if err := normalizeRouteGroupFields(in.Name, in.Description, in.SortOrder); err != nil {
		return RouteGroup{}, 0, err
	}

	var affected int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoutingWrites(ctx); err != nil {
			return err
		}
		before, ok, err := s.repo.RouteGroupForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if before.RowVersion != in.RowVersion {
			return routeGroupConflict(before.RowVersion)
		}
		name := before.Name
		if in.Name != nil {
			name = *in.Name
		}
		desc := before.Description
		if in.Description != nil {
			desc = *in.Description
		}
		sortOrder := before.SortOrder
		if in.SortOrder != nil {
			sortOrder = *in.SortOrder
		}
		if err := s.repo.UpdateRouteGroup(ctx, id, name, desc, sortOrder); err != nil {
			return routingWriteErr(err)
		}
		if in.SortOrder != nil && *in.SortOrder != before.SortOrder {
			members, err := s.repo.GroupMemberIDs(ctx, id)
			if err != nil {
				return err
			}
			affected, err = s.repo.LiveNodeCount(ctx, members)
			if err != nil {
				return err
			}
			if affected > 0 {
				s.MarkConfigChanged(ctx)
			}
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action: "route_group.update", ResourceType: "route_group", ResourceID: &id,
			Data: map[string]any{
				"before_name": before.Name, "name": name,
				"before_sort_order": before.SortOrder, "sort_order": sortOrder,
				"affected_nodes": affected,
			},
		})
	})
	if err != nil {
		return RouteGroup{}, 0, err
	}
	groups, err := s.routeGroupsByID(ctx, &id)
	if err != nil {
		return RouteGroup{}, 0, err
	}
	if len(groups) == 0 {
		return RouteGroup{}, 0, errs.New(errs.NotFound)
	}
	return groups[0], affected, nil
}

// DeleteRouteGroup 删除路由组并推进成员节点行版本；成员规则仍指向组内出站时拒绝。
func (s *Service) DeleteRouteGroup(ctx context.Context, id uuid.UUID, rowVersion int64) (int, error) {
	if rowVersion <= 0 {
		return 0, errs.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}

	var affected int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
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
		members, err := s.repo.GroupMemberIDs(ctx, id)
		if err != nil {
			return err
		}
		before, err := s.repo.DanglingRefs(ctx)
		if err != nil {
			return err
		}
		if err := s.repo.DeleteRouteGroup(ctx, id); err != nil {
			return err
		}
		if err := refuseNewDangling(ctx, s, before, "组内出站仍被成员节点的规则引用"); err != nil {
			return err
		}
		if err := s.repo.BumpNodeRowVersions(ctx, members); err != nil {
			return err
		}
		s.nodesChanged(ctx, members)
		affected, err = s.repo.LiveNodeCount(ctx, members)
		if err != nil {
			return err
		}
		if affected > 0 {
			s.MarkConfigChanged(ctx)
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action: "route_group.delete", ResourceType: "route_group", ResourceID: &id,
			Data: map[string]any{"name": g.Name, "members": len(members), "affected_nodes": affected},
		})
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}
