package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是投递记录保留期清理任务类型。
const TypeRetention = "retention:notify"

const (
	purgeBatch = 5000
	maxBatches = 200
)

// Service 是任务用到的 notify service 方法。
type Service interface {
	Dispatch(ctx context.Context) (int, error)
	PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error)
}

// Module 注册 notify 相关的 asynq 任务与定时调度。
type Module struct {
	svc Service
	now func() time.Time
}

// New 构造 notify 任务模块；时钟默认为 time.Now。
func New(svc Service) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(service.TaskDispatch, m.dispatch)
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明派发兜底与保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 30s",
			Type:     service.TaskDispatch,
			Opts: []asynq.Option{
				asynq.Queue("default"),
				asynq.MaxRetry(0),
				asynq.Timeout(service.DispatchTimeout),
				asynq.Unique(25 * time.Second),
			},
		},
		{
			Cronspec: "53 3 * * *",
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

func (m *Module) dispatch(ctx context.Context, _ *asynq.Task) error {
	n, err := m.svc.Dispatch(ctx)
	if err != nil {
		return fmt.Errorf("通知派发: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("notify dispatch done", zap.Int("processed", n))
	}
	return nil
}

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	before := m.now().AddDate(0, 0, -service.RetentionDays)
	var total, batches int
	for batches = 0; batches < maxBatches; batches++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := m.svc.PurgeDeliveries(ctx, before, purgeBatch)
		if err != nil {
			return fmt.Errorf("通知保留期清理: %w", err)
		}
		total += n
		if n < purgeBatch {
			batches++
			break
		}
	}
	plog.From(ctx).Info("notify retention done", zap.Int("deleted", total), zap.Int("batches", batches))
	return nil
}

var _ jobs.Module = (*Module)(nil)
