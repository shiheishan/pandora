// Package task 是 support 模块的 asynq 任务：SLA 升级与自动关闭。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// TypeEscalateSLA 是 SLA 升级任务类型：首次响应已超时、还没响应的工单置为 escalated 并升一级优先级。
const TypeEscalateSLA = "support:escalate_sla"

// TypeAutoClose 是工单自动关闭任务类型：超时未回复的 pending_user 工单自动关闭。
const TypeAutoClose = "support:auto_close"

// escalateTimeout 是一轮升级的上限（docs/architecture.md 第 12 节）。
const escalateTimeout = 30 * time.Second

// autoCloseTimeout 是一轮自动关闭的上限。
const autoCloseTimeout = 30 * time.Second

// Service 是任务用到的 service 方法。
type Service interface {
	EscalateOverdueScheduled(ctx context.Context) (int, error)
	AutoCloseScheduled(ctx context.Context) (int, error)
}

var _ Service = (*service.Service)(nil)
var _ jobs.Module = (*Module)(nil)

// Module 注册 support 的 asynq 任务与定时调度。
type Module struct {
	svc Service
}

// New 构造任务模块。
func New(svc Service) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeEscalateSLA, m.escalate)
	mux.HandleFunc(TypeAutoClose, m.autoClose)
}

// Periodic 声明 SLA 升级与自动关闭的定时调度（default 队列，不重试：下一轮就是重试；Unique 短于周期防叠跑）。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "*/5 * * * *",
			Type:     TypeEscalateSLA,
			Opts: []asynq.Option{
				asynq.Queue("default"),
				asynq.MaxRetry(0),
				asynq.Timeout(escalateTimeout),
				asynq.Unique(4 * time.Minute),
			},
		},
		{
			Cronspec: "2-59/10 * * * *",
			Type:     TypeAutoClose,
			Opts: []asynq.Option{
				asynq.Queue("default"),
				asynq.MaxRetry(0),
				asynq.Timeout(autoCloseTimeout),
				asynq.Unique(9 * time.Minute),
			},
		},
	}
}

// escalate 跑一轮升级；一轮最多 service.EscalateBatch 张，剩下的下一轮接着处理。
func (m *Module) escalate(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, escalateTimeout)
	defer cancel()
	n, err := m.svc.EscalateOverdueScheduled(ctx)
	if err != nil {
		return fmt.Errorf("工单 SLA 升级: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("support sla escalate done", zap.Int("escalated", n))
	}
	return nil
}

// autoClose 跑一轮自动关闭；一轮最多 service.AutoCloseBatch 张，剩下的下一轮接着处理。
func (m *Module) autoClose(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, autoCloseTimeout)
	defer cancel()
	n, err := m.svc.AutoCloseScheduled(ctx)
	if err != nil {
		return fmt.Errorf("工单自动关闭: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("support auto close done", zap.Int("closed", n))
	}
	return nil
}
