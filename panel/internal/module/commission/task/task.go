// Package task 是 commission 模块的 asynq 任务：冻结期满的佣金解冻。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// TypeSettle 是佣金解冻任务类型：冻结期已满、不需复核的佣金转为可提现。
const TypeSettle = "commission:settle"

// settleTimeout 是一轮解冻的上限。
const settleTimeout = time.Minute

// Service 是任务用到的 service 方法。
type Service interface {
	SettleDue(ctx context.Context, limit int) (service.SettleStats, error)
}

var _ Service = (*service.Service)(nil)
var _ jobs.Module = (*Module)(nil)

// Module 注册 commission 的 asynq 任务与定时调度。
type Module struct {
	svc Service
}

// New 构造任务模块。
func New(svc Service) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeSettle, m.settle)
}

// Periodic 声明每小时第 7 分钟一轮的解冻（critical 队列，不重试：下一轮就是重试；Unique 防叠跑）。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "7 * * * *",
			Type:     TypeSettle,
			Opts: []asynq.Option{
				asynq.Queue("critical"),
				asynq.MaxRetry(0),
				asynq.Timeout(settleTimeout),
				asynq.Unique(55 * time.Minute),
			},
		},
	}
}

func (m *Module) settle(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()
	stats, err := m.svc.SettleDue(ctx, service.SettleBatchDefault)
	// 只有补转（或补转失败）、没有新到期条目的那一轮也要留日志，不然查「为什么一直重试」时看不到
	if stats.Scanned > 0 || stats.AutoTransferred > 0 || stats.Failed > 0 {
		plog.From(ctx).Info("commission settle done",
			zap.Int("scanned", stats.Scanned),
			zap.Int("settled", stats.Settled),
			zap.Int("skipped", stats.Skipped),
			zap.Int("quarantined", stats.Quarantined),
			zap.Int("failed", stats.Failed),
			zap.Int("auto_transferred", stats.AutoTransferred))
	}
	if err != nil {
		return fmt.Errorf("佣金解冻: %w", err)
	}
	return nil
}
