// Package task 是 identity 模块的 asynq 任务：注册会话、验证码与批量生成任务行的保留期清理，
// 批量生成账号（一次性）与它的每分钟补排。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是注册会话与验证码的保留期清理任务。
const TypeRetention = "retention:identity"

const (
	RetentionDays = 7    // 产品定的保留期：注册会话和验证码 7 天
	purgeBatch    = 5000 // 每批删除行数
	maxBatches    = 200  // 单次任务每张表最多批数，删不完留给明天
)

// Service 是任务用到的 service 方法；*service.Service 实现。
type Service interface {
	PurgeRegistrations(ctx context.Context, before time.Time, batch int) (int, error)
	PurgeCodes(ctx context.Context, before time.Time, batch int) (int, error)
	PurgeGenerationJobs(ctx context.Context, before time.Time, batch int) (int, error)
	RunGenerationJob(ctx context.Context, id uuid.UUID) error
	ReconcileGeneration(ctx context.Context) (requeued int, purged int64, err error)
}

var _ Service = (*service.Service)(nil)

// Module 注册 identity 的 asynq 任务与定时调度。
type Module struct {
	svc Service
	now func() time.Time
}

// New 构造任务模块；时钟缺省 time.Now。
func New(svc Service) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeRetention, m.retention)
	mux.HandleFunc(service.TaskGenerateUsers, m.generate)
	mux.HandleFunc(service.TaskReconcileGeneration, m.reconcile)
}

// Periodic 声明每天一次的保留期清理与每分钟一次的批量生成补排。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 1m",
			Type:     service.TaskReconcileGeneration,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(30 * time.Second),
				asynq.Unique(50 * time.Second),
			},
		},
		{
			Cronspec: "47 3 * * *",
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

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	before := m.now().AddDate(0, 0, -RetentionDays)
	regs, err := purgeAll(ctx, before, m.svc.PurgeRegistrations)
	if err != nil {
		return fmt.Errorf("注册会话保留期清理: %w", err)
	}
	codes, err := purgeAll(ctx, before, m.svc.PurgeCodes)
	if err != nil {
		return fmt.Errorf("验证码保留期清理: %w", err)
	}
	jobsBefore := m.now().AddDate(0, 0, -service.GenerationRetentionDays)
	genJobs, err := purgeAll(ctx, jobsBefore, m.svc.PurgeGenerationJobs)
	if err != nil {
		return fmt.Errorf("批量生成任务保留期清理: %w", err)
	}
	plog.From(ctx).Info("identity retention done",
		zap.Int("registration_sessions", regs), zap.Int("verification_codes", codes),
		zap.Int("generation_jobs", genJobs))
	return nil
}

// generate 执行一个批量生成任务。除载荷解不开外一律回 nil：MaxRetry 0 的任务失败会被 asynq 归档；
// 进度、失败与租约都记在任务行里，中断的由 reconcile:user_generation 按新代数重新入队。
func (m *Module) generate(ctx context.Context, t *asynq.Task) error {
	var p service.GenerationPayload
	if err := jobs.Decode(t, &p); err != nil {
		return fmt.Errorf("identity:generate_users 载荷: %w", err)
	}
	if err := m.svc.RunGenerationJob(ctx, p.JobID); err != nil {
		plog.From(ctx).Error("批量生成账号出错", zap.String("job", p.JobID.String()), zap.Error(err))
	}
	return nil
}

// reconcile 清过期结果密文，并把排队太久或租约过期的生成任务重新入队。
func (m *Module) reconcile(ctx context.Context, _ *asynq.Task) error {
	requeued, purged, err := m.svc.ReconcileGeneration(ctx)
	if err != nil {
		return fmt.Errorf("批量生成补排: %w", err)
	}
	if requeued > 0 || purged > 0 {
		plog.From(ctx).Info("批量生成补排", zap.Int("requeued", requeued), zap.Int64("purged_results", purged))
	}
	return nil
}

// purgeAll 分批调用 purge，直到一批不满或到批数上限。
func purgeAll(ctx context.Context, before time.Time, purge func(context.Context, time.Time, int) (int, error)) (int, error) {
	total := 0
	for i := 0; i < maxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := purge(ctx, before, purgeBatch)
		if err != nil {
			return total, err
		}
		total += n
		if n < purgeBatch {
			break
		}
	}
	return total, nil
}

var _ jobs.Module = (*Module)(nil)
