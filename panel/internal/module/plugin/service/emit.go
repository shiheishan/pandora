package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// Emit 把一个事件排进所有启用且订阅了它的钩子的队列：只写一条 INSERT，不在业务事务里发网络请求。
// ctx 带事务时就在调用方事务里写（事件与业务变更同生共死：订单回滚了却通知了插件，插件那边就多出一笔不存在的订单）；
// 同一钩子同一 dedupeKey 只投一次。写入了新行时登记提交后入队一次 plugin:dispatch（同一事务多次 Emit 只入队一次）。
// 出错时调用方应让业务事务失败。
func (s *Service) Emit(ctx context.Context, event, dedupeKey string, payload any) error {
	if !KnownEvent(event) {
		return errs.Wrap(errs.Internal, fmt.Errorf("plugin: 未登记的事件 %q", event))
	}
	if dedupeKey == "" || len(dedupeKey) > 200 {
		shown := dedupeKey
		if len(shown) > 40 {
			shown = shown[:40]
		}
		return errs.Wrap(errs.Internal, fmt.Errorf("plugin: 去重键不合法 %q", shown))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		return errs.Wrap(errs.Internal, fmt.Errorf("plugin: payload 必须是 JSON 对象"))
	}
	n, err := s.store.EnqueueEvent(ctx, event, dedupeKey, string(raw))
	if err != nil {
		return err
	}
	if n > 0 {
		tx.AfterCommit(ctx, TaskDispatch, s.kick)
	}
	return nil
}

// kick 入队一次 plugin:dispatch（TaskID 按秒分桶去重）；失败只记日志，周期任务会补发。
func (s *Service) kick(ctx context.Context) {
	if s.queue == nil {
		return
	}
	task, err := jobs.NewTask(ctx, TaskDispatch, nil)
	if err != nil {
		plog.From(ctx).Warn("plugin: 入队投递任务失败，等周期任务补发", zap.Error(err))
		return
	}
	_, err = s.queue.EnqueueContext(ctx, task,
		asynq.TaskID(fmt.Sprintf("%s:%d", TaskDispatch, s.now().Unix())),
		asynq.Queue("low"),
		asynq.MaxRetry(0),
		asynq.Timeout(DispatchTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return
		}
		plog.From(ctx).Warn("plugin: 入队投递任务失败，等周期任务补发", zap.Error(err))
	}
}
