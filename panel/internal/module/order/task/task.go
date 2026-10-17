// Package task 是 order 模块的 asynq 任务：待付订单的超时释放。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// TypeExpireReservations 是超时释放任务类型：过了付款期限的待付订单转 expired，并退回优惠券与余额冻结。
const TypeExpireReservations = "order:expire_reservations"

// expireTimeout 是一轮释放的上限（周期 30 秒，留出余量）。
const expireTimeout = 20 * time.Second

// Service 是任务用到的 service 方法。
type Service interface {
	ExpireDue(ctx context.Context, limit int) (service.ExpireStats, error)
}

var _ Service = (*service.Service)(nil)
var _ jobs.Module = (*Module)(nil)

// Module 注册 order 的 asynq 任务与定时调度。
type Module struct {
	svc Service
}

// New 构造任务模块。
func New(svc Service) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeExpireReservations, m.expire)
}

// Periodic 声明每 30 秒一轮的超时释放（critical 队列，不重试：下一轮就是重试；Unique 与超时相同，不叠跑）。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 30s",
			Type:     TypeExpireReservations,
			Opts: []asynq.Option{
				asynq.Queue("critical"),
				asynq.MaxRetry(0),
				asynq.Timeout(expireTimeout),
				asynq.Unique(expireTimeout),
			},
		},
	}
}

func (m *Module) expire(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, expireTimeout)
	defer cancel()
	stats, err := m.svc.ExpireDue(ctx, service.ExpireBatchDefault)
	if err != nil {
		return fmt.Errorf("订单超时释放: %w", err)
	}
	if stats.Expired > 0 || stats.Failed > 0 {
		plog.From(ctx).Info("order expire reservations done",
			zap.Int("scanned", stats.Scanned),
			zap.Int("expired", stats.Expired),
			zap.Int("skipped", stats.Skipped),
			zap.Int("failed", stats.Failed))
	}
	return nil
}
