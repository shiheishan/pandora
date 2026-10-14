// Package task 是 subscription 模块的 asynq 任务：订阅到期、配额周期滚动、拉取日志的保留期清理。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// 任务类型。
const (
	TypeExpire            = "subscription:expire"
	TypeRollQuota         = "subscription:roll_quota"
	TypeRetentionFetchLog = "retention:subscription_fetch_log"
)

const (
	expireTimeout    = time.Minute     // 一轮到期扫描的上限
	rollTimeout      = time.Minute     // 一轮配额滚动的上限
	retentionTimeout = 5 * time.Minute // 一次保留期清理的上限
	maxPurgeBatches  = 200             // 单次清理最多批数，删不完留给明天
)

// Service 是任务用到的 service 方法。
type Service interface {
	ExpireDue(ctx context.Context) (service.ExpireStats, error)
	RollQuotas(ctx context.Context) (service.RollStats, error)
	PurgeFetchLogs(ctx context.Context) (int64, error)
}

var _ Service = (*service.Service)(nil)
var _ jobs.Module = (*Module)(nil)

// Module 注册 subscription 的 asynq 任务与定时调度。
type Module struct{ svc Service }

// New 构造任务模块。
func New(svc Service) *Module { return &Module{svc: svc} }

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeExpire, m.expire)
	mux.HandleFunc(TypeRollQuota, m.rollQuota)
	mux.HandleFunc(TypeRetentionFetchLog, m.retention)
}

// Periodic 声明订阅到期（每分钟）、配额滚动（每 10 分钟）与拉取日志保留期清理（每天凌晨）。
// 周期任务不重试（下一轮就是重试），Unique 略短于周期防叠跑。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 1m",
			Type:     TypeExpire,
			Opts: []asynq.Option{
				asynq.Queue("critical"),
				asynq.MaxRetry(0),
				asynq.Timeout(expireTimeout),
				asynq.Unique(55 * time.Second),
			},
		},
		{
			Cronspec: "@every 10m",
			Type:     TypeRollQuota,
			Opts: []asynq.Option{
				asynq.Queue("critical"),
				asynq.MaxRetry(0),
				asynq.Timeout(rollTimeout),
				asynq.Unique(9 * time.Minute),
			},
		},
		{
			// 每天一次的任务各占一分钟，占用情况见 .claude/rules/panel-app.md 的「每日任务分钟表」。
			Cronspec: "50 3 * * *",
			Type:     TypeRetentionFetchLog,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(retentionTimeout),
				asynq.Unique(23 * time.Hour),
			},
		},
	}
}

func (m *Module) expire(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, expireTimeout)
	defer cancel()
	stats, err := m.svc.ExpireDue(ctx)
	if stats.Expired > 0 || stats.Closed > 0 {
		plog.From(ctx).Info("subscription expire done",
			zap.Int("expired", stats.Expired),
			zap.Int("renewal_closed", stats.Closed),
			zap.Int("revoked_credentials", stats.RevokedCredentials))
	}
	if err != nil {
		return fmt.Errorf("订阅到期扫描: %w", err)
	}
	return nil
}

func (m *Module) rollQuota(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, rollTimeout)
	defer cancel()
	stats, err := m.svc.RollQuotas(ctx)
	if err != nil {
		return fmt.Errorf("配额周期滚动: %w", err)
	}
	if stats.Rolled > 0 || stats.Cycle > 0 {
		plog.From(ctx).Info("subscription quota roll done",
			zap.Int("rolled", stats.Rolled),
			zap.Int("cycle", stats.Cycle),
			zap.Int("passes", stats.Passes))
	}
	return nil
}

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	ctx, cancel := context.WithTimeout(ctx, retentionTimeout)
	defer cancel()
	var total int64
	var batches int
	for i := 0; i < maxPurgeBatches; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := m.svc.PurgeFetchLogs(ctx)
		if err != nil {
			return fmt.Errorf("拉取日志保留期清理: %w", err)
		}
		batches++
		total += n
		if n == 0 {
			break
		}
	}
	plog.From(ctx).Info("subscription fetch log retention done", zap.Int64("deleted", total), zap.Int("batches", batches))
	return nil
}
