// Package task 是 node 模块的 asynq 任务：节点数据的保留期清理、strict 设备模式的重新判定。
package task

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TypeRetention 是节点数据保留期清理任务类型。
const TypeRetention = "retention:node"

// TypeDevicesStrict 是 strict 设备模式的重新判定任务类型。
const TypeDevicesStrict = "devices:strict"

const (
	purgeBatch = 5000
	maxBatches = 200
)

// Service 是任务用到的 node service 方法。
type Service interface {
	PurgeRetention(ctx context.Context, table string, now time.Time, batch int) (int64, error)
	// RecheckStrictDevices：strict 模式下重算超限订阅集合，变了推进名单纪元；loose 时什么都不做。
	RecheckStrictDevices(ctx context.Context) error
}

// Module 注册 node 相关的 asynq 任务与定时调度。
type Module struct {
	svc Service
	now func() time.Time
}

// New 构造 node 任务模块；时钟默认为 time.Now。
func New(svc Service) *Module {
	return &Module{svc: svc, now: time.Now}
}

// Register 把任务处理函数挂到 mux。
func (m *Module) Register(mux *asynq.ServeMux) {
	mux.HandleFunc(TypeRetention, m.retention)
	mux.HandleFunc(TypeDevicesStrict, m.devicesStrict)
}

// Periodic 声明节点数据保留期清理（每天凌晨）与 strict 设备判定（每分钟）。
func (m *Module) Periodic() []jobs.Periodic {
	return []jobs.Periodic{
		{
			Cronspec: "44 3 * * *",
			Type:     TypeRetention,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(10 * time.Minute),
				asynq.Unique(23 * time.Hour),
			},
		},
		{
			Cronspec: "@every 1m",
			Type:     TypeDevicesStrict,
			Opts: []asynq.Option{
				asynq.Queue("low"),
				asynq.MaxRetry(0),
				asynq.Timeout(30 * time.Second),
				asynq.Unique(50 * time.Second),
			},
		},
	}
}

func (m *Module) retention(ctx context.Context, _ *asynq.Task) error {
	now := m.now()
	perTable := make(map[string]int64, len(service.RetentionTables))
	var total int64

	for _, table := range service.RetentionTables {
		var tableDeleted int64
		for batch := 0; batch < maxBatches; batch++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := m.svc.PurgeRetention(ctx, table, now, purgeBatch)
			if err != nil {
				return fmt.Errorf("节点数据保留期清理 %s: %w", table, err)
			}
			tableDeleted += n
			if n < int64(purgeBatch) {
				break
			}
		}
		perTable[table] = tableDeleted
		total += tableDeleted
	}

	fields := make([]zap.Field, 0, len(service.RetentionTables)+1)
	fields = append(fields, zap.Int64("total", total))
	for _, table := range service.RetentionTables {
		fields = append(fields, zap.Int64(table, perTable[table]))
	}
	plog.From(ctx).Info("node retention done", fields...)
	return nil
}

// devicesStrict 调 service 重新判定；出错原样返回（MaxRetry 0，下一分钟再算）。
func (m *Module) devicesStrict(ctx context.Context, _ *asynq.Task) error {
	if err := m.svc.RecheckStrictDevices(ctx); err != nil {
		return fmt.Errorf("strict 设备判定: %w", err)
	}
	return nil
}

var _ jobs.Module = (*Module)(nil)
