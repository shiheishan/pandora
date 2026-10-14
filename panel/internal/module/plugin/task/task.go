// Package task 是 plugin 模块的 asynq 任务：投递派发与投递记录的保留期清理。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/plugin/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是投递记录保留期清理任务类型。
const TypeRetention = "retention:plugin"

const (
	purgeBatch = 5000
	maxBatches = 200
)

// Service 是任务用到的 plugin service 方法。
type Service interface {
	Dispatch(ctx context.Context) (int, error)
	PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error)
}

// Module 注册 plugin 相关的 asynq 任务与定时调度。
type Module struct {
	svc Service
	now func() time.Time
}

// New 构造 plugin 任务模块；时钟默认为 time.Now。
func New(svc Service) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(service.TaskDispatch, m.dispatch)
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明派发兜底与投递记录保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 1m",
			Type:     service.TaskDispatch,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(service.DispatchTimeout),
				// 唯一窗口不短于超时：上一轮还没跑完时不再排一轮
				asynq.Unique(service.DispatchTimeout + time.Minute),
			},
		},
		{
			Cronspec: "56 3 * * *",
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

// dispatch 出错只记日志、不返回错误：任务不重试（下一分钟的周期任务会再跑），返回错误只会让任务进归档，
// 而归档的任务会一直占着提交后入队用的固定 TaskID，让之后的入队全部冲突丢弃。
func (m *Module) dispatch(ctx context.Context, _ *asynq.Task) error {
	n, err := m.svc.Dispatch(ctx)
	if err != nil {
		plog.From(ctx).Error("插件投递派发失败，等下一轮", zap.Error(err))
		return nil
	}
	if n > 0 {
		plog.From(ctx).Info("plugin dispatch done", zap.Int("processed", n))
	}
	return nil
}

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	before := m.now().AddDate(0, 0, -service.RetentionDays)
	total, err := jobs.PurgeInBatches(ctx, purgeBatch, maxBatches, func(ctx context.Context, limit int) (int64, error) {
		n, err := m.svc.PurgeDeliveries(ctx, before, limit)
		return int64(n), err
	})
	if err != nil {
		return fmt.Errorf("插件投递保留期清理: %w", err)
	}
	plog.From(ctx).Info("plugin retention done", zap.Int64("deleted", total))
	return nil
}

var _ jobs.Module = (*Module)(nil)
