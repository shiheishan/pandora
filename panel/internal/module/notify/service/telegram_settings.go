package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// TelegramSettings 读后台 Telegram 设置（Token 只给是否已设置）。
func (s *Service) TelegramSettings(ctx context.Context) (TelegramSettingsView, error) {
	t, err := s.settings.Telegram(ctx)
	if err != nil {
		return TelegramSettingsView{}, err
	}
	return TelegramSettingsView{
		Enabled:     t.Enabled,
		BotUsername: t.BotUsername,
		HasToken:    t.HasToken,
		AdminChatID: t.AdminChatID,
	}, nil
}

// SaveTelegramSettings 保存后台 Telegram 设置。启用且有 Token、面板对外地址是 https 时，先向 Telegram 注册 webhook（每次换一个新 secret），成功后再落库。
func (s *Service) SaveTelegramSettings(ctx context.Context, in TelegramSettingsInput) error {
	in.BotUsername = strings.TrimPrefix(strings.TrimSpace(in.BotUsername), "@")
	in.BotToken = strings.TrimSpace(in.BotToken)

	fields := map[string]string{}
	if strings.ContainsRune(in.BotUsername, 0) {
		fields["bot_username"] = "不能包含空字符"
	}
	if strings.ContainsRune(in.BotToken, 0) {
		fields["bot_token"] = "不能包含空字符"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	cur, err := s.settings.Telegram(ctx)
	if err != nil {
		return err
	}
	if in.Enabled && in.BotToken == "" && !cur.HasToken {
		return errs.Invalid(map[string]string{"bot_token": "启用前要填 Bot Token"})
	}

	update := settingssvc.TelegramUpdate{
		Enabled:      in.Enabled,
		BotUsername:  in.BotUsername,
		AdminChatSet: in.AdminChatSet,
		AdminChatID:  in.AdminChatID,
	}
	if in.BotToken != "" {
		token := in.BotToken
		update.BotToken = &token
	}
	// 先把字段全部校验完，再向 Telegram 注册 webhook：否则 Telegram 已换成新 secret、落库却被拒，绑定会静默失效
	if err := settingssvc.ValidateTelegram(&update); err != nil {
		return err
	}
	effectiveToken := cur.BotToken
	if update.BotToken != nil {
		effectiveToken = *update.BotToken
	}

	if in.Enabled && effectiveToken != "" && strings.HasPrefix(s.publicBaseURL, "https://") {
		secret, err := newWebhookSecret()
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		url := strings.TrimRight(s.publicBaseURL, "/") + "/v1/webhooks/telegram"
		if err := s.tg.SetWebhook(ctx, effectiveToken, url, secret); err != nil {
			var te *TelegramError
			if errors.As(err, &te) {
				return errs.Newf(errs.ValidationFailed, "设置 Telegram webhook 失败："+te.Detail)
			}
			return errs.Wrap(errs.Internal, err)
		}
		update.WebhookSecret = &secret
	} else if in.Enabled && effectiveToken != "" {
		plog.From(ctx).Warn("notify: 面板对外地址不是 https，未注册 Telegram webhook，用户无法完成绑定")
	} else if !in.Enabled {
		// 关闭时换一个不注册给 Telegram 的随机 secret，旧 webhook 带来的请求一律 401
		secret, err := newWebhookSecret()
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		update.WebhookSecret = &secret
	}

	return s.settings.SetTelegram(ctx, update)
}

// TestTelegram 给 chatID 发一条测试消息；chatID 为 0 时发到管理员群组。先留痕再发。
func (s *Service) TestTelegram(ctx context.Context, chatID int64) error {
	if chatID == 0 {
		cfg, err := s.settings.Telegram(ctx)
		if err != nil {
			return err
		}
		if cfg.AdminChatID == nil || *cfg.AdminChatID == 0 {
			return errs.Invalid(map[string]string{"chat_id": "请填写要接收测试消息的 chat id，或先保存管理员群组 chat id"})
		}
		chatID = *cfg.AdminChatID
	}
	cfg, ready, err := s.telegramReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return errs.Newf(errs.ValidationFailed, "Telegram 还没配置好：启用开关、Bot 用户名、Bot Token 都要有")
	}
	if err := s.recordTestSend(ctx, "telegram", strconv.FormatInt(chatID, 10), nil); err != nil {
		return err
	}
	if err := s.tg.SendMessage(ctx, cfg.BotToken, chatID, "配置测试\n\n收到这条消息说明面板的 Telegram 通知已经通了。"); err != nil {
		var te *TelegramError
		if errors.As(err, &te) {
			return errs.Newf(errs.ValidationFailed, "发送失败："+te.Detail)
		}
		return errs.Wrap(errs.Internal, err)
	}
	return nil
}

func newWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
