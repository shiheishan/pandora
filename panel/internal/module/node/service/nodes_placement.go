package service

// 节点的排序。

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// nodeReorderMax 是一次排序最多的节点数。
const nodeReorderMax = 200

// nodeSortedIDs 校验一批节点引用（1–max 项、id 不重复、版本为正）并按 id 排序（批量操作统一锁序）。
func nodeSortedIDs(ids []uuid.UUID, versions []int64, max int) ([]uuid.UUID, bool) {
	if len(ids) == 0 || len(ids) > max {
		return nil, false
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for i, id := range ids {
		if seen[id] || versions[i] <= 0 {
			return nil, false
		}
		seen[id] = true
	}
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out, true
}

// ReorderNodes 批量改排序键：先全部核对版本，再逐个写；整批原子。
func (s *Service) ReorderNodes(ctx context.Context, items []NodeOrderItem) error {
	ids := make([]uuid.UUID, len(items))
	versions := make([]int64, len(items))
	want := make(map[uuid.UUID]NodeOrderItem, len(items))
	for i, it := range items {
		ids[i], versions[i] = it.ID, it.RowVersion
		want[it.ID] = it
	}
	sorted, ok := nodeSortedIDs(ids, versions, nodeReorderMax)
	if !ok {
		return inventoryInvalid("items", "必须包含 1 到 200 个节点，ID 不重复且带 row_version")
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.NodesByIDsForUpdate(ctx, sorted)
		if err != nil {
			return err
		}
		if len(rows) != len(sorted) {
			return errs.New(errs.NotFound)
		}
		for _, n := range rows {
			if n.RowVersion != want[n.ID].RowVersion {
				return nodeVersionConflict(n.RowVersion)
			}
		}
		for _, n := range rows {
			it := want[n.ID]
			ok, err := s.repo.SetNodeSortOrder(ctx, n.ID, it.RowVersion, it.SortOrder)
			if err != nil {
				return err
			}
			if !ok {
				return nodeVersionConflict(it.RowVersion)
			}
			s.nodeChanged(ctx, realtime.OpUpdate, n.ID)
		}
		return s.audit.Write(ctx, auditsvc.Event{Action: "node.order_batch", ResourceType: "node_set",
			Data: map[string]any{"count": len(rows)}})
	})
}
