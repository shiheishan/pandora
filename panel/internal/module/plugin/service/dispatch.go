package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/plugin/model"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// Dispatch 认领到期的投递并逐条发送，返回处理条数：先把尝试已用尽、租约已过仍为 queued 的行置为 failed，
// 再一批批认领（每批 dispatchBatch，至多 dispatchMaxBatches 批），直到队列空、批数用完或 ctx 结束。
// 同一批里按顺序逐条发（并发上限 1，与旧代码一致）。认领带租约：别的实例在租约内看不到这些行。
func (s *Service) Dispatch(ctx context.Context) (int, error) {
	n, err := s.store.FailExhausted(ctx)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		plog.From(ctx).Warn("plugin: 尝试用尽的投递置为失败", zap.Int("count", n))
	}

	var total int
	for batch := 0; batch < dispatchMaxBatches; batch++ {
		if ctx.Err() != nil {
			break
		}
		rows, err := s.store.ClaimDue(ctx, dispatchBatch, dispatchLease)
		if err != nil {
			return total, err
		}
		if len(rows) == 0 {
			break
		}

		ids := make([]uuid.UUID, 0, len(rows))
		seen := make(map[uuid.UUID]bool, len(rows))
		for _, d := range rows {
			if !seen[d.HookID] {
				seen[d.HookID] = true
				ids = append(ids, d.HookID)
			}
		}
		hooks, err := s.store.HooksByIDs(ctx, ids)
		if err != nil {
			return total, err
		}
		hookMap := make(map[uuid.UUID]model.Hook, len(hooks))
		for _, h := range hooks {
			hookMap[h.ID] = h
		}

		for _, d := range rows {
			if ctx.Err() != nil {
				return total, nil
			}
			h, ok := hookMap[d.HookID]
			if !ok {
				continue
			}
			s.deliver(ctx, h, d)
			total++
		}
		if len(rows) < dispatchBatch {
			break
		}
	}
	return total, nil
}

// deliver 发送一条已认领的投递并写回状态；问题只记日志（不含密钥与完整地址）。
func (s *Service) deliver(ctx context.Context, h model.Hook, d model.Delivery) {
	lg := plog.From(ctx).With(
		zap.String("delivery_id", d.ID.String()),
		zap.String("hook", h.Code),
		zap.String("event", d.Event),
	)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	secret, err := s.sealer.Open(h.SecretEnc, secretAAD(h.Code))
	if err != nil {
		if _, werr := s.store.MarkFailed(wctx, d.ID, nil, "签名密钥无法解开", nil); werr != nil {
			lg.Error("plugin: 标记投递失败失败", zap.Error(werr))
		}
		lg.Error("plugin: 签名密钥无法解开", zap.Error(err))
		return
	}

	var code int
	var dur *int
	var sendErr error

	if _, err := parseEndpoint(h.EndpointURL, s.opts.AllowPrivate); err != nil {
		code = 0
		dur = nil
		sendErr = fmt.Errorf("地址不可用：%w", err)
	} else {
		code, dur, sendErr = postSigned(ctx, s.client, h.EndpointURL, d.Event, d.ID.String(),
			[]byte(d.Payload), string(secret), time.Duration(h.TimeoutMS)*time.Millisecond, s.now())
	}

	if sendErr == nil && code >= 200 && code < 300 {
		if _, werr := s.store.MarkSent(wctx, d.ID, code, dur); werr != nil {
			lg.Error("plugin: 标记已送达失败", zap.Error(werr))
		}
		return
	}

	if sendErr != nil && ctx.Err() != nil {
		lg.Warn("plugin: 发送被中断，租约到期后重发")
		return
	}

	msg := ""
	if sendErr != nil {
		msg = sendErr.Error()
	} else {
		msg = fmt.Sprintf("对方返回 HTTP %d", code)
	}

	var codePtr *int
	if code != 0 {
		codePtr = &code
	}

	terminal := d.Attempts >= d.MaxAttempts || (code >= 400 && code < 500)
	if terminal {
		if _, werr := s.store.MarkFailed(wctx, d.ID, codePtr, msg, dur); werr != nil {
			lg.Error("plugin: 标记投递失败失败", zap.Error(werr))
		}
	} else {
		if _, werr := s.store.MarkRetry(wctx, d.ID, backoff(d.Attempts), codePtr, msg, dur); werr != nil {
			lg.Error("plugin: 标记重试失败", zap.Error(werr))
		}
	}

	lg.Warn("plugin: 发送失败",
		zap.String("reason", msg),
		zap.Int("attempt", d.Attempts),
	)
}

// backoff 第 n 次失败后的等待：1、2、4、8、16 分钟，封顶 30 分钟（对方在重启的话，密集重试帮不上忙）。
func backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 6 {
		return 30 * time.Minute
	}
	return min(time.Duration(1<<(n-1))*time.Minute, 30*time.Minute)
}

// PurgeDeliveries 删除早于 before 的投递记录（每次至多 batch 行），返回删除行数。
func (s *Service) PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.store.PurgeDeliveries(ctx, before, batch)
}
