// Package task 是 certs 模块的 asynq 任务：每分钟的证书巡检与单张订单的签发。
package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypePatrol 是证书巡检任务类型。
const TypePatrol = "certs:patrol"

// Service 是任务用到的 certs service 方法。
type Service interface {
	Patrol(ctx context.Context) (service.PatrolResult, error)
	IssueOrder(ctx context.Context, orderID uuid.UUID) error
}

var _ Service = (*service.Service)(nil)

// Module 注册 certs 的 asynq 任务与定时调度。
type Module struct {
	svc Service
	// envWarn 让「进程环境里有 lego 自己会读的变量」的告警每个进程只打一次
	envWarn sync.Once
	envVars func() []string
}

// New 构造 certs 任务模块。
func New(svc Service) *Module {
	return &Module{svc: svc, envVars: service.LibraryEnvWarnings}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypePatrol, m.patrol)
	mux.HandleFunc(service.TaskIssue, m.issue)
}

// Periodic 声明证书巡检（每分钟）。certs:issue 不定时，由巡检与后台操作按订单入队（TaskID cert:<订单>）。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{{
		Cronspec: "@every 1m",
		Type:     TypePatrol,
		Opts: []asynq.Option{
			asynq.Queue("default"),
			asynq.MaxRetry(0),
			asynq.Timeout(30 * time.Second),
			asynq.Unique(50 * time.Second),
		},
	}}
}

// patrol 跑一轮巡检；出错原样返回（MaxRetry 0，下一分钟再跑）。
func (m *Module) patrol(ctx context.Context, _ *asynq.Task) error {
	m.warnLibraryEnv(ctx)
	res, err := m.svc.Patrol(ctx)
	if err != nil {
		return fmt.Errorf("证书巡检: %w", err)
	}
	if res.Queued+res.Enqueued+res.Expired+res.Raised > 0 {
		plog.From(ctx).Info("证书巡检",
			zap.Int("queued", res.Queued), zap.Int("enqueued", res.Enqueued),
			zap.Int("expired", res.Expired), zap.Int("raised", res.Raised))
	}
	return nil
}

// issue 签发一张订单。除载荷解不开外一律回 nil：MaxRetry 0 的任务失败会被 asynq 归档，归档的任务占着
// TaskID cert:<订单>，同一张订单就再也入不了队；订单的失败已经记在库里（失败、退避、审计），
// 写库失败时订单停在 running，租约一过巡检按中断收掉。
func (m *Module) issue(ctx context.Context, t *asynq.Task) error {
	m.warnLibraryEnv(ctx)
	var p service.IssuePayload
	if err := jobs.Decode(t, &p); err != nil {
		return fmt.Errorf("certs:issue 载荷: %w", err)
	}
	if err := m.svc.IssueOrder(ctx, p.OrderID); err != nil {
		plog.From(ctx).Error("证书签发出错", zap.String("order", p.OrderID.String()), zap.Error(err))
	}
	return nil
}

func (m *Module) warnLibraryEnv(ctx context.Context) {
	m.envWarn.Do(func() {
		if vars := m.envVars(); len(vars) > 0 {
			// lego 会自己读这些变量：调试开关可能把 DNS 服务商 API 的请求（含令牌）打进日志，
			// CNAME / TCP 开关会改变签发行为。面板不用它们，提醒运维删掉
			plog.From(ctx).Warn("进程环境里设了 lego 自己会读的变量，可能泄露 DNS 凭据或改变签发行为，请从环境文件里删掉",
				zap.Strings("vars", vars))
		}
	})
}

var _ jobs.Module = (*Module)(nil)
