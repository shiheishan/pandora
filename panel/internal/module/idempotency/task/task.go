// Package task 注册幂等键保留期清理的 asynq 定时任务。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是清理过期幂等键的任务类型名。
const TypeRetention = "retention:idempotency"

const purgeBatch = 1000
const maxBatches = 500

// purger 由 service.Service 实现，便于任务层只依赖清理接口。
type purger interface {
	Purge(ctx context.Context, before time.Time, batch int) (int64, error)
}

// Module 实现 jobs.Module，注册保留期清理任务。
type Module struct {
	svc purger
	now func() time.Time
}

// New 构造幂等键任务模块。
func New(svc *service.Service) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 asynq mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明每天执行的保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "40 3 * * *",
			Type:     TypeRetention,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(5 * time.Minute),
				asynq.Unique(23 * time.Hour),
			},
		},
	}
}

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	before := m.now().Add(-service.Retention)
	total, err := jobs.PurgeInBatches(ctx, purgeBatch, maxBatches, func(ctx context.Context, limit int) (int64, error) {
		return m.svc.Purge(ctx, before, limit)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("清理幂等键: %w", err)
	}
	plog.From(ctx).Info("idempotency retention", zap.Int64("deleted", total))
	return nil
}
