package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// errPoolPortMissing 是启动时没装配 catalog 或 identity 端口。
var errPoolPortMissing = errs.Newf(errs.Unavailable, "节点池依赖的模块未装配")

var poolCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ListPools 返回后台节点池列表（含节点数、绑定套餐、成员与仅限用户组）。
func (s *Service) ListPools(ctx context.Context) ([]Pool, error) {
	if s.catalog == nil {
		return nil, errPoolPortMissing
	}
	rows, err := s.repo.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []Pool{}, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	counts, err := s.repo.PoolNodeCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	members, err := s.repo.PoolMembers(ctx, ids)
	if err != nil {
		return nil, err
	}
	groupIDsByPool, err := s.repo.PoolUserGroupIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	usage, err := s.catalog.PoolUsage(ctx, ids)
	if err != nil {
		return nil, err
	}

	needGroups := false
	for _, gids := range groupIDsByPool {
		if len(gids) > 0 {
			needGroups = true
			break
		}
	}
	groupName := map[uuid.UUID]string{}
	if needGroups {
		if s.groups == nil {
			return nil, errPoolPortMissing
		}
		groups, err := s.groups.ListGroups(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			groupName[g.ID] = g.Name
		}
	}

	out := make([]Pool, len(rows))
	for i, r := range rows {
		c := counts[r.ID]
		u := usage[r.ID]
		planNames := u.PlanNames
		if planNames == nil {
			planNames = []string{}
		}
		out[i] = Pool{
			ID:                r.ID,
			Code:              r.Code,
			Name:              r.Name,
			Region:            r.Region,
			Status:            r.Status,
			Nodes:             c.Nodes,
			ActiveNodes:       c.Active,
			Plans:             u.Plans,
			PlanNames:         planNames,
			Members:           poolMembersFromRows(members[r.ID]),
			AllowedUserGroups: poolUserGroupsFromIDs(groupIDsByPool[r.ID], groupName),
		}
	}
	return out, nil
}

func poolMembersFromRows(rows []repo.PoolMemberRow) []PoolMember {
	if len(rows) == 0 {
		return []PoolMember{}
	}
	out := make([]PoolMember, len(rows))
	for i, m := range rows {
		out[i] = PoolMember{ID: m.ID, Name: m.Name, NodeNo: m.NodeNo}
	}
	return out
}

func poolUserGroupsFromIDs(ids []uuid.UUID, nameByID map[uuid.UUID]string) []PoolUserGroup {
	if len(ids) == 0 {
		return []PoolUserGroup{}
	}
	out := make([]PoolUserGroup, len(ids))
	for i, id := range ids {
		out[i] = PoolUserGroup{ID: id, Name: nameByID[id]}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out
}

// CreatePool 新建节点池；可选同时设定仅限用户组名单。
func (s *Service) CreatePool(ctx context.Context, in PoolCreate) (uuid.UUID, error) {
	fields := validatePoolNameRegion(in.Name, in.Region, true)
	code := strings.TrimSpace(in.Code)
	if code != "" && !poolCodePattern.MatchString(code) {
		fields["code"] = "标识只能用小写字母、数字、- 和 _，以字母或数字开头，最多 32 个字符"
	}
	if len(fields) > 0 {
		return uuid.Nil, errs.Invalid(fields)
	}
	if code == "" {
		code = poolCodeFromName(in.Name)
	}
	if in.UserGroupIDs != nil && len(*in.UserGroupIDs) > MaxPoolUserGroups {
		return uuid.Nil, errs.Invalid(map[string]string{
			"allowed_user_group_ids": "一个节点池最多限定 100 个用户组",
		})
	}

	var newID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		p := &model.NodePool{
			Code:   code,
			Name:   in.Name,
			Region: in.Region,
			Status: PoolActive,
		}
		if err := s.repo.InsertPool(ctx, p); err != nil {
			if errors.Is(err, repo.ErrPoolCodeTaken) {
				return errs.Newf(errs.Conflict, "这个节点池标识已存在")
			}
			return err
		}
		newID = p.ID
		if err := s.auditPool(ctx, ActionPoolCreated, newID, map[string]any{
			"code": code, "name": in.Name, "region": in.Region,
		}); err != nil {
			return err
		}
		if in.UserGroupIDs != nil {
			_, err := s.replacePoolGroupsTx(ctx, newID, *in.UserGroupIDs)
			return err
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return newID, nil
}

// poolCodeFromName 从展示名派生池标识；名字全是中文时派生不出可读标识，随机兜底。
func poolCodeFromName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '-' || r == '_':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-_")
	if len(s) > 32 {
		cut := s[:32]
		s = strings.TrimRight(cut, "-_")
	}
	if s == "" {
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "pool"
		}
		return "pool-" + hex.EncodeToString(buf[:])
	}
	return s
}

// replacePoolGroupsTx 在调用方事务里替换池的仅限用户组名单；无变化时不写库。
func (s *Service) replacePoolGroupsTx(ctx context.Context, poolID uuid.UUID, ids []uuid.UUID) (bool, error) {
	if len(ids) > 0 {
		if s.groups == nil {
			return false, errPoolPortMissing
		}
		groups, err := s.groups.ListGroups(ctx)
		if err != nil {
			return false, err
		}
		known := make(map[uuid.UUID]struct{}, len(groups))
		for _, g := range groups {
			known[g.ID] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := known[id]; !ok {
				return false, errs.Invalid(map[string]string{
					"allowed_user_group_ids": "包含不存在的用户组",
				})
			}
		}
	}

	beforeMap, err := s.repo.PoolUserGroupIDs(ctx, []uuid.UUID{poolID})
	if err != nil {
		return false, err
	}
	before := beforeMap[poolID]
	sortedAfter := append([]uuid.UUID(nil), ids...)
	sort.Slice(sortedAfter, func(i, j int) bool {
		return sortedAfter[i].String() < sortedAfter[j].String()
	})
	sortedBefore := append([]uuid.UUID(nil), before...)
	sort.Slice(sortedBefore, func(i, j int) bool {
		return sortedBefore[i].String() < sortedBefore[j].String()
	})
	if poolUUIDSlicesEqual(sortedBefore, sortedAfter) {
		return false, nil
	}

	if err := s.repo.ReplacePoolUserGroups(ctx, poolID, ids); err != nil {
		if errors.Is(err, repo.ErrPoolGroupMissing) {
			return false, errs.Invalid(map[string]string{
				"allowed_user_group_ids": "包含不存在的用户组",
			})
		}
		return false, err
	}
	if err := s.auditPool(ctx, ActionPoolUserGroupsChanged, poolID, map[string]any{
		"before": poolUUIDsToStrings(sortedBefore),
		"after":  poolUUIDsToStrings(sortedAfter),
	}); err != nil {
		return false, err
	}
	s.MarkUsersChanged(ctx)
	return true, nil
}

func poolUUIDSlicesEqual(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func poolUUIDsToStrings(ids []uuid.UUID) []string {
	if len(ids) == 0 {
		return []string{}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// UpdatePool 修改节点池名称、地区、状态或仅限用户组名单。
func (s *Service) UpdatePool(ctx context.Context, id uuid.UUID, in PoolUpdate) error {
	fields := map[string]string{}
	if in.Status != "" && in.Status != PoolActive && in.Status != PoolDraining && in.Status != PoolDisabled {
		fields["status"] = "状态只能是 active / draining / disabled"
	}
	if in.Name != "" || in.Region != "" {
		for k, v := range validatePoolNameRegion(in.Name, in.Region, false) {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	if in.UserGroupIDs != nil && len(*in.UserGroupIDs) > MaxPoolUserGroups {
		return errs.Invalid(map[string]string{
			"allowed_user_group_ids": "一个节点池最多限定 100 个用户组",
		})
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.PoolByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}

		name := cur.Name
		if in.Name != "" {
			name = in.Name
		}
		region := cur.Region
		if in.Region != "" {
			region = in.Region
		}
		status := cur.Status
		if in.Status != "" {
			status = in.Status
		}

		metaChanged := name != cur.Name || region != cur.Region || status != cur.Status
		if metaChanged {
			if err := s.repo.UpdatePool(ctx, id, name, region, status); err != nil {
				return err
			}
			if err := s.auditPool(ctx, ActionPoolUpdated, id, map[string]any{
				"before": map[string]string{"name": cur.Name, "region": cur.Region, "status": cur.Status},
				"after":  map[string]string{"name": name, "region": region, "status": status},
			}); err != nil {
				return err
			}
			if status != cur.Status {
				s.MarkUsersChanged(ctx)
			}
		}

		if in.UserGroupIDs != nil {
			_, err := s.replacePoolGroupsTx(ctx, id, *in.UserGroupIDs)
			return err
		}
		return nil
	})
}

func validatePoolNameRegion(name, region string, create bool) map[string]string {
	fields := map[string]string{}
	if create || name != "" {
		if name == "" {
			fields["name"] = "节点池名称必填"
		} else {
			if utf8.RuneCountInString(name) > 64 {
				fields["name"] = "节点池名称最多 64 个字"
			}
			if strings.ContainsRune(name, 0) {
				fields["name"] = "名称含有非法字符"
			}
		}
	}
	if region != "" {
		if utf8.RuneCountInString(region) > 64 {
			fields["region"] = "地区最多 64 个字，且不能含非法字符"
		} else if strings.ContainsRune(region, 0) {
			fields["region"] = "地区最多 64 个字，且不能含非法字符"
		}
	}
	return fields
}

// DeletePool 删除空池（无在役节点、无未归档套餐绑定）；已归档套餐的绑定不挡，在同一事务里一起删掉（用户 2026-10-16 定）。
func (s *Service) DeletePool(ctx context.Context, id uuid.UUID) error {
	if s.catalog == nil {
		return errPoolPortMissing
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.PoolByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		live, err := s.repo.LivePoolNodeCount(ctx, id)
		if err != nil {
			return err
		}
		if live > 0 {
			return errs.Newf(errs.Conflict, "这个节点池下还有节点，先把节点移到别的节点池再删")
		}
		usage, err := s.catalog.PoolUsage(ctx, []uuid.UUID{id})
		if err != nil {
			return err
		}
		if usage[id].Plans > 0 {
			return errs.Newf(errs.Conflict, "还有在售或草稿套餐绑定着这个节点池，先在套餐的线路里去掉它再删")
		}
		released, err := s.catalog.ReleaseArchivedPoolBindings(ctx, id)
		if err != nil {
			return err
		}
		if err := s.repo.DetachDeletedPoolNodes(ctx, id); err != nil {
			return err
		}
		ok, err := s.repo.DeletePool(ctx, id)
		if err != nil {
			if errors.Is(err, repo.ErrPoolReferenced) {
				return errs.Newf(errs.Conflict, "节点池仍被引用，不能删除")
			}
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		releasedIDs := make([]string, 0, len(released))
		for _, rid := range released {
			releasedIDs = append(releasedIDs, rid.String())
		}
		return s.auditPool(ctx, ActionPoolDeleted, id, map[string]any{
			"code": cur.Code, "name": cur.Name,
			"released_archived_plans": releasedIDs,
		})
	})
}

func (s *Service) auditPool(ctx context.Context, action string, id uuid.UUID, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       action,
		ResourceType: ResourceNodePool,
		ResourceID:   &id,
		Data:         data,
	})
}
