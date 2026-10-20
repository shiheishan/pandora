// Package task 是 report 模块的 asynq 任务：行为趋势按天汇总的刷新与保留期清理、数据巡检。
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
	// TypeIntegrity 每 30 分钟跑一轮数据巡检：逐项检查写 integrity_checks，变坏时往管理员群组发告警（service/integrity.go）。
	TypeIntegrity = "report:integrity"
)

const (
	purgeBatch = 5000 // 每批删除行数
	maxBatches = 200  // 单次任务最多批数，删不完留给明天
)

// Service 是任务用到的 report service 方法；*service.Service 实现它。
type Service interface {
	RefreshActivity(ctx context.Context) error
	PurgeActivity(ctx context.Context, batch int) (int, error)
	RunIntegrity(ctx context.Context) error
}

// Module 注册 report 的 asynq 任务与定时调度。
type Module struct{ svc Service }

// New 构造 report 任务模块。
func New(svc Service) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeActivity, m.activity)
	mux.HandleFunc(TypeRetention, m.retention)
	mux.HandleFunc(TypeIntegrity, m.integrity)
}

// Periodic 声明行为汇总刷新、保留期清理与数据巡检。
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
		{
			// 错开 commission:settle（每小时第 7 分）与每 10 分钟的任务；不重试，下一轮就是重试。账本漂移要按科目聚合全部分录，给足 5 分钟
			Cronspec: "19,49 * * * *",
			Type:     TypeIntegrity,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(5 * time.Minute),
				asynq.Unique(29 * time.Minute),
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
	total, err := jobs.PurgeInBatches(ctx, purgeBatch, maxBatches, func(ctx context.Context, limit int) (int64, error) {
		n, err := m.svc.PurgeActivity(ctx, limit)
		return int64(n), err
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("行为汇总保留期清理: %w", err)
	}
	plog.From(ctx).Info("activity_daily retention done", zap.Int64("deleted", total))
	return nil
}

func (m *Module) integrity(ctx context.Context, _ *asynq.Task) error {
	if err := m.svc.RunIntegrity(ctx); err != nil {
		return fmt.Errorf("数据巡检: %w", err)
	}
	return nil
}

var _ jobs.Module = (*Module)(nil)
