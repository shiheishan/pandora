// Package task 是 support 模块的 asynq 任务：工单首次响应超过 SLA 时自动升级。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// TypeEscalateSLA 是 SLA 升级任务类型：首次响应已超时、还没响应的工单置为 escalated 并升一级优先级。
const TypeEscalateSLA = "support:escalate_sla"

// escalateTimeout 是一轮升级的上限（docs/architecture.md 第 12 节）。
const escalateTimeout = 30 * time.Second

// Service 是任务用到的 service 方法。
type Service interface {
	EscalateOverdue(ctx context.Context, reply func(int) (idemsvc.Response, error)) (int, error)
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
}

// Periodic 声明每 5 分钟一轮的 SLA 升级（default 队列，不重试：下一轮就是重试；Unique 短于周期防叠跑）。
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
	}
}

// escalate 跑一轮升级；一轮最多 service.EscalateBatch 张，剩下的下一轮接着处理。
func (m *Module) escalate(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, escalateTimeout)
	defer cancel()
	n, err := m.svc.EscalateOverdue(ctx, nil)
	if err != nil {
		return fmt.Errorf("工单 SLA 升级: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("support sla escalate done", zap.Int("escalated", n))
	}
	return nil
}
