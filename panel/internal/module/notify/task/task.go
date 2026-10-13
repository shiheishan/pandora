package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是保留期清理任务类型：投递记录、站内信、去重键、过期绑定码。
const TypeRetention = "retention:notify"

// TypePublishAnnouncements 是定时公告到点发布任务类型。
const TypePublishAnnouncements = "notify:publish_announcements"

const (
	purgeBatch = 5000
	maxBatches = 200

	// bindCodeGrace 是绑定码过期后再留多久才删（排查「为什么绑不上」用）
	bindCodeGrace = 24 * time.Hour
	// publishTimeout 是定时公告发布任务的超时
	publishTimeout = 20 * time.Second
)

// Service 是任务用到的 notify service 方法。
type Service interface {
	Dispatch(ctx context.Context) (int, error)
	Scan(ctx context.Context) (int, error)
	PublishDueAnnouncements(ctx context.Context) (int, error)
	PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error)
	PurgeInbox(ctx context.Context, before time.Time, batch int) (int, error)
	PurgeEvents(ctx context.Context, before time.Time, batch int) (int, error)
	PurgeBindCodes(ctx context.Context, before time.Time, batch int) (int, error)
}

// Module 注册 notify 相关的 asynq 任务与定时调度。
type Module struct {
	svc Service
	now func() time.Time
}

// New 构造 notify 任务模块；时钟默认为 time.Now。
func New(svc Service) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(service.TaskDispatch, m.dispatch)
	mux.HandleFunc(service.TaskScan, m.scan)
	mux.HandleFunc(TypePublishAnnouncements, m.publish)
	mux.HandleFunc(TypeRetention, m.retention)
}

// Periodic 声明派发兜底、到期与流量扫描、定时公告发布与保留期清理。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "@every 30s",
			Type:     service.TaskDispatch,
			Opts: []asynq.Option{
				asynq.Queue("default"),
				asynq.MaxRetry(0),
				asynq.Timeout(service.DispatchTimeout),
				asynq.Unique(service.DispatchTimeout),
			},
		},
		{
			Cronspec: "@every 5m",
			Type:     service.TaskScan,
			Opts: []asynq.Option{
				asynq.Queue("default"),
				asynq.MaxRetry(0),
				asynq.Timeout(service.ScanTimeout),
				asynq.Unique(4 * time.Minute),
			},
		},
		{
			Cronspec: "@every 1m",
			Type:     TypePublishAnnouncements,
			Opts: []asynq.Option{
				asynq.Queue("default"),
				asynq.MaxRetry(0),
				asynq.Timeout(publishTimeout),
				asynq.Unique(50 * time.Second),
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

func (m *Module) dispatch(ctx context.Context, _ *asynq.Task) error {
	n, err := m.svc.Dispatch(ctx)
	if err != nil {
		return fmt.Errorf("通知派发: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("notify dispatch done", zap.Int("processed", n))
	}
	return nil
}

func (m *Module) scan(ctx context.Context, _ *asynq.Task) error {
	n, err := m.svc.Scan(ctx)
	if err != nil {
		return fmt.Errorf("通知扫描: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("notify scan done", zap.Int("written", n))
	}
	return nil
}

func (m *Module) publish(ctx context.Context, _ *asynq.Task) error {
	n, err := m.svc.PublishDueAnnouncements(ctx)
	if err != nil {
		return fmt.Errorf("定时公告发布: %w", err)
	}
	if n > 0 {
		plog.From(ctx).Info("notify announcements published", zap.Int("count", n))
	}
	return nil
}

// retention 依次清理四类过期数据；一类出错不挡其余几类，最后合并返回错误。
func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	now := m.now()
	jobsList := []struct {
		name   string
		before time.Time
		purge  func(ctx context.Context, before time.Time, batch int) (int, error)
	}{
		{"deliveries", now.AddDate(0, 0, -service.RetentionDays), m.svc.PurgeDeliveries},
		{"inbox", now.AddDate(0, 0, -service.InboxRetentionDays), m.svc.PurgeInbox},
		{"events", now.AddDate(0, 0, -service.EventRetentionDays), m.svc.PurgeEvents},
		{"bind_codes", now.Add(-bindCodeGrace), m.svc.PurgeBindCodes},
	}
	var errList []error
	for _, j := range jobsList {
		total, batches, err := purgeLoop(ctx, j.before, j.purge)
		if err != nil {
			errList = append(errList, fmt.Errorf("通知保留期清理 %s: %w", j.name, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		plog.From(ctx).Info("notify retention done", zap.String("kind", j.name), zap.Int("deleted", total), zap.Int("batches", batches))
	}
	return errors.Join(errList...)
}

// purgeLoop 分批调用 purge 直到某批不满或到批数上限。
func purgeLoop(ctx context.Context, before time.Time, purge func(ctx context.Context, before time.Time, batch int) (int, error)) (total, batches int, err error) {
	for batches = 0; batches < maxBatches; batches++ {
		if err := ctx.Err(); err != nil {
			return total, batches, err
		}
		n, err := purge(ctx, before, purgeBatch)
		if err != nil {
			return total, batches, err
		}
		total += n
		if n < purgeBatch {
			return total, batches + 1, nil
		}
	}
	return total, batches, nil
}

var _ jobs.Module = (*Module)(nil)
