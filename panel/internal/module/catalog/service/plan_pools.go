package service

// 套餐绑定的节点池（表 plan_node_pools，属主 catalog）。池归 node：绑池时由 node 在同一事务里按 id 升序锁池并检查存在、未停用（PoolGuard.LockAssignablePools）；
// 改绑立即对在用订阅生效，所以改完调 PoolGuard.MarkUsersChanged，由 node 在事务提交后推进名单纪元。

import (
	"context"
	"sort"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
)

// PoolGuard 由 node 模块实现。
type PoolGuard interface {
	// LockAssignablePools 在同一事务里按 id 升序锁住这些池，有不存在或已停用的回 422（fields.pool_ids）。ids 已去重。
	LockAssignablePools(ctx context.Context, ids []uuid.UUID) error
	// MarkUsersChanged 登记本事务提交后推进名单纪元（同一事务里多次调用只生效一次）。
	MarkUsersChanged(ctx context.Context)
}

// UsePoolGuard 只在启动装配时调用（不并发安全）。
func (s *Service) UsePoolGuard(g PoolGuard) { s.pools = g }

// PoolBinding 是一个节点池与套餐的单条绑定。
type PoolBinding struct {
	PlanID   uuid.UUID
	PlanName string
	// OnSale 是套餐在售（active 且有当前版本）。
	OnSale bool
	// VersionIDs 是这个套餐的全部版本（任何状态，按版本号升序），给 node 判断「有没有人在用」。永不为 nil。
	VersionIDs []uuid.UUID
}

// PoolUsage 是一个节点池被套餐引用的情况。
type PoolUsage struct {
	Plans     int           // 绑定了这个池的套餐数（任何状态）
	PlanNames []string      // 这些套餐的名字，去重后升序，永不为 nil
	Bindings  []PoolBinding // 按套餐名、套餐 id 排序，永不为 nil
}

// PlanPools 批量取套餐绑定的池：套餐 id → 池 id（升序）。没绑的套餐不在 map 里。
func (s *Service) PlanPools(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(planIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	out, err := s.repo.PlanPools(ctx, planIDs)
	return out, catalogErr(err)
}

// PoolUsage 批量取池被套餐引用的情况：池 id → 用量。没被引用的池不在 map 里。
func (s *Service) PoolUsage(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]PoolUsage, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID]PoolUsage{}, nil
	}
	rows, err := s.repo.PoolPlanRows(ctx, poolIDs)
	if err != nil {
		return nil, catalogErr(err)
	}
	planIDSet := make(map[uuid.UUID]struct{})
	for _, row := range rows {
		planIDSet[row.PlanID] = struct{}{}
	}
	planIDs := make([]uuid.UUID, 0, len(planIDSet))
	for id := range planIDSet {
		planIDs = append(planIDs, id)
	}
	verByPlan, err := s.repo.VersionIDsByPlans(ctx, planIDs)
	if err != nil {
		return nil, catalogErr(err)
	}

	out := make(map[uuid.UUID]PoolUsage)
	for _, row := range rows {
		u := out[row.PoolID]
		u.Plans++
		u.PlanNames = append(u.PlanNames, row.PlanName)
		vids := verByPlan[row.PlanID]
		if vids == nil {
			vids = []uuid.UUID{}
		}
		u.Bindings = append(u.Bindings, PoolBinding{
			PlanID:     row.PlanID,
			PlanName:   row.PlanName,
			OnSale:     row.OnSale,
			VersionIDs: vids,
		})
		out[row.PoolID] = u
	}
	for pid, u := range out {
		if u.Bindings == nil {
			u.Bindings = []PoolBinding{}
		} else {
			sort.Slice(u.Bindings, func(i, j int) bool {
				if u.Bindings[i].PlanName != u.Bindings[j].PlanName {
					return u.Bindings[i].PlanName < u.Bindings[j].PlanName
				}
				return u.Bindings[i].PlanID.String() < u.Bindings[j].PlanID.String()
			})
		}
		seen := make(map[string]bool, len(u.PlanNames))
		uniq := make([]string, 0, len(u.PlanNames))
		for _, name := range u.PlanNames {
			if seen[name] {
				continue
			}
			seen[name] = true
			uniq = append(uniq, name)
		}
		sort.Strings(uniq)
		u.PlanNames = uniq
		if u.PlanNames == nil {
			u.PlanNames = []string{}
		}
		out[pid] = u
	}
	return out, nil
}

func uuidSlicesEqualSet(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	ac := append([]uuid.UUID(nil), a...)
	bc := append([]uuid.UUID(nil), b...)
	sort.Slice(ac, func(i, j int) bool { return ac[i].String() < ac[j].String() })
	sort.Slice(bc, func(i, j int) bool { return bc[i].String() < bc[j].String() })
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// addedPools 返回 next 里 before 没有的池。
func addedPools(before, next []uuid.UUID) []uuid.UUID {
	had := make(map[uuid.UUID]bool, len(before))
	for _, id := range before {
		had[id] = true
	}
	out := []uuid.UUID{}
	for _, id := range next {
		if !had[id] {
			out = append(out, id)
		}
	}
	return out
}

// replacePoolsTx 整体替换套餐绑池（CreatePlan 与 SetPlanPools 共用）。added 是这次新加的池：只检查它们存在、未停用；
// 原来就绑着的池即使后来停用了也照样保留（否则改别的线路时得连它一起解绑，那个池上的老用户立刻掉线）。
func (s *Service) replacePoolsTx(ctx context.Context, planID uuid.UUID, poolIDs, added []uuid.UUID) error {
	if len(added) > 0 {
		if s.pools == nil {
			return errs.Newf(errs.Unavailable, "节点池模块未装配，暂时不能绑定线路")
		}
		ids := append([]uuid.UUID(nil), added...)
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
		if err := s.pools.LockAssignablePools(ctx, ids); err != nil {
			return err
		}
	}
	if err := s.repo.ReplacePlanPools(ctx, planID, poolIDs); err != nil {
		return err
	}
	if s.pools != nil {
		s.pools.MarkUsersChanged(ctx)
	}
	s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
	return nil
}

// SetPlanPools 整体替换套餐的线路。
func (s *Service) SetPlanPools(ctx context.Context, planID uuid.UUID, in PoolsUpdate) (PlanDetail, error) {
	if err := checkExpected(in.ExpectedRowVersion); err != nil {
		return PlanDetail{}, err
	}
	fields := map[string]string{}
	validatePoolIDs(in.PoolIDs, fields)
	if err := invalid(fields); err != nil {
		return PlanDetail{}, err
	}

	var detail PlanDetail
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.lockEditablePlan(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		beforeMap, err := s.repo.PlanPools(ctx, []uuid.UUID{planID})
		if err != nil {
			return err
		}
		before := beforeMap[planID]
		if before == nil {
			before = []uuid.UUID{}
		}
		if uuidSlicesEqualSet(before, in.PoolIDs) {
			detail, err = s.GetPlan(ctx, planID)
			return err
		}

		if err := s.replacePoolsTx(ctx, planID, in.PoolIDs, addedPools(before, in.PoolIDs)); err != nil {
			return err
		}
		// 已发布的套餐改线路后仍要过上架检查（线路里有可服务的节点），与发布时同一道闸：不能一步改成没有线路
		if cur.CurrentVersionID != nil && s.gate != nil {
			if err := s.gate.CheckPublishable(ctx, planID); err != nil {
				return err
			}
		}
		next, err := s.bumpPlanRow(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		auditAfter := in.PoolIDs
		if auditAfter == nil {
			auditAfter = []uuid.UUID{}
		}
		if err := s.writeAudit(ctx, ActionPlanPools, ResourcePlan, planID, map[string]any{
			"before": map[string]any{"row_version": in.ExpectedRowVersion, "pool_ids": before},
			"after":  map[string]any{"row_version": next, "pool_ids": auditAfter},
		}); err != nil {
			return err
		}
		detail, err = s.GetPlan(ctx, planID)
		return err
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return detail, nil
}
