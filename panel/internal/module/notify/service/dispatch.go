package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

// Dispatch 认领到期的 queued 邮件投递并逐封发送，返回处理条数。notify.email 开关关闭时不认领：
// 记录留在队列里，开关恢复后按原顺序发送。
func (s *Service) Dispatch(ctx context.Context) (int, error) {
	on, err := s.settings.SwitchEnabled(ctx, settingssvc.SwitchNotifyEmail)
	if err != nil {
		return 0, err
	}
	if !on {
		return 0, nil
	}

	cfg, configured, err := s.smtpConfig(ctx)
	if err != nil {
		return 0, err
	}

	var total int
	for batch := 0; batch < dispatchMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			break
		}
		rows, err := s.store.ClaimDeliveries(ctx, ChannelEmail, dispatchBatch, dispatchLease)
		if err != nil {
			return total, err
		}
		for _, d := range rows {
			if err := ctx.Err(); err != nil {
				return total, nil
			}
			s.deliver(ctx, configured, cfg, d)
			total++
		}
		if len(rows) < dispatchBatch {
			break
		}
	}
	return total, nil
}

// deliver 发送一条已认领的投递并写回状态；问题只记日志（不含收件地址）。
func (s *Service) deliver(ctx context.Context, configured bool, cfg SMTPConfig, d model.Delivery) {
	lg := plog.From(ctx).With(zap.String("delivery_id", d.ID.String()), zap.String("template", d.TemplateCode))

	finish := func(status, msg string, count bool) {
		if _, err := s.store.MarkFinal(ctx, d.ID, status, msg, count); err != nil {
			lg.Error("notify: 标记投递终态失败", zap.Error(err))
		}
	}

	if !configured {
		finish(model.StatusSuppressed, "渠道未配置", false)
		return
	}

	var p deliveryPayload
	raw, err := s.sealer.Open(d.PayloadEnc, deliveryAAD(d.ID))
	if err != nil {
		lg.Error("notify: 解封投递内容失败", zap.Error(err))
		finish(model.StatusFailed, "投递内容无法解开", true)
		return
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		lg.Error("notify: 解析投递内容失败", zap.Error(err))
		finish(model.StatusFailed, "投递内容无法解开", true)
		return
	}

	def, ok := lookupDef(d.TemplateCode, d.Channel)
	if !ok {
		finish(model.StatusSuppressed, "模板未登记", false)
		return
	}

	t, ok, err := s.store.GetTemplate(ctx, d.TemplateCode, d.Channel)
	if err != nil {
		lg.Warn("notify: 读取模板失败，租约到期后重试", zap.Error(err))
		return
	}
	if !ok {
		finish(model.StatusSuppressed, "模板缺失", false)
		return
	}

	subject, err := renderText(t.Subject, def.Allowed, p.Vars)
	if err != nil {
		finish(model.StatusFailed, "模板渲染失败", true)
		return
	}
	body, err := renderText(t.Body, def.Allowed, p.Vars)
	if err != nil {
		finish(model.StatusFailed, "模板渲染失败", true)
		return
	}

	sendErr := s.sender.Send(ctx, cfg, Message{To: p.To, Subject: headerSafe(subject), Body: body})
	if sendErr == nil {
		if _, err := s.store.MarkSent(ctx, d.ID); err != nil {
			lg.Error("notify: 标记已发送失败", zap.Error(err))
		}
		return
	}
	if ctx.Err() != nil {
		lg.Warn("notify: 发送被中断，租约到期后重发")
		return
	}

	msg, permanent := "发送失败", false
	var se *SendError
	if errors.As(sendErr, &se) {
		msg, permanent = se.Error(), se.Permanent
	}
	lg.Warn("notify: 发送失败",
		zap.String("reason", msg),
		zap.Int("attempt", d.Attempts+1),
	)

	if permanent || d.Attempts+1 >= d.MaxAttempts {
		finish(model.StatusFailed, msg, true)
		return
	}
	if _, err := s.store.MarkRetry(ctx, d.ID, backoff(d.Attempts+1), msg); err != nil {
		lg.Error("notify: 标记重试失败", zap.Error(err))
	}
}

// backoff 第 n 次失败后的等待：1、2、4、8… 分钟，封顶 32 分钟。
func backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 6 {
		n = 6
	}
	return time.Duration(1<<(n-1)) * time.Minute
}

// PurgeDeliveries 删除早于 before 的投递记录。
func (s *Service) PurgeDeliveries(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.store.PurgeDeliveries(ctx, before, batch)
}
