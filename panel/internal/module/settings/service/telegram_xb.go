package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const (
	keyTelegramSubCommand        = "telegram.sub_command_enabled" // bool，缺省 false：/sub 命令（经 Telegram 下发订阅链接）
	keyTelegramStartMessage      = "telegram.start_message"       // string，缺省空串 = 用内置欢迎文案
	keyTelegramAdminPaidNotify   = "telegram.admin_paid_notify"   // bool，缺省 false：收款推到管理员群组
	keyTelegramAdminTicketNotify = "telegram.admin_ticket_notify" // bool，缺省 false：工单推到管理员群组
	keyTelegramJoinReview        = "telegram.join_review_enabled" // bool，缺省 false：用户群入群审核
	keyTelegramUserGroupChatID   = "telegram.user_group_chat_id"  // int64，没设就没有这一行
	keyTelegramDiscussLink       = "telegram.discuss_link"        // string，缺省空串 = 门户不显示

	auditTelegramExtrasChanged = "settings.telegram_extras_updated"
	// TelegramStartMessageMax 是 /start 欢迎文案的字数上限。
	TelegramStartMessageMax = 1000
	// TelegramDiscussLinkMax 是讨论群链接的长度上限。
	TelegramDiscussLinkMax = 200
)

var telegramExtrasKeys = []string{
	keyTelegramSubCommand,
	keyTelegramStartMessage,
	keyTelegramAdminPaidNotify,
	keyTelegramAdminTicketNotify,
	keyTelegramJoinReview,
	keyTelegramUserGroupChatID,
	keyTelegramDiscussLink,
}

// TelegramExtras 是 Telegram 的扩展设置（Xboard 补齐）：都缺省关。
type TelegramExtras struct {
	SubCommand        bool
	StartMessage      string
	AdminPaidNotify   bool
	AdminTicketNotify bool
	JoinReview        bool
	UserGroupChatID   *int64
	DiscussLink       string
}

// TelegramExtras 读取 Telegram 扩展设置。
func (s *Service) TelegramExtras(ctx context.Context) (TelegramExtras, error) {
	vals, err := s.store.Values(ctx, telegramExtrasKeys)
	if err != nil {
		return TelegramExtras{}, err
	}
	return decodeTelegramExtras(vals)
}

// ValidateTelegramExtras 规整（StartMessage、DiscussLink 去首尾空白）并校验一份完整的扩展设置；SetTelegramExtras 落库前同样调它。
func ValidateTelegramExtras(v *TelegramExtras) error {
	v.StartMessage = strings.TrimSpace(v.StartMessage)
	v.DiscussLink = strings.TrimSpace(v.DiscussLink)

	fields := map[string]string{}
	if utf8.RuneCountInString(v.StartMessage) > TelegramStartMessageMax {
		fields["start_message"] = "欢迎文案最多 1000 个字"
	}
	if strings.IndexFunc(v.StartMessage, func(r rune) bool { return r == 0 }) >= 0 {
		fields["start_message"] = "不能包含空字符"
	}
	if v.DiscussLink != "" {
		if strings.IndexFunc(v.DiscussLink, func(r rune) bool { return r == 0 }) >= 0 {
			fields["discuss_link"] = "讨论群链接要以 https:// 开头，如 https://t.me/xxx"
		} else if len(v.DiscussLink) > TelegramDiscussLinkMax {
			fields["discuss_link"] = "讨论群链接要以 https:// 开头，如 https://t.me/xxx"
		} else {
			u, err := url.Parse(v.DiscussLink)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				fields["discuss_link"] = "讨论群链接要以 https:// 开头，如 https://t.me/xxx"
			}
		}
	}
	if v.UserGroupChatID != nil && *v.UserGroupChatID >= 0 {
		fields["user_group_chat_id"] = "用户群 chat id 必须是负数（群组的 chat id 以 - 开头）"
	}
	if v.JoinReview && v.UserGroupChatID == nil {
		fields["user_group_chat_id"] = "开启入群审核前要填用户群 chat id"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// SetTelegramExtras 整份写入（不是部分更新）：锁住这组键、与旧值比较、只写变了的键、写一条审计。
func (s *Service) SetTelegramExtras(ctx context.Context, v TelegramExtras) error {
	if err := ValidateTelegramExtras(&v); err != nil {
		return err
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, telegramExtrasKeys)
		if err != nil {
			return err
		}
		old, err := decodeTelegramExtras(vals)
		if err != nil {
			return err
		}

		changes, b, a := diff(telegramExtrasMap(old), telegramExtrasMap(v))

		userGroupChanged := false
		if !telegramUserGroupChatEqual(old.UserGroupChatID, v.UserGroupChatID) {
			userGroupChanged = true
			b[keyTelegramUserGroupChatID] = adminChatAuditValue(old.UserGroupChatID)
			a[keyTelegramUserGroupChatID] = adminChatAuditValue(v.UserGroupChatID)
			if v.UserGroupChatID == nil {
				if err := s.store.Delete(ctx, keyTelegramUserGroupChatID); err != nil {
					return err
				}
			} else {
				changes = append(changes, change{key: keyTelegramUserGroupChatID, value: *v.UserGroupChatID})
			}
		}

		startMessageChanged := old.StartMessage != v.StartMessage
		delete(b, keyTelegramStartMessage)
		delete(a, keyTelegramStartMessage)
		if len(changes) == 0 && !userGroupChanged {
			return nil
		}
		if len(changes) > 0 {
			if err := s.writeChanges(ctx, changes); err != nil {
				return err
			}
		}
		return s.writeAudit(ctx, auditTelegramExtrasChanged, auditResourceSettings, map[string]any{
			"group":                 "telegram_extras",
			"before":                b,
			"after":                 a,
			"start_message_changed": startMessageChanged,
		})
	})
}

func decodeTelegramExtras(vals map[string]json.RawMessage) (TelegramExtras, error) {
	sub, err := decodeOr(vals, keyTelegramSubCommand, false)
	if err != nil {
		return TelegramExtras{}, err
	}
	start, err := decodeOr(vals, keyTelegramStartMessage, "")
	if err != nil {
		return TelegramExtras{}, err
	}
	paid, err := decodeOr(vals, keyTelegramAdminPaidNotify, false)
	if err != nil {
		return TelegramExtras{}, err
	}
	ticket, err := decodeOr(vals, keyTelegramAdminTicketNotify, false)
	if err != nil {
		return TelegramExtras{}, err
	}
	join, err := decodeOr(vals, keyTelegramJoinReview, false)
	if err != nil {
		return TelegramExtras{}, err
	}
	userGroup, err := decodeTelegramUserGroupChat(vals)
	if err != nil {
		return TelegramExtras{}, err
	}
	discuss, err := decodeOr(vals, keyTelegramDiscussLink, "")
	if err != nil {
		return TelegramExtras{}, err
	}
	return TelegramExtras{
		SubCommand:        sub,
		StartMessage:      start,
		AdminPaidNotify:   paid,
		AdminTicketNotify: ticket,
		JoinReview:        join,
		UserGroupChatID:   userGroup,
		DiscussLink:       discuss,
	}, nil
}

func decodeTelegramUserGroupChat(vals map[string]json.RawMessage) (*int64, error) {
	raw, ok := vals[keyTelegramUserGroupChatID]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	v, err := decodeOr(vals, keyTelegramUserGroupChatID, int64(0))
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func telegramUserGroupChatEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func telegramExtrasMap(v TelegramExtras) map[string]any {
	return map[string]any{
		keyTelegramSubCommand:        v.SubCommand,
		keyTelegramStartMessage:      v.StartMessage,
		keyTelegramAdminPaidNotify:   v.AdminPaidNotify,
		keyTelegramAdminTicketNotify: v.AdminTicketNotify,
		keyTelegramJoinReview:        v.JoinReview,
		keyTelegramDiscussLink:       v.DiscussLink,
	}
}
