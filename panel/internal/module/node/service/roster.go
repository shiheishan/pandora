package service

// 名单（p5-delivery 实现）。

import (
	"context"
	"slices"

	"github.com/google/uuid"
)

// Rosters 一次算出这些服务器上每个该跑的节点（repo.ServedOnServerCond）的完整名单：服务器 id → 名单（按节点 id 升序）。
// 会话层在名单纪元变了、节点新建流时调；一批服务器共用一次查询，同一内容的名单只编码一次。
// 不存在或已删除的服务器不在结果里；存在但没有该跑节点的服务器对应空切片（节点端停掉全部入站）。
//
// 一轮固定 6 次读（与服务器、节点、订阅个数无关）：该跑的节点与池、池的用户组名单、可服务的订阅、
// active 账号与组、套餐版本（catalog 内 3 条：版本、权益、配额）、版本绑池；然后在内存里按池分发。
// 名单只取决于节点所在的池（节点自身条件已在 ServedOnServerCond 里），所以按池算一次、同池的节点共用
// 同一个 Users 切片与版本（只读，调用方不得修改）。
func (s *Service) Rosters(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeRoster, error) {
	out := make(map[uuid.UUID][]NodeRoster, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	if s.subs == nil || s.users == nil || s.versions == nil || s.catalog == nil {
		return nil, errDeliveryPortMissing
	}
	ids := slices.Clone(serverIDs)
	slices.SortFunc(ids, compareUUID)
	ids = slices.Compact(ids)
	rows, err := s.repo.ServedNodes(ctx, ids)
	if err != nil {
		return nil, err
	}
	var pools []uuid.UUID
	for _, r := range rows {
		if r.NodeID != nil && r.PoolID != nil {
			pools = append(pools, *r.PoolID)
		}
	}
	slices.SortFunc(pools, compareUUID)
	pools = slices.Compact(pools)
	byPool, err := s.poolRosters(ctx, pools)
	if err != nil {
		return nil, err
	}
	empty := poolRoster{users: []RosterUser{}, version: rosterVersion(nil)}
	for _, r := range rows {
		list, ok := out[r.ServerID]
		if !ok {
			list = []NodeRoster{}
		}
		if r.NodeID != nil {
			pr := empty
			if r.PoolID != nil {
				pr = byPool[*r.PoolID]
			}
			list = append(list, NodeRoster{NodeID: *r.NodeID, Version: pr.version, Users: pr.users})
		}
		out[r.ServerID] = list
	}
	for id, list := range out {
		slices.SortFunc(list, func(a, b NodeRoster) int { return compareUUID(a.NodeID, b.NodeID) })
		out[id] = list
	}
	return out, nil
}

// poolRoster 是一个池的名单与版本（同池的节点共用）。
type poolRoster struct {
	users   []RosterUser
	version string
}

// poolRosters 按池算名单：订阅侧可服务、主人账号 active、套餐版本绑了这个池、池的用户组限定放行（poolAdmits）。
// 每个池都有结果（没人可放行的是空名单）；pools 为空时不查库。
func (s *Service) poolRosters(ctx context.Context, pools []uuid.UUID) (map[uuid.UUID]poolRoster, error) {
	out := make(map[uuid.UUID]poolRoster, len(pools))
	if len(pools) == 0 {
		return out, nil
	}
	poolGroups, err := s.repo.PoolUserGroupIDs(ctx, pools)
	if err != nil {
		return nil, err
	}
	subs, err := s.subs.ServingSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	userIDs := make([]uuid.UUID, 0, len(subs))
	versionIDs := make([]uuid.UUID, 0)
	for _, sub := range subs {
		userIDs = append(userIDs, sub.UserID)
		versionIDs = append(versionIDs, sub.PlanVersionID)
	}
	slices.SortFunc(userIDs, compareUUID)
	userIDs = slices.Compact(userIDs)
	slices.SortFunc(versionIDs, compareUUID)
	versionIDs = slices.Compact(versionIDs)

	groups, err := s.users.ActiveUserGroups(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	versions, err := s.versions.VersionsByIDs(ctx, versionIDs)
	if err != nil {
		return nil, err
	}
	versionPools, err := s.catalog.VersionPools(ctx, versionIDs)
	if err != nil {
		return nil, err
	}

	wanted := make(map[uuid.UUID]bool, len(pools))
	for _, p := range pools {
		wanted[p] = true
	}
	users := make(map[uuid.UUID][]RosterUser, len(pools))
	// subs 已按 NodeUID 升序，追加后每个池的名单天然有序
	for _, sub := range subs {
		group, active := groups[sub.UserID]
		if !active {
			continue
		}
		v, ok := versions[sub.PlanVersionID]
		if !ok {
			continue
		}
		speed, devices := rosterLimits(sub.DeviceLimit, v)
		u := RosterUser{UID: sub.NodeUID, UUID: sub.ProxyUUID, SpeedLimitKbps: speed, DeviceLimit: devices}
		for _, p := range versionPools[sub.PlanVersionID] {
			if wanted[p] && poolAdmits(poolGroups[p], group) {
				users[p] = append(users[p], u)
			}
		}
	}
	for _, p := range pools {
		list := users[p]
		if list == nil {
			list = []RosterUser{}
		}
		// rosterVersion 发现无序会先排序；这里保持名单本身也有序（契约：按 UID 升序）
		slices.SortFunc(list, compareRosterUser)
		out[p] = poolRoster{users: list, version: rosterVersion(list)}
	}
	return out, nil
}

// UsersChanged 实现 identity 的 RosterNotifier：账号状态或用户组变了（已在提交后调用），推进名单纪元。
func (s *Service) UsersChanged(ctx context.Context, _ []uuid.UUID) {
	s.bump(ctx, UsersEpochKey, "users")
}
