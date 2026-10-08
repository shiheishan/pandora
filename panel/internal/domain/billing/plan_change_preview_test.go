package billing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PreviewPlanChange 已随门户的 change-plan/preview 接口退役（报价接口取代，设计稿 2.2）。
// PG18 用例仍要在不下单的前提下读一次「建单会算出的那组数」：这里用同一个 quotePlanChange
// 在一个回滚的事务里算，只给测试用。
func (s *Service) PreviewPlanChange(ctx context.Context, tenantID string,
	in PlanChangeInput) (*PlanChangePreview, error) {
	var out PlanChangePreview
	errRollback := errors.New("preview rollback")
	err := s.pool.InTx(ctx, dbScope(tenantID, in.UserID), func(tx pgx.Tx) error {
		now := time.Now().UTC()
		q, err := quotePlanChange(ctx, tx, tenantID, in, now, now)
		if err != nil {
			return err
		}
		out = q.PlanChangePreview
		return errRollback
	})
	if err != nil && err != errRollback {
		return nil, err
	}
	return &out, nil
}
