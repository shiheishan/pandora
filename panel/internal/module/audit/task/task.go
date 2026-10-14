package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是审计保留期清理任务类型。
const TypeRetention = "retention:audit"

const (
	RetentionDays = 400  // 产品定的审计保留期
	purgeBatch    = 5000 // 每批删除行数
	maxBatches    = 200  // 单次任务最多批数，删不完留给明天
)

// Purger 是任务用到的 service 方法。
type Purger interface {
	Purge(ctx context.Context, before time.Time, batch int) (int, error)
}

// Module 注册审计相关的 asynq 任务与定时调度。
type Module struct {
	svc Purger
	now func() time.Time
}

// New 构造审计任务模块；时钟默认为 time.Now。
func New(svc Purger) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明每天一次的保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "41 3 * * *",
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
	before := m.now().AddDate(0, 0, -RetentionDays)
	total, err := jobs.PurgeInBatches(ctx, purgeBatch, maxBatches, func(ctx context.Context, limit int) (int64, error) {
		n, err := m.svc.Purge(ctx, before, limit)
		return int64(n), err
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("审计保留期清理: %w", err)
	}
	plog.From(ctx).Info("audit retention done", zap.Int64("deleted", total))
	return nil
}

var _ jobs.Module = (*Module)(nil)
