// Package jobs 是 asynq 任务的统一约定：载荷格式、req_id 传递、日志中间件与模块注册。
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// envelope 是所有任务载荷的外层：{"req_id": "...", "data": <业务载荷>}；定时任务不带 req_id（见 newPeriodicTask）。
type envelope struct {
	ReqID string          `json:"req_id,omitempty"`
	Data  json.RawMessage `json:"data"`
}

// NewTask 建任务，req_id 取自 ctx（没有就新生成）。
func NewTask(ctx context.Context, typ string, data any, opts ...asynq.Option) (*asynq.Task, error) {
	id := reqid.From(ctx)
	if id == "" {
		id = reqid.New()
	}
	raw := json.RawMessage("{}")
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("jobs: 编码 %s 载荷: %w", typ, err)
		}
		raw = b
	}
	payload, err := json.Marshal(envelope{ReqID: id, Data: raw})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(typ, payload, opts...), nil
}

// Decode 把载荷里的 data 解进 dst。
func Decode(t *asynq.Task, dst any) error {
	var env envelope
	if err := json.Unmarshal(t.Payload(), &env); err != nil {
		return fmt.Errorf("jobs: 解析 %s 载荷: %w", t.Type(), err)
	}
	if len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return fmt.Errorf("jobs: 解析 %s 数据: %w", t.Type(), err)
	}
	return nil
}

// Middleware 把载荷里的 req_id 与 asynq 任务 id 放进 ctx 和日志，任务结束记一行结果。
func Middleware(base *zap.Logger) asynq.MiddlewareFunc {
	return func(next asynq.Handler) asynq.Handler {
		return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
			// 载荷没带 req_id（定时任务）或解析失败时为空，Normalize 生成新的。
			var env envelope
			_ = json.Unmarshal(t.Payload(), &env)
			ctx = plog.WithReqID(ctx, base, reqid.Normalize(env.ReqID))
			fields := []zap.Field{zap.String("task", t.Type())}
			// asynq 处理任务时 ctx 里带任务 id，日志据此对上 asynq 里的那条任务（重试、归档）。
			if id, ok := asynq.GetTaskID(ctx); ok {
				fields = append(fields, zap.String("task_id", id))
			}
			lg := plog.From(ctx).With(fields...)
			ctx = plog.With(ctx, lg)

			start := time.Now()
			err := next.ProcessTask(ctx, t)
			dur := zap.Int64("dur_ms", time.Since(start).Milliseconds())
			if err != nil {
				lg.Error("task failed", dur, zap.Error(err))
			} else {
				lg.Info("task done", dur)
			}
			return err
		})
	}
}

// newPeriodicTask 构造定时任务，载荷固定为 {"data":{}}。
// 定时任务的载荷不带 req_id，由 Middleware 每次运行时生成新的；
// 载荷固定不变，asynq.Unique（按载荷算键）在不同 Scheduler 实例与重启前后都能去重。
func newPeriodicTask(typ string) *asynq.Task {
	payload, _ := json.Marshal(envelope{Data: json.RawMessage("{}")})
	return asynq.NewTask(typ, payload)
}

// Periodic 是模块声明的定时任务。
type Periodic struct {
	Cronspec string
	Type     string
	Opts     []asynq.Option
}

// Module 由每个业务模块的 task 包实现，worker 统一注册。
type Module interface {
	Register(mux *asynq.ServeMux)
	Periodic() []Periodic
}

// Register 注册各模块的处理函数与定时任务。
func Register(mux *asynq.ServeMux, sched *asynq.Scheduler, mods ...Module) error {
	for _, m := range mods {
		m.Register(mux)
		for _, p := range m.Periodic() {
			task := newPeriodicTask(p.Type)
			if _, err := sched.Register(p.Cronspec, task, p.Opts...); err != nil {
				return fmt.Errorf("jobs: 注册定时任务 %s: %w", p.Type, err)
			}
		}
	}
	return nil
}

// ZapLogger 让 asynq 的 Server 与 Scheduler 用 zap 写日志。
type ZapLogger struct{ *zap.SugaredLogger }

func (l ZapLogger) Debug(args ...any) { l.SugaredLogger.Debug(args...) }
func (l ZapLogger) Info(args ...any)  { l.SugaredLogger.Info(args...) }
func (l ZapLogger) Warn(args ...any)  { l.SugaredLogger.Warn(args...) }
func (l ZapLogger) Error(args ...any) { l.SugaredLogger.Error(args...) }

// Fatal 只记 Error 不退出：进程是否退出由我们自己的生命周期决定，不让库直接 os.Exit 跳过收尾。
func (l ZapLogger) Fatal(args ...any) { l.SugaredLogger.Error(args...) }
