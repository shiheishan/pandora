package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

const (
	keyTelegramEnabled       = "telegram.enabled"
	keyTelegramBotUsername   = "telegram.bot_username"
	keyTelegramBotToken      = "telegram.bot_token" //nolint:gosec // 设置键名；值经 Sealer 封存（AAD 为键名）
	keyTelegramAdminChatID   = "telegram.admin_chat_id"
	keyTelegramWebhookSecret = "telegram.webhook_secret" //nolint:gosec // 设置键名；值经 Sealer 封存（AAD 为键名）

	auditTelegramChanged = "settings.telegram_updated"
)

var (
	telegramKeys      = []string{keyTelegramEnabled, keyTelegramBotUsername, keyTelegramBotToken, keyTelegramAdminChatID, keyTelegramWebhookSecret}
	telegramBotUserRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
	telegramWebhookRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Telegram 是 Telegram 通知设置。BotToken、WebhookSecret 是解封后的明文，只给发送与 webhook 校验用；后台展示只看 HasToken。
type Telegram struct {
	Enabled       bool
	BotUsername   string
	BotToken      string
	HasToken      bool
	AdminChatID   *int64
	WebhookSecret string
}

// TelegramUpdate 是 SetTelegram 的输入。BotToken、WebhookSecret 为 nil 表示不改（不支持清除）；
// AdminChatSet 为 false 时不动管理员群组，为 true 时写 AdminChatID（nil 即删除该键）。
type TelegramUpdate struct {
	Enabled       bool
	BotUsername   string
	BotToken      *string
	AdminChatSet  bool
	AdminChatID   *int64
	WebhookSecret *string
}

// Telegram 读取 Telegram 通知设置。
func (s *Service) Telegram(ctx context.Context) (Telegram, error) {
	vals, err := s.store.Values(ctx, telegramKeys)
	if err != nil {
		return Telegram{}, err
	}
	enabled, err := decodeOr(vals, keyTelegramEnabled, false)
	if err != nil {
		return Telegram{}, err
	}
	username, err := decodeOr(vals, keyTelegramBotUsername, "")
	if err != nil {
		return Telegram{}, err
	}
	adminChat, err := decodeTelegramAdminChat(vals)
	if err != nil {
		return Telegram{}, err
	}

	m := Telegram{
		Enabled:     enabled,
		BotUsername: username,
		AdminChatID: adminChat,
	}

	encToken, err := decodeOr(vals, keyTelegramBotToken, "")
	if err != nil {
		return Telegram{}, err
	}
	if encToken != "" {
		m.HasToken = true
		m.BotToken = s.openSealed(ctx, keyTelegramBotToken, encToken)
	}

	encSecret, err := decodeOr(vals, keyTelegramWebhookSecret, "")
	if err != nil {
		return Telegram{}, err
	}
	if encSecret != "" {
		m.WebhookSecret = s.openSealed(ctx, keyTelegramWebhookSecret, encSecret)
	}
	return m, nil
}

// ValidateTelegram 规整并校验一份 Telegram 设置更新（SetTelegram 落库前同样调它）。
// 调用方要在动外部状态（如向 Telegram 注册 webhook）之前先调，免得外部已改、落库却被拒。
func ValidateTelegram(v *TelegramUpdate) error {
	v.BotUsername = strings.TrimPrefix(strings.TrimSpace(v.BotUsername), "@")
	if v.BotToken != nil {
		*v.BotToken = strings.TrimSpace(*v.BotToken)
	}
	if v.WebhookSecret != nil {
		*v.WebhookSecret = strings.TrimSpace(*v.WebhookSecret)
	}

	fields := map[string]string{}
	if v.BotUsername != "" && !telegramBotUserRe.MatchString(v.BotUsername) {
		fields["bot_username"] = "Bot 用户名只能是 5–32 位字母、数字、下划线"
	}
	if v.Enabled && v.BotUsername == "" {
		fields["bot_username"] = "启用前要填 Bot 用户名，用户要靠它找到你的 bot"
	}
	if v.BotToken != nil {
		if *v.BotToken == "" || len(*v.BotToken) > 256 || strings.IndexFunc(*v.BotToken, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) >= 0 {
			fields["bot_token"] = "Bot Token 格式不正确"
		}
	}
	if v.AdminChatSet && v.AdminChatID != nil && *v.AdminChatID == 0 {
		fields["admin_chat_id"] = "管理员群组 chat id 必须是非 0 整数"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	if v.WebhookSecret != nil {
		sec := *v.WebhookSecret
		if len(sec) < 16 || len(sec) > 256 || !telegramWebhookRe.MatchString(sec) {
			return errs.Wrap(errs.Internal, errors.New("settings: webhook_secret 格式无效"))
		}
	}
	return nil
}

// SetTelegram 写入 Telegram 通知设置。
func (s *Service) SetTelegram(ctx context.Context, v TelegramUpdate) error {
	if err := ValidateTelegram(&v); err != nil {
		return err
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, telegramKeys)
		if err != nil {
			return err
		}
		oldEnabled, err := decodeOr(vals, keyTelegramEnabled, false)
		if err != nil {
			return err
		}
		oldUsername, err := decodeOr(vals, keyTelegramBotUsername, "")
		if err != nil {
			return err
		}
		oldAdminChat, err := decodeTelegramAdminChat(vals)
		if err != nil {
			return err
		}

		before := map[string]any{
			keyTelegramEnabled:     oldEnabled,
			keyTelegramBotUsername: oldUsername,
		}
		after := map[string]any{
			keyTelegramEnabled:     v.Enabled,
			keyTelegramBotUsername: v.BotUsername,
		}
		changes, b, a := diff(before, after)

		adminChatChanged := false
		if v.AdminChatSet {
			newAdmin := v.AdminChatID
			if !telegramAdminChatEqual(oldAdminChat, newAdmin) {
				adminChatChanged = true
				b[keyTelegramAdminChatID] = adminChatAuditValue(oldAdminChat)
				a[keyTelegramAdminChatID] = adminChatAuditValue(newAdmin)
				if newAdmin == nil {
					if err := s.store.Delete(ctx, keyTelegramAdminChatID); err != nil {
						return err
					}
				} else {
					changes = append(changes, change{key: keyTelegramAdminChatID, value: *newAdmin})
				}
			}
		}

		if v.BotToken != nil {
			enc, err := s.sealSetting(keyTelegramBotToken, *v.BotToken)
			if err != nil {
				return err
			}
			changes = append(changes, change{key: keyTelegramBotToken, value: enc})
		}
		if v.WebhookSecret != nil {
			enc, err := s.sealSetting(keyTelegramWebhookSecret, *v.WebhookSecret)
			if err != nil {
				return err
			}
			changes = append(changes, change{key: keyTelegramWebhookSecret, value: enc})
		}

		if len(changes) == 0 && !adminChatChanged && v.BotToken == nil && v.WebhookSecret == nil {
			return nil
		}
		if len(changes) > 0 {
			if err := s.writeChanges(ctx, changes); err != nil {
				return err
			}
		}
		return s.writeAudit(ctx, auditTelegramChanged, auditResourceSettings, map[string]any{
			"group":                  "telegram",
			"before":                 b,
			"after":                  a,
			"bot_token_changed":      v.BotToken != nil,
			"webhook_secret_changed": v.WebhookSecret != nil,
			"admin_chat_changed":     adminChatChanged,
		})
	})
}

func decodeTelegramAdminChat(vals map[string]json.RawMessage) (*int64, error) {
	raw, ok := vals[keyTelegramAdminChatID]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	v, err := decodeOr(vals, keyTelegramAdminChatID, int64(0))
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func telegramAdminChatEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func adminChatAuditValue(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func (s *Service) sealSetting(key, plain string) (string, error) {
	if s.sealer == nil {
		return "", errs.Wrap(errs.Internal, errors.New("settings: sealer 未配置"))
	}
	blob, err := s.sealer.Seal([]byte(plain), []byte(key))
	if err != nil {
		return "", errs.Wrap(errs.Internal, err)
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

func (s *Service) openSealed(ctx context.Context, key, enc string) string {
	if s.sealer == nil {
		plog.From(ctx).Error("settings: 设置项解封失败", zap.String("key", key), zap.Error(errors.New("settings: sealer 未配置")))
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		plog.From(ctx).Error("settings: 设置项解封失败", zap.String("key", key), zap.Error(err))
		return ""
	}
	plain, err := s.sealer.Open(blob, []byte(key))
	if err != nil {
		plog.From(ctx).Error("settings: 设置项解封失败", zap.String("key", key), zap.Error(err))
		return ""
	}
	return string(plain)
}
