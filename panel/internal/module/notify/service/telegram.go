package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/module/notify/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// BindCodeTTL 是绑定码有效期。
const BindCodeTTL = 10 * time.Minute

const bindCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

var errBindRollback = errors.New("notify: bind rollback")

// TelegramBinding 读门户绑定状态。
func (s *Service) TelegramBinding(ctx context.Context, userID uuid.UUID) (TelegramBindingInfo, error) {
	cfg, ready, err := s.telegramReady(ctx)
	if err != nil {
		return TelegramBindingInfo{}, err
	}
	out := TelegramBindingInfo{
		Enabled:     ready,
		BotUsername: "",
	}
	if ready {
		out.BotUsername = cfg.BotUsername
		if ex, err := s.settings.TelegramExtras(ctx); err == nil && ex.DiscussLink != "" {
			out.DiscussLink = ex.DiscussLink
		}
	}
	b, ok, err := s.store.Binding(ctx, userID)
	if err != nil {
		return TelegramBindingInfo{}, err
	}
	if ok {
		out.Bound = true
		out.Username = b.Username
	}
	return out, nil
}

// IssueBindCode 给用户发一个一次性绑定码；同一用户只留最新一个未用的码。
func (s *Service) IssueBindCode(ctx context.Context, userID uuid.UUID) (BindCode, error) {
	cfg, ready, err := s.telegramReady(ctx)
	if err != nil {
		return BindCode{}, err
	}
	if !ready {
		return BindCode{}, errs.Newf(errs.ValidationFailed, "站点还没有启用 Telegram 通知")
	}

	now := s.now()
	expiresAt := now.Add(BindCodeTTL)
	var code string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		_, ok, err := s.store.Binding(ctx, userID)
		if err != nil {
			return err
		}
		if ok {
			return errs.Newf(errs.Conflict, "你已经绑定过 Telegram 了，要换一个请先解绑")
		}
		if err := s.store.DeleteUnusedBindCodes(ctx, userID); err != nil {
			return err
		}
		// 8 位、31 个字符约 8.5e11 种，撞上概率可以忽略；真撞上时事务已被唯一约束打断、不能在同一事务里重试，
		// 直接报内部错误，用户再点一次即可
		c, err := newBindCode()
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if err := s.store.InsertBindCode(ctx, &model.TelegramBindCode{
			UserID:    userID,
			CodeHash:  s.bindCodeHash(c),
			ExpiresAt: expiresAt,
			// created_at 与 expires_at 取同一个时钟（表上有 expires_at > created_at 的 CHECK）
			CreatedAt: now,
		}); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Wrap(errs.Internal, errors.New("notify: 绑定码撞上已有的码"))
			}
			return err
		}
		code = c
		return nil
	})
	if err != nil {
		return BindCode{}, err
	}
	return BindCode{Code: code, BotUsername: cfg.BotUsername, ExpiresAt: expiresAt}, nil
}

// UnbindTelegram 解除本人的 Telegram 绑定（门户与后台「个人设置」用）。
func (s *Service) UnbindTelegram(ctx context.Context, userID uuid.UUID) error {
	return s.unbindTelegram(ctx, userID, nil, "")
}

// unbindTelegram 是门户、后台与 bot /unbind 共用的解绑：删绑定、压掉还在排队的 Telegram 投递、写审计 telegram.unbound。
// actor 非 nil 时（bot 里没有登录主体）审计记 ActorID=actor、Audience="public"；via 非空时 data 加 "via"。
func (s *Service) unbindTelegram(ctx context.Context, userID uuid.UUID, actor *uuid.UUID, via string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.store.DeleteBinding(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.NotFound, "你还没有绑定 Telegram")
		}
		n, err := s.store.SuppressQueuedTelegram(ctx, userID)
		if err != nil {
			return err
		}
		data := map[string]any{"suppressed_deliveries": n}
		if via != "" {
			data["via"] = via
		}
		evt := auditsvc.Event{
			Action:       "telegram.unbound",
			ResourceType: "user",
			ResourceID:   &userID,
			Data:         data,
		}
		if actor != nil {
			// 经 bot 解绑没有登录主体：发命令的人此刻是管理员就记 admin，否则记 public
			evt.ActorID = actor
			evt.Audience = "public"
			if s.admins != nil {
				isAdmin, err := s.admins.IsActiveAdmin(ctx, *actor)
				if err != nil {
					return err
				}
				if isAdmin {
					evt.Audience = "admin"
				}
			}
		}
		return s.audit.Write(ctx, evt)
	})
}

// HandleTelegramUpdate 处理 bot webhook 推来的一条更新：校验 secret，处理群工单回复、入群审核与私聊消息。
func (s *Service) HandleTelegramUpdate(ctx context.Context, secret string, body []byte) error {
	cfg, err := s.settings.Telegram(ctx)
	if err != nil {
		return err
	}
	// 关闭即拒绝：关闭期间 Telegram 可能仍按旧 webhook 推送，不处理绑定、不用 token 回消息
	if !cfg.Enabled || cfg.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.WebhookSecret)) != 1 {
		return errs.New(errs.Unauthorized)
	}

	var upd tgWebhookUpdate
	if err := json.Unmarshal(body, &upd); err != nil {
		return nil
	}

	ex, err := s.settings.TelegramExtras(ctx)
	if err != nil {
		plog.From(ctx).Warn("notify: 读取 Telegram 扩展设置失败", zap.Error(err))
		ex = settingssvc.TelegramExtras{}
	}

	if upd.ChatJoinRequest != nil {
		s.handleJoinRequest(ctx, cfg, ex, upd.ChatJoinRequest)
		return nil
	}

	if upd.Message != nil && upd.Message.Chat != nil {
		switch upd.Message.Chat.Type {
		case "private":
			text := strings.TrimSpace(upd.Message.Text)
			if text == "" {
				return nil
			}
			chatID := upd.Message.Chat.ID
			username := ""
			if upd.Message.From != nil {
				username = truncateRunes(upd.Message.From.Username, 64)
			}
			reply := s.handlePrivateMessage(ctx, ex, chatID, username, text)
			if reply != "" && cfg.BotToken != "" {
				if _, err := s.tg.SendMessage(ctx, cfg.BotToken, chatID, reply); err != nil {
					plog.From(ctx).Warn("notify: Telegram 回复私聊消息失败", zap.Error(err))
				}
			}
			return nil

		case "group", "supergroup":
			if upd.Message.ReplyToMessage != nil {
				s.handleAdminReply(ctx, cfg, upd.Message)
			}
			return nil
		}
	}

	return nil
}

// PurgeBindCodes 删除过期早于 before 的绑定码。
func (s *Service) PurgeBindCodes(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.store.PurgeBindCodes(ctx, before, batch)
}

func (s *Service) bindCodeHash(code string) []byte {
	return s.keys.HMAC(crypto.InfoHMACNotify, []byte("telegram-bind:"+code))
}

func (s *Service) handleBindMessage(ctx context.Context, text string, chatID int64, username string) string {
	code, promptOnly := parseBindCodeInput(text)
	if promptOnly {
		return "请发送面板上显示的 8 位绑定码。"
	}
	if code == "" {
		return "请发送面板上显示的 8 位绑定码。"
	}

	// 码不对、重复绑定这类结果用哨兵错误让事务回滚，提示文字记在 reply 里，事务结束后再回给用户
	var reply string
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		c, ok, err := s.store.LockBindCode(ctx, s.bindCodeHash(code), s.now())
		if err != nil {
			return err
		}
		if !ok {
			reply = "绑定码无效或已过期，请回到面板重新获取。"
			return errBindRollback
		}
		// 先写绑定、再消耗码：重复时什么都没改，码留给用户换个账号再用
		userID := c.UserID
		err = s.store.InsertBinding(ctx, &model.TelegramBinding{
			UserID:   userID,
			ChatID:   chatID,
			Username: username,
		})
		if err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				reply = "这个 Telegram 账号已经绑定了其它用户，或你的账号已经绑定过了。"
				return errBindRollback
			}
			return err
		}
		used, err := s.store.MarkBindCodeUsed(ctx, c.ID)
		if err != nil {
			return err
		}
		if !used {
			// 码已加 FOR UPDATE 锁，走到这里说明数据不一致；回滚整笔
			return errs.Wrap(errs.Internal, errors.New("notify: 绑定码已被使用"))
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "telegram.bound",
			ResourceType: "user",
			ResourceID:   &userID,
			ActorID:      &userID,
			Audience:     "public",
			Data:         map[string]any{"username": username},
		}); err != nil {
			return err
		}
		reply = "绑定成功，以后会在这里收到通知。"
		return nil
	})
	if err != nil {
		if errors.Is(err, errBindRollback) {
			return reply
		}
		plog.From(ctx).Warn("notify: Telegram 绑定事务失败", zap.Error(err))
		return ""
	}
	return reply
}

type tgWebhookUpdate struct {
	Message         *tgWebhookMessage  `json:"message"`
	ChatJoinRequest *tgChatJoinRequest `json:"chat_join_request"`
}

type tgWebhookMessage struct {
	MessageID      int64             `json:"message_id"`
	Chat           *tgWebhookChat    `json:"chat"`
	From           *tgWebhookUser    `json:"from"`
	Text           string            `json:"text"`
	ReplyToMessage *tgWebhookReplyTo `json:"reply_to_message"`
}

type tgWebhookChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgWebhookReplyTo struct {
	MessageID int64 `json:"message_id"`
}

type tgWebhookUser struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type tgChatJoinRequest struct {
	Chat *tgWebhookChat `json:"chat"`
	From *tgWebhookUser `json:"from"`
}

func parseBindCodeInput(text string) (code string, promptOnly bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "/start") {
		rest := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
		if rest == "" {
			return "", true
		}
		text = rest
	}
	text = strings.ToUpper(strings.TrimSpace(text))
	if len(text) != 8 {
		return "", false
	}
	for _, r := range text {
		if !strings.ContainsRune(bindCodeAlphabet, r) {
			return "", false
		}
	}
	return text, false
}

func newBindCode() (string, error) {
	const reject = 31 * 8 // 248，拒绝采样避免取模偏差
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		for {
			var b [1]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			if int(b[0]) >= reject {
				continue
			}
			out[i] = bindCodeAlphabet[int(b[0])%len(bindCodeAlphabet)]
			break
		}
	}
	return string(out), nil
}
