// Package task 是 payment 模块的 asynq 任务：主动查单巡检。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeQueryPatrol 是主动查单巡检任务类型：回调丢了（渠道故障、我方 502、防火墙）时靠它把已付的钱补记上。
const TypeQueryPatrol = "payment:query_patrol"

// patrolTimeout 是一轮巡检的上限（周期 1 分钟，留出余量）。
const patrolTimeout = 50 * time.Second

// Patroller 是任务用到的 service 方法。
type Patroller interface {
	ReconcileDue(ctx context.Context) (service.PatrolStats, error)
}

var _ Patroller = (*service.Service)(nil)
var _ jobs.Module = (*Module)(nil)

// Module 注册 payment 的 asynq 任务与定时调度。
type Module struct{ svc Patroller }

// New 构造任务模块。
func New(svc Patroller) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeQueryPatrol, m.queryPatrol)
}

// Periodic 声明每分钟一轮的查单巡检：critical 队列，不重试（下一轮就是重试），Unique 略短于周期。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 1m",
			Type:     TypeQueryPatrol,
			Opts: []asynq.Option{
				asynq.Queue("critical"),
				asynq.MaxRetry(0),
				asynq.Timeout(patrolTimeout),
				asynq.Unique(patrolTimeout),
			},
		},
	}
}

func (m *Module) queryPatrol(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, patrolTimeout)
	defer cancel()
	stats, err := m.svc.ReconcileDue(ctx)
	if err != nil {
		return fmt.Errorf("查单巡检: %w", err)
	}
	if stats.Queried > 0 {
		plog.From(ctx).Info("payment query patrol done",
			zap.Int("queried", stats.Queried),
			zap.Int("reconciled", stats.Reconciled),
			zap.Int("failed", stats.Failed))
	}
	return nil
}
