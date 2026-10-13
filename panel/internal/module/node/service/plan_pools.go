package service

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// PlanPools 返回套餐页绑池选择器数据（草稿优先，否则当前发布版本）。
func (s *Service) PlanPools(ctx context.Context, planID uuid.UUID) (PlanPools, error) {
	if s.catalog == nil {
		return PlanPools{}, errPoolPortMissing
	}
	b, err := s.catalog.PoolBindingVersion(ctx, planID)
	if err != nil {
		return PlanPools{}, err
	}
	rows, err := s.repo.PickablePools(ctx, b.PoolIDs)
	if err != nil {
		return PlanPools{}, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	bound := make(map[uuid.UUID]bool, len(b.PoolIDs))
	for _, id := range b.PoolIDs {
		bound[id] = true
	}

	activeCounts := map[uuid.UUID]int64{}
	deliverableCounts := map[uuid.UUID]int64{}
	if len(ids) > 0 {
		nodeCounts, err := s.repo.PoolNodeCounts(ctx, ids)
		if err != nil {
			return PlanPools{}, err
		}
		for id, c := range nodeCounts {
			activeCounts[id] = c.Active
		}
		deliverableCounts, err = s.repo.DeliverableCountsByPool(ctx, ids)
		if err != nil {
			return PlanPools{}, err
		}
	}

	pools := make([]PlanPoolOption, len(rows))
	for i, r := range rows {
		pools[i] = PlanPoolOption{
			ID:               r.ID,
			Name:             r.Name,
			ActiveNodes:      activeCounts[r.ID],
			DeliverableNodes: deliverableCounts[r.ID],
			Bound:            bound[r.ID],
		}
	}
	return PlanPools{
		VersionID:     b.VersionID,
		VersionStatus: b.Status,
		RowVersion:    b.RowVersion,
		Editable:      b.Editable,
		Pools:         pools,
	}, nil
}

// SetPlanPools 整体替换草稿版本的绑池；只改草稿，不影响在用订阅，故不推进名单纪元。
func (s *Service) SetPlanPools(ctx context.Context, planID, versionID uuid.UUID, expectedRowVersion int64, poolIDs []uuid.UUID) (int64, error) {
	if len(poolIDs) > MaxPlanPools {
		return 0, errs.Invalid(map[string]string{"pool_ids": "一次最多绑定 500 个节点池"})
	}
	if s.catalog == nil {
		return 0, errPoolPortMissing
	}
	var next int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.lockAssignablePoolsInTx(ctx, poolIDs); err != nil {
			return err
		}
		n, err := s.catalog.SetVersionPools(ctx, planID, versionID, expectedRowVersion, poolIDs)
		if err != nil {
			return err
		}
		next = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return next, nil
}

// LockAssignablePools 按 id 升序锁住可绑定的节点池（catalog 的 PoolGuard）。
func (s *Service) LockAssignablePools(ctx context.Context, ids []uuid.UUID) error {
	unique := poolDedupeSortIDs(ids)
	if len(unique) == 0 {
		return nil
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		return s.lockAssignablePoolsInTx(ctx, unique)
	})
}

func (s *Service) lockAssignablePoolsInTx(ctx context.Context, ids []uuid.UUID) error {
	unique := poolDedupeSortIDs(ids)
	if len(unique) == 0 {
		return nil
	}
	locked, err := s.repo.LockAssignablePools(ctx, unique)
	if err != nil {
		return err
	}
	if len(locked) != len(unique) {
		return errs.Invalid(map[string]string{"pool_ids": "包含不存在或已停用的节点池"})
	}
	return nil
}

func poolDedupeSortIDs(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })
	out := make([]uuid.UUID, 0, len(sorted))
	var prev uuid.UUID
	for i, id := range sorted {
		if i == 0 || id != prev {
			out = append(out, id)
			prev = id
		}
	}
	return out
}

// CheckPublishable 在发布版本前检查绑池是否至少有一个可服务节点（catalog 的 PublishGate）。
// 「可服务」= repo.DeliverableNodeCond，与套餐页的 deliverable_nodes、套餐列表的可服务节点数同一口径。
func (s *Service) CheckPublishable(ctx context.Context, versionID uuid.UUID) error {
	if s.catalog == nil {
		return errPoolPortMissing
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		vp, err := s.catalog.VersionPools(ctx, []uuid.UUID{versionID})
		if err != nil {
			return err
		}
		ids := vp[versionID]
		if len(ids) == 0 {
			return errs.Invalid(map[string]string{"pool_ids": "发布前至少绑定一个可用节点池"})
		}
		locked, err := s.repo.LockAssignablePools(ctx, ids)
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return errs.Invalid(map[string]string{"pool_ids": "发布前至少绑定一个可用节点池"})
		}
		ok, err := s.repo.HasDeliverableNode(ctx, ids)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Invalid(map[string]string{"pool_ids": "绑定池中至少需要一个可服务节点"})
		}
		return nil
	})
}

// ServingNodeCounts 返回每个版本绑定线路里可服务节点数（catalog handler 的 NodeCounter）。
func (s *Service) ServingNodeCounts(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(versionIDs) == 0 {
		return map[uuid.UUID]int64{}, nil
	}
	if s.catalog == nil {
		return nil, errPoolPortMissing
	}
	vp, err := s.catalog.VersionPools(ctx, versionIDs)
	if err != nil {
		return nil, err
	}
	poolIDSet := make(map[uuid.UUID]struct{})
	for _, pids := range vp {
		for _, id := range pids {
			poolIDSet[id] = struct{}{}
		}
	}
	allPoolIDs := make([]uuid.UUID, 0, len(poolIDSet))
	for id := range poolIDSet {
		allPoolIDs = append(allPoolIDs, id)
	}
	sort.Slice(allPoolIDs, func(i, j int) bool { return allPoolIDs[i].String() < allPoolIDs[j].String() })

	counts := map[uuid.UUID]int64{}
	if len(allPoolIDs) > 0 {
		counts, err = s.repo.DeliverableCountsByPool(ctx, allPoolIDs)
		if err != nil {
			return nil, err
		}
	}

	out := make(map[uuid.UUID]int64, len(versionIDs))
	for _, vid := range versionIDs {
		var sum int64
		for _, pid := range vp[vid] {
			sum += counts[pid]
		}
		out[vid] = sum
	}
	return out, nil
}
