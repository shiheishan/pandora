// Package task 是 identity 模块的 asynq 任务：注册会话与验证码的保留期清理。
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

// TypeRetention 是注册会话与验证码的保留期清理任务。
const TypeRetention = "retention:identity"

const (
	RetentionDays = 7    // 产品定的保留期：注册会话和验证码 7 天
	purgeBatch    = 5000 // 每批删除行数
	maxBatches    = 200  // 单次任务每张表最多批数，删不完留给明天
)

// Purger 是任务用到的 service 方法。
type Purger interface {
	PurgeRegistrations(ctx context.Context, before time.Time, batch int) (int, error)
	PurgeCodes(ctx context.Context, before time.Time, batch int) (int, error)
}

// Module 注册 identity 的 asynq 任务与定时调度。
type Module struct {
	svc Purger
	now func() time.Time
}

// New 构造任务模块；时钟缺省 time.Now。
func New(svc Purger) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明每天一次的保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
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
	plog.From(ctx).Info("identity retention done",
		zap.Int("registration_sessions", regs), zap.Int("verification_codes", codes))
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
