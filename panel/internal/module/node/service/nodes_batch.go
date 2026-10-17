package service

// 批量修改节点规格与服务状态。

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// NodeBatchEditMax 是一次批量修改最多的节点数。
const NodeBatchEditMax = 100

// BatchEditNodes 批量修改节点：整批一个事务，任一节点不合法整批回滚。返回改了几个节点。
// 顺序：先校验（条目、原因、Set 里给了的字段，与单个 PATCH 同一套 nodePatchPrecheck）→ 事务里按 id 升序锁住全部节点并核对版本 →
// 给了 ServingStatus 先走 BatchNodeStatus（同一事务，同一套状态机与上线前提）→ 再逐个节点用当前版本调 patchNodeTx（与单个 PATCH 同一套校验、审计与纪元）。
// 出错时错误里带是哪个节点（Fields["node_id"]）与哪个字段（原错误的 Fields 原样保留）。最后写一条 node.batch_update 审计。
func (s *Service) BatchEditNodes(ctx context.Context, in NodeBatchEdit) (int, error) {
	ids := make([]uuid.UUID, len(in.Items))
	versions := make([]int64, len(in.Items))
	want := make(map[uuid.UUID]int64, len(in.Items))
	for i, it := range in.Items {
		ids[i], versions[i] = it.ID, it.RowVersion
		want[it.ID] = it.RowVersion
	}
	sorted, ok := nodeSortedIDs(ids, versions, NodeBatchEditMax)
	if !ok {
		return 0, inventoryInvalid("items", fmt.Sprintf("必须包含 1 到 %d 个节点，ID 不重复且带 row_version", NodeBatchEditMax))
	}

	reason, err := nodeValidateReason(in.Reason)
	if err != nil {
		return 0, err
	}

	hasPatch := in.Set.PoolID.Set ||
		in.Set.TrafficRate != nil ||
		in.Set.Tags != nil ||
		in.Set.TrafficLimitBytes.Set ||
		in.Set.TrafficResetDay.Set ||
		in.Set.TrafficRateSchedule != nil
	hasServingStatus := in.Set.ServingStatus != ""

	if !hasPatch && !hasServingStatus {
		return 0, inventoryInvalid("set", "至少要改一个字段")
	}

	if hasServingStatus && !ValidNodeServingStatus(in.Set.ServingStatus) {
		return 0, inventoryInvalid("serving_status", "不支持的状态")
	}

	tmpl := NodePatch{
		RowVersion:          1,
		PoolID:              in.Set.PoolID,
		TrafficRate:         in.Set.TrafficRate,
		Tags:                in.Set.Tags,
		TrafficLimitBytes:   in.Set.TrafficLimitBytes,
		TrafficResetDay:     in.Set.TrafficResetDay,
		TrafficRateSchedule: in.Set.TrafficRateSchedule,
	}
	if hasPatch {
		if err := nodePatchPrecheck(&tmpl); err != nil {
			return 0, err
		}
	}

	var fields []string
	if in.Set.PoolID.Set {
		fields = append(fields, "pool_id")
	}
	if in.Set.TrafficRate != nil {
		fields = append(fields, "traffic_rate")
	}
	if in.Set.Tags != nil {
		fields = append(fields, "tags")
	}
	if in.Set.ServingStatus != "" {
		fields = append(fields, "serving_status")
	}
	if in.Set.TrafficLimitBytes.Set {
		fields = append(fields, "traffic_limit_bytes")
	}
	if in.Set.TrafficResetDay.Set {
		fields = append(fields, "traffic_reset_day")
	}
	if in.Set.TrafficRateSchedule != nil {
		fields = append(fields, "traffic_rate_schedule")
	}

	err = s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.NodesByIDsForUpdate(ctx, sorted)
		if err != nil {
			return err
		}
		if len(rows) != len(sorted) {
			return errs.New(errs.NotFound)
		}

		for _, row := range rows {
			if row.RowVersion != want[row.ID] {
				return batchNodeErr(row.ID, row.Name, nodeVersionConflict(row.RowVersion))
			}
		}

		if hasServingStatus {
			if err := s.BatchNodeStatus(ctx, NodeStatusBatch{
				Items:         in.Items,
				ServingStatus: in.Set.ServingStatus,
				Reason:        reason,
			}); err != nil {
				return err
			}
		}

		if hasPatch {
			currentRows, err := s.repo.NodesByIDsForUpdate(ctx, sorted)
			if err != nil {
				return err
			}
			if len(currentRows) != len(sorted) {
				return errs.New(errs.NotFound)
			}
			for _, row := range currentRows {
				p := tmpl
				p.RowVersion = row.RowVersion
				if _, err := s.patchNodeTx(ctx, row.ID, p); err != nil {
					return batchNodeErr(row.ID, row.Name, err)
				}
			}
		}

		idsStr := make([]string, len(sorted))
		for i, id := range sorted {
			idsStr[i] = id.String()
		}

		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "node.batch_update",
			ResourceType: "node_set",
			Data: map[string]any{
				"count":    len(rows),
				"node_ids": idsStr,
				"fields":   fields,
				"reason":   reason,
			},
		})
	})
	if err != nil {
		return 0, err
	}

	return len(sorted), nil
}

// batchNodeErr 复制一份 *errs.Error（不改原错误：有的是包级变量），在文案前拼接节点名称，并在 Fields 里注入 node_id。
// 带内部原因（errs.Wrap）的错误原样返回：副本带不上原因，日志会丢掉真正的出错点，而内部错误本来也不给调用方看细节。
func batchNodeErr(id uuid.UUID, name string, err error) error {
	var e *errs.Error
	if !errors.As(err, &e) || errors.Unwrap(e) != nil {
		return err
	}
	fields := make(map[string]string, len(e.Fields)+1)
	for k, v := range e.Fields {
		fields[k] = v
	}
	fields["node_id"] = id.String()
	return &errs.Error{
		Code:              e.Code,
		Message:           "节点「" + name + "」：" + e.Message,
		Fields:            fields,
		RetryAfterSeconds: e.RetryAfterSeconds,
	}
}
