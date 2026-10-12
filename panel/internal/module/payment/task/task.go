// Package task 是 payment 模块的 asynq 任务：主动查单巡检、验签失败事件的保留期清理。
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

// TypeRetention 是验签失败回调事件的保留期清理任务类型。
const TypeRetention = "retention:payment_unverified_events"

// patrolTimeout 是一轮巡检的上限（周期 1 分钟，留出余量）。
const patrolTimeout = 50 * time.Second

const (
	// RetentionDays 是验签失败事件的保留期（库函数同样只删 30 天以前的）。
	RetentionDays = 30
	purgeBatch    = 5000 // 每批删除行数
	maxBatches    = 200  // 单次任务最多批数，删不完留给明天
)

// Service 是任务用到的 service 方法。
type Service interface {
	ReconcileDue(ctx context.Context) (service.PatrolStats, error)
	PurgeUnverifiedEvents(ctx context.Context, before time.Time, batch int) (int, error)
}

var _ Service = (*service.Service)(nil)
var _ jobs.Module = (*Module)(nil)

// Module 注册 payment 的 asynq 任务与定时调度。
type Module struct {
	svc Service
	now func() time.Time
}

// New 构造任务模块；时钟默认为 time.Now。
func New(svc Service) *Module { return &Module{svc: svc, now: time.Now} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeQueryPatrol, m.queryPatrol)
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明每分钟一轮的查单巡检（critical 队列，不重试：下一轮就是重试，Unique 略短于周期）
// 和每天一次的保留期清理（low 队列）。
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

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	before := m.now().AddDate(0, 0, -RetentionDays)
	var total, batches int
	for batches = 0; batches < maxBatches; batches++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := m.svc.PurgeUnverifiedEvents(ctx, before, purgeBatch)
		if err != nil {
			return fmt.Errorf("验签失败事件保留期清理: %w", err)
		}
		total += n
		if n < purgeBatch {
			batches++
			break
		}
	}
	plog.From(ctx).Info("payment unverified events retention done", zap.Int("deleted", total), zap.Int("batches", batches))
	return nil
}
