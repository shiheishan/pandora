package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
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

// botAllowedUpdates 是 webhook 要收的更新类型：消息；开了入群审核再加 chat_join_request。
func botAllowedUpdates(ex settingssvc.TelegramExtras) []string {
	out := []string{"message"}
	if ex.JoinReview {
		out = append(out, "chat_join_request")
	}
	return out
}

// botCommands 是私聊命令菜单：start「开始使用 / 绑定账号」、traffic「查看流量与到期时间」、
// sub「获取订阅链接」（只在 ex.SubCommand 为真时有）、unbind「解除绑定」，按这个顺序。
func botCommands(ex settingssvc.TelegramExtras) []botCommand {
	cmds := []botCommand{
		{Command: "start", Description: "开始使用 / 绑定账号"},
		{Command: "traffic", Description: "查看流量与到期时间"},
	}
	if ex.SubCommand {
		cmds = append(cmds, botCommand{Command: "sub", Description: "获取订阅链接"})
	}
	cmds = append(cmds, botCommand{Command: "unbind", Description: "解除绑定"})
	return cmds
}

// telegramWebhookURL 是 Telegram webhook 地址：当前生效的主域名 + /v1/webhooks/telegram；ok 为 false 表示主域名不是 https（Telegram 只收 https）。
func (s *Service) telegramWebhookURL(ctx context.Context) (url string, ok bool) {
	base := s.settings.SiteURL(ctx)
	if !strings.HasPrefix(base, "https://") {
		return "", false
	}
	return strings.TrimRight(base, "/") + "/v1/webhooks/telegram", true
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

	ex, err := s.settings.TelegramExtras(ctx)
	if err != nil {
		return err
	}

	registeredWebhook := false
	hookURL, httpsOK := s.telegramWebhookURL(ctx)
	if in.Enabled && effectiveToken != "" && httpsOK {
		secret, err := newWebhookSecret()
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if err := s.tg.SetWebhook(ctx, effectiveToken, hookURL, secret, botAllowedUpdates(ex), true); err != nil {
			var te *TelegramError
			if errors.As(err, &te) {
				return errs.Newf(errs.ValidationFailed, "设置 Telegram webhook 失败："+te.Detail)
			}
			return errs.Wrap(errs.Internal, err)
		}
		update.WebhookSecret = &secret
		registeredWebhook = true
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

	if err := s.settings.SetTelegram(ctx, update); err != nil {
		return err
	}
	if registeredWebhook {
		s.syncBotCommands(ctx, effectiveToken, ex)
	}
	return nil
}

// syncBotCommands 向 Telegram 注册命令菜单；失败只记日志。
func (s *Service) syncBotCommands(ctx context.Context, token string, ex settingssvc.TelegramExtras) {
	if token == "" {
		return
	}
	if err := s.tg.SetMyCommands(ctx, token, botCommands(ex)); err != nil {
		plog.From(ctx).Warn("notify: 注册 Telegram 命令菜单失败", zap.Error(err))
	}
}

// TelegramExtrasInput 是后台「Telegram 扩展」的保存输入：指针为 nil 表示不改；UserGroupChatSet 为 false 时不动用户群，
// 为 true 时写 UserGroupChatID（nil 即清除）。
type TelegramExtrasInput struct {
	SubCommand        *bool
	StartMessage      *string
	AdminPaidNotify   *bool
	AdminTicketNotify *bool
	JoinReview        *bool
	UserGroupChatSet  bool
	UserGroupChatID   *int64
	DiscussLink       *string
}

// TelegramExtras 读后台「Telegram 扩展」设置。
func (s *Service) TelegramExtras(ctx context.Context) (settingssvc.TelegramExtras, error) {
	return s.settings.TelegramExtras(ctx)
}

// SaveTelegramExtras 保存扩展设置：读当前值、合并、settingssvc.ValidateTelegramExtras 校验；
// 入群审核开关变了、且 webhook 已注册（Telegram 已启用、有 token、面板对外地址 https、已有 webhook secret）时，
// 先用现有 secret 重新 setWebhook 换 allowed_updates（失败回 errs.Newf(errs.ValidationFailed, "设置 Telegram webhook 失败："+te.Detail)，什么都不落库）；
// 再 SetTelegramExtras 落库；webhook 已注册时最后 syncBotCommands（/sub 开关会改命令菜单）。
func (s *Service) SaveTelegramExtras(ctx context.Context, in TelegramExtrasInput) error {
	cur, err := s.settings.TelegramExtras(ctx)
	if err != nil {
		return err
	}
	merged := cur
	if in.SubCommand != nil {
		merged.SubCommand = *in.SubCommand
	}
	if in.StartMessage != nil {
		merged.StartMessage = *in.StartMessage
	}
	if in.AdminPaidNotify != nil {
		merged.AdminPaidNotify = *in.AdminPaidNotify
	}
	if in.AdminTicketNotify != nil {
		merged.AdminTicketNotify = *in.AdminTicketNotify
	}
	if in.JoinReview != nil {
		merged.JoinReview = *in.JoinReview
	}
	if in.UserGroupChatSet {
		merged.UserGroupChatID = in.UserGroupChatID
	}
	if in.DiscussLink != nil {
		merged.DiscussLink = *in.DiscussLink
	}
	if err := settingssvc.ValidateTelegramExtras(&merged); err != nil {
		return err
	}

	joinReviewChanged := in.JoinReview != nil && *in.JoinReview != cur.JoinReview

	cfg, err := s.settings.Telegram(ctx)
	if err != nil {
		return err
	}
	hookURL, httpsOK := s.telegramWebhookURL(ctx)
	webhookRegistered := cfg.Enabled && cfg.BotToken != "" && httpsOK && cfg.WebhookSecret != ""

	if joinReviewChanged && webhookRegistered {
		if err := s.tg.SetWebhook(ctx, cfg.BotToken, hookURL, cfg.WebhookSecret, botAllowedUpdates(merged), false); err != nil {
			var te *TelegramError
			if errors.As(err, &te) {
				return errs.Newf(errs.ValidationFailed, "设置 Telegram webhook 失败："+te.Detail)
			}
			return errs.Wrap(errs.Internal, err)
		}
	}

	if err := s.settings.SetTelegramExtras(ctx, merged); err != nil {
		return err
	}
	if webhookRegistered {
		s.syncBotCommands(ctx, cfg.BotToken, merged)
	}
	return nil
}

var _ settingssvc.SiteURLChangeListener = (*Service)(nil)

// telegramRehookTimeout 是主域名改动后重新注册 webhook 的总时长（setWebhook 与命令菜单两次调用）。
const telegramRehookTimeout = 15 * time.Second

// SiteURLChanged 实现 settings 的 SiteURLChangeListener：后台改了主域名并提交后，webhook 已注册（启用、有 token、有 secret）时
// 用现有 secret 向 Telegram 重新注册到新地址（drop_pending_updates=false，不丢排队的更新），再同步命令菜单。
// 在提交后同步调用；失败只记日志、不回滚设置，可以在后台 Telegram 设置里再保存一次重注册。
func (s *Service) SiteURLChanged(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telegramRehookTimeout)
	defer cancel()
	log := plog.From(ctx)
	cfg, err := s.settings.Telegram(ctx)
	if err != nil {
		log.Warn("notify: 主域名改了，Telegram webhook 没有重新注册（读 Telegram 设置失败）", zap.Error(err))
		return
	}
	if !cfg.Enabled || cfg.BotToken == "" || cfg.WebhookSecret == "" {
		return
	}
	hookURL, ok := s.telegramWebhookURL(ctx)
	if !ok {
		log.Warn("notify: 主域名不是 https，Telegram webhook 没有重新注册，用户无法完成绑定")
		return
	}
	ex, err := s.settings.TelegramExtras(ctx)
	if err != nil {
		log.Warn("notify: 主域名改了，Telegram webhook 没有重新注册（读 Telegram 扩展设置失败）", zap.Error(err))
		return
	}
	if err := s.tg.SetWebhook(ctx, cfg.BotToken, hookURL, cfg.WebhookSecret, botAllowedUpdates(ex), false); err != nil {
		log.Warn("notify: 主域名改了，Telegram webhook 重新注册失败", zap.Error(plog.StripURL(err)))
		return
	}
	log.Info("notify: 主域名改了，Telegram webhook 已重新注册到新地址")
	s.syncBotCommands(ctx, cfg.BotToken, ex)
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
	if _, err := s.tg.SendMessage(ctx, cfg.BotToken, chatID, "配置测试\n\n收到这条消息说明面板的 Telegram 通知已经通了。"); err != nil {
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
