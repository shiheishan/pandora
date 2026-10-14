package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

// Dispatch 认领到期的 queued 投递并发送：先邮件、再 Telegram，返回两者处理条数之和。
// 一个渠道出错不挡另一个渠道，最后返回第一个错误。
func (s *Service) Dispatch(ctx context.Context) (int, error) {
	nEmail, errEmail := s.dispatchEmail(ctx)
	nTG, errTG := s.dispatchTelegram(ctx)
	total := nEmail + nTG
	if errEmail != nil {
		return total, errEmail
	}
	return total, errTG
}

// dispatchTelegram 认领到期的 queued Telegram 投递并逐条发送，返回处理条数。
func (s *Service) dispatchTelegram(ctx context.Context) (int, error) {
	cfg, ready, err := s.telegramReady(ctx)
	if err != nil {
		return 0, err
	}
	if n, err := s.store.FailExhausted(ctx, ChannelTelegram); err != nil {
		return 0, err
	} else if n > 0 {
		plog.From(ctx).Warn("notify: 尝试用尽的投递置为失败", zap.Int("count", n))
	}

	var total int
	for batch := 0; batch < dispatchMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			break
		}
		rows, err := s.store.ClaimDeliveries(ctx, ChannelTelegram, dispatchBatch, dispatchLease)
		if err != nil {
			return total, err
		}
		for _, d := range rows {
			if err := ctx.Err(); err != nil {
				return total, nil
			}
			s.deliverTelegram(ctx, ready, cfg, d)
			total++
		}
		if len(rows) < dispatchBatch {
			break
		}
	}
	return total, nil
}

// deliverTelegram 发送一条已认领的 Telegram 投递并写回状态；问题只记日志（不含 chat_id）。
func (s *Service) deliverTelegram(ctx context.Context, ready bool, cfg settingssvc.Telegram, d model.Delivery) {
	lg := plog.From(ctx).With(zap.String("delivery_id", d.ID.String()), zap.String("template", d.TemplateCode))
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	finish := func(status, msg string) {
		if _, err := s.store.MarkFinal(wctx, d.ID, status, msg); err != nil {
			lg.Error("notify: 标记投递终态失败", zap.Error(err))
		}
	}

	if !ready {
		finish(model.StatusSuppressed, "渠道未配置")
		return
	}

	var p deliveryPayload
	raw, err := s.sealer.Open(d.PayloadEnc, deliveryAAD(d.ID))
	if err != nil {
		lg.Error("notify: 解封投递内容失败", zap.Error(err))
		finish(model.StatusFailed, "投递内容无法解开")
		return
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		lg.Error("notify: 解析投递内容失败", zap.Error(err))
		finish(model.StatusFailed, "投递内容无法解开")
		return
	}

	chatID, err := strconv.ParseInt(strings.TrimSpace(p.To), 10, 64)
	if err != nil || chatID == 0 {
		finish(model.StatusFailed, "chat_id 无效")
		return
	}

	def, ok := lookupDef(d.TemplateCode, d.Channel)
	if !ok {
		finish(model.StatusSuppressed, "模板未登记")
		return
	}

	t, ok, err := s.store.GetTemplate(ctx, d.TemplateCode, d.Channel)
	if err != nil {
		lg.Warn("notify: 读取模板失败，租约到期后重试", zap.Error(err))
		return
	}
	if !ok {
		finish(model.StatusSuppressed, "模板缺失")
		return
	}

	subject, err := renderText(t.Subject, def.Allowed, p.Vars)
	if err != nil {
		finish(model.StatusFailed, "模板渲染失败")
		return
	}
	body, err := renderText(t.Body, def.Allowed, p.Vars)
	if err != nil {
		finish(model.StatusFailed, "模板渲染失败")
		return
	}

	text := body
	if subject != "" {
		text = subject + "\n\n" + body
	}

	_, sendErr := s.tg.SendMessage(ctx, cfg.BotToken, chatID, text)
	if sendErr == nil {
		if _, err := s.store.MarkSent(wctx, d.ID); err != nil {
			lg.Error("notify: 标记已发送失败", zap.Error(err))
		}
		return
	}
	if ctx.Err() != nil {
		lg.Warn("notify: 发送被中断，租约到期后重发")
		return
	}

	msg, permanent := "发送失败", false
	var te *TelegramError
	if errors.As(sendErr, &te) {
		msg, permanent = te.Error(), te.Permanent
	}
	lg.Warn("notify: 发送失败",
		zap.String("reason", msg),
		zap.Int("attempt", d.Attempts),
	)

	if permanent || d.Attempts >= d.MaxAttempts {
		finish(model.StatusFailed, msg)
		return
	}
	if _, err := s.store.MarkRetry(wctx, d.ID, backoff(d.Attempts), msg); err != nil {
		lg.Error("notify: 标记重试失败", zap.Error(err))
	}
}
