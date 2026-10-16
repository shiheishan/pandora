package service

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// PlanPools 返回套餐页绑池选择器数据（可选的池与是否已绑）。
func (s *Service) PlanPools(ctx context.Context, planID uuid.UUID) (PlanPools, error) {
	if s.catalog == nil {
		return PlanPools{}, errPoolPortMissing
	}
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{planID})
	if err != nil {
		return PlanPools{}, err
	}
	if _, ok := plans[planID]; !ok {
		return PlanPools{}, errs.New(errs.NotFound)
	}
	pp, err := s.catalog.PlanPools(ctx, []uuid.UUID{planID})
	if err != nil {
		return PlanPools{}, err
	}
	poolIDs := pp[planID]
	rows, err := s.repo.PickablePools(ctx, poolIDs)
	if err != nil {
		return PlanPools{}, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	bound := make(map[uuid.UUID]bool, len(poolIDs))
	for _, id := range poolIDs {
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
		Pools: pools,
	}, nil
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

// 发布检查的两种「没有可用池」提示。
const (
	poolNoteUnbound     = "发布前至少绑定一个节点池"
	poolNoteAllDisabled = "绑定的节点池都已停用：先在「线路 · 节点池」里启用，或换绑一个启用中的池"
)

// CheckPublishable 在发布套餐前检查套餐绑定的池是否至少有一个可服务节点（catalog 的 PublishGate）。
// 「可服务」= repo.DeliverableNodeCond，与套餐页的 deliverable_nodes、套餐列表的可服务节点数同一口径。
func (s *Service) CheckPublishable(ctx context.Context, planID uuid.UUID) error {
	if s.catalog == nil {
		return errPoolPortMissing
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		pp, err := s.catalog.PlanPools(ctx, []uuid.UUID{planID})
		if err != nil {
			return err
		}
		ids := pp[planID]
		// 「没绑池」与「绑的池都停用了」分开提示，方便管理员排查（用户 2026-10-14 定；门户侧不受影响）
		if len(ids) == 0 {
			return errs.Invalid(map[string]string{"pool_ids": poolNoteUnbound})
		}
		locked, err := s.repo.LockAssignablePools(ctx, ids)
		if err != nil {
			return err
		}
		if len(locked) == 0 {
			return errs.Invalid(map[string]string{"pool_ids": poolNoteAllDisabled})
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

// ServingNodeCounts 返回每个套餐绑定线路里可服务节点数（catalog 后台套餐列表的可服务节点数）。
func (s *Service) ServingNodeCounts(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID]int64{}, nil
	}
	if s.catalog == nil {
		return nil, errPoolPortMissing
	}
	pp, err := s.catalog.PlanPools(ctx, planIDs)
	if err != nil {
		return nil, err
	}
	poolIDSet := make(map[uuid.UUID]struct{})
	for _, pids := range pp {
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

	out := make(map[uuid.UUID]int64, len(planIDs))
	for _, pid := range planIDs {
		var sum int64
		for _, poolID := range pp[pid] {
			sum += counts[poolID]
		}
		out[pid] = sum
	}
	return out, nil
}
