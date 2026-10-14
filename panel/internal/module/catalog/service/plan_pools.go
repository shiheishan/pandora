package service

// 套餐版本绑定的节点池（表 plan_node_pools，属主 catalog）。
//
// 池本身归 node 模块：池存不存在、停没停用由 node 在同一事务里先锁好（PoolGuard），
// catalog 只管绑定行、版本的 row_version 与冻结。订阅按开通时的版本取线路，所以只有草稿版本能改绑池；
// 已发布版本的绑池由 00019 的触发器兜底（23001 → repo.ErrFrozen → ErrSnapshotFrozen）。

import (
	"context"
	"sort"

	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
)

// ActionVersionPoolsChanged 是替换草稿版本绑池的审计动作名。
const ActionVersionPoolsChanged = "plan_version.pools_changed"

// PoolGuard 由 node 模块实现：按 id 升序 FOR SHARE 锁住这些池，有不存在或已停用的回 422（fields.pool_ids）。
// 在调用方事务里执行；ids 已去重。
type PoolGuard interface {
	LockAssignablePools(ctx context.Context, ids []uuid.UUID) error
}

// UsePoolGuard 只在启动装配时调用（不并发安全）。没装配时向导带非空 pool_ids 回 503。
func (s *Service) UsePoolGuard(g PoolGuard) { s.pools = g }

// PoolBindingVersion 是套餐页绑池选择器对应的版本：有草稿给草稿（可编辑），否则给当前已发布版本（只读）；
// 两者都没有时 VersionID 为 nil、PoolIDs 为空。
type PoolBindingVersion struct {
	VersionID  *uuid.UUID
	Status     string
	RowVersion int64
	Editable   bool
	// PoolIDs 按 id 升序，永不为 nil。
	PoolIDs []uuid.UUID
}

// PoolBinding 是一个节点池与套餐版本的单条绑定关系。
type PoolBinding struct {
	VersionID uuid.UUID
	PlanName  string
	OnSale    bool
}

// PoolUsage 是一个节点池被套餐版本引用的情况。
type PoolUsage struct {
	// Versions 是绑定了这个池的套餐版本数（任何状态）。
	Versions int
	// PlanNames 是这些版本所属套餐的名字（任何状态），去重后按名字升序，永不为 nil。
	PlanNames []string
	// Bindings 是这个池的全部绑定（带在售标记），按套餐名、版本 id 排序，永不为 nil。
	Bindings []PoolBinding
}

// VersionPools 批量取版本绑定的池（一条 SQL）：版本 id → 池 id（升序）。没绑的版本不在 map 里。
func (s *Service) VersionPools(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	if len(versionIDs) == 0 {
		return map[uuid.UUID][]uuid.UUID{}, nil
	}
	out, err := s.repo.VersionPools(ctx, versionIDs)
	return out, catalogErr(err)
}

// PoolUsage 批量取池被套餐版本引用的情况（一条 SQL）：池 id → 用量。没被引用的池不在 map 里。
func (s *Service) PoolUsage(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]PoolUsage, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID]PoolUsage{}, nil
	}
	rows, err := s.repo.PoolPlanRows(ctx, poolIDs)
	if err != nil {
		return nil, catalogErr(err)
	}
	out := make(map[uuid.UUID]PoolUsage)
	for _, row := range rows {
		u := out[row.PoolID]
		u.Versions++
		u.PlanNames = append(u.PlanNames, row.PlanName)
		u.Bindings = append(u.Bindings, PoolBinding{
			VersionID: row.PlanVersionID,
			PlanName:  row.PlanName,
			OnSale:    row.OnSale,
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
				return u.Bindings[i].VersionID.String() < u.Bindings[j].VersionID.String()
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

// PoolBindingVersion 取套餐页绑池选择器对应的版本与它的绑池；套餐不存在回 ErrNotFound。
func (s *Service) PoolBindingVersion(ctx context.Context, planID uuid.UUID) (PoolBindingVersion, error) {
	plan, found, err := s.repo.PlanByID(ctx, planID, repo.NoLock)
	if err != nil {
		return PoolBindingVersion{}, catalogErr(err)
	}
	if !found {
		return PoolBindingVersion{}, ErrNotFound
	}

	var vid uuid.UUID
	var editable bool
	drafts, err := s.repo.DraftVersionIDs(ctx, []uuid.UUID{planID})
	if err != nil {
		return PoolBindingVersion{}, catalogErr(err)
	}
	if id, ok := drafts[planID]; ok {
		vid = id
		editable = true
	} else if plan.CurrentVersionID != nil {
		vid = *plan.CurrentVersionID
		editable = false
	} else {
		return PoolBindingVersion{PoolIDs: []uuid.UUID{}}, nil
	}

	ver, found, err := s.repo.VersionByID(ctx, planID, vid, repo.NoLock)
	if err != nil {
		return PoolBindingVersion{}, catalogErr(err)
	}
	if !found {
		return PoolBindingVersion{}, ErrNotFound
	}

	poolMap, err := s.repo.VersionPools(ctx, []uuid.UUID{vid})
	if err != nil {
		return PoolBindingVersion{}, catalogErr(err)
	}
	pools := poolMap[vid]
	if pools == nil {
		pools = []uuid.UUID{}
	}

	return PoolBindingVersion{
		VersionID:  &vid,
		Status:     ver.Status,
		RowVersion: ver.RowVersion,
		Editable:   editable,
		PoolIDs:    pools,
	}, nil
}

// SetVersionPools 整体替换草稿版本的绑池，返回版本新的 row_version。poolIDs 已去重；
// 池的存在与状态由调用方（node）在同一事务里先锁好。ctx 里有事务就加入。
func (s *Service) SetVersionPools(ctx context.Context, planID, versionID uuid.UUID, expectedRowVersion int64, poolIDs []uuid.UUID) (int64, error) {
	if expectedRowVersion <= 0 {
		return 0, errs.Invalid(map[string]string{"expected_version_row_version": "必须是正整数"})
	}
	var next int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.VersionByID(ctx, planID, versionID, repo.ForUpdate)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if cur.Status != VersionDraft {
			return errs.Newf(errs.Conflict, "只有未发布的草稿版本可以修改节点池")
		}
		if cur.RowVersion != expectedRowVersion {
			return rowConflict("套餐版本", cur.RowVersion)
		}

		beforeMap, err := s.repo.VersionPools(ctx, []uuid.UUID{versionID})
		if err != nil {
			return err
		}
		before := beforeMap[versionID]
		if before == nil {
			before = []uuid.UUID{}
		}

		if err := s.repo.ReplaceVersionPools(ctx, versionID, poolIDs); err != nil {
			return err
		}

		ok, err := s.repo.BumpDraftVersionRow(ctx, versionID, expectedRowVersion)
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("套餐版本", cur.RowVersion)
		}
		s.catalogChanged(ctx, rtTableVersions, realtime.OpUpdate, versionID)

		next = expectedRowVersion + 1
		auditPools := poolIDs
		if auditPools == nil {
			auditPools = []uuid.UUID{}
		}
		return s.writeAudit(ctx, ActionVersionPoolsChanged, ResourcePlanVersion, versionID, map[string]any{
			"before": map[string]any{"row_version": expectedRowVersion, "pool_ids": before},
			"after":  map[string]any{"row_version": next, "pool_ids": auditPools, "plan_id": planID},
		})
	})
	return next, catalogErr(err)
}

// bindWizardPoolsTx 在向导事务里把版本的绑池整体替换成 poolIDs（已去重；空 = 清空）：
// 非空时先经 PoolGuard 锁池（没装配回 503），再替换。不推进 row_version（发布时现读）。
func (s *Service) bindWizardPoolsTx(ctx context.Context, versionID uuid.UUID, poolIDs []uuid.UUID) error {
	if len(poolIDs) > 0 {
		if s.pools == nil {
			return errs.Newf(errs.Unavailable, "节点池模块未装配，暂时不能绑定线路")
		}
		ids := append([]uuid.UUID(nil), poolIDs...)
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
		if err := s.pools.LockAssignablePools(ctx, ids); err != nil {
			return err
		}
	}
	if err := s.repo.ReplaceVersionPools(ctx, versionID, poolIDs); err != nil {
		return err
	}
	s.catalogChanged(ctx, rtTableVersions, realtime.OpUpdate, versionID)
	return nil
}
