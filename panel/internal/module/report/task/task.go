// Package task 是 report 模块的 asynq 任务：行为趋势按天汇总的刷新与保留期清理。
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

const (
	// TypeActivity 每 10 分钟重算前天、昨天与今天的行为汇总（已定稿的跳过）；读路径直接用这张表。
	TypeActivity = "report:activity_daily"
	// TypeRetention 每天清理 400 天以前的行为汇总。
	TypeRetention = "retention:activity_daily"
)

const (
	purgeBatch = 5000 // 每批删除行数
	maxBatches = 200  // 单次任务最多批数，删不完留给明天
)

// Service 是任务用到的 report service 方法；*service.Service 实现它。
type Service interface {
	RefreshActivity(ctx context.Context) error
	PurgeActivity(ctx context.Context, batch int) (int, error)
}

// Module 注册 report 的 asynq 任务与定时调度。
type Module struct{ svc Service }

// New 构造 report 任务模块。
func New(svc Service) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeActivity, m.activity)
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明行为汇总刷新与保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "*/10 * * * *",
			Type:     TypeActivity,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(2 * time.Minute),
				asynq.Unique(9 * time.Minute),
			},
		},
		{
			Cronspec: "57 3 * * *",
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

func (m *Module) activity(ctx context.Context, _ *asynq.Task) error {
	if err := m.svc.RefreshActivity(ctx); err != nil {
		return fmt.Errorf("行为趋势汇总: %w", err)
	}
	return nil
}

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	var total, batches int
	for batches = 0; batches < maxBatches; batches++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := m.svc.PurgeActivity(ctx, purgeBatch)
		if err != nil {
			return fmt.Errorf("行为汇总保留期清理: %w", err)
		}
		total += n
		if n < purgeBatch {
			batches++
			break
		}
	}
	plog.From(ctx).Info("activity_daily retention done", zap.Int("deleted", total), zap.Int("batches", batches))
	return nil
}

var _ jobs.Module = (*Module)(nil)
