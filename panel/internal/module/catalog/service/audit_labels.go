package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
)

// 编译期断言：套餐名、套餐版本名供审计对象展示。
func _() {
	var s *Service
	var _ auditsvc.ResourceLabeler = s.PlanLabels
	var _ auditsvc.ResourceLabeler = s.PlanVersionLabels
}

// PlanLabels 是审计里资源类型 plan 的可读名（套餐名）；签名与 audit 的 ResourceLabeler 一致，查不到的 id 不在返回值里。
func (s *Service) PlanLabels(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return s.repo.PlanNamesByIDs(ctx, ids)
}

// PlanVersionLabels 是审计里资源类型 plan_version 的可读名（「套餐名 · v版本号」）；查不到的 id 不在返回值里。
func (s *Service) PlanVersionLabels(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := s.repo.VersionLabelsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(rows))
	for id, l := range rows {
		out[id] = fmt.Sprintf("%s · v%d", l.PlanName, l.Version)
	}
	return out, nil
}
