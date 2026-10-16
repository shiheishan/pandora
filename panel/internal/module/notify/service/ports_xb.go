package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ---- Xboard 补齐（xb-tg）：Telegram 用户命令、入群审核、管理员群组提醒与在 TG 里回复工单要的跨模块端口 ----
// 都由启动装配注入（app/wire.go；不是 *xxx.Service 直接满足的，适配器在 app/adapters.go）。

// SubscriptionSummaries 是 /traffic 与工单提醒要的订阅摘要；*subsvc.Service 直接满足。
type SubscriptionSummaries interface {
	// SummariesByUsers 批量取用户的全部订阅摘要（含状态口径、套餐名与配额）。
	SummariesByUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]subsvc.UserSubscriptions, error)
}

// SubscribeLinks 是 /sub 要的订阅链接（属 subscription），由 app 适配器实现。
type SubscribeLinks interface {
	// ActiveLinks 返回该用户每份生效订阅当前的订阅链接（与门户「订阅链接」同一口径，不含已过期的），没有返回空切片。
	ActiveLinks(ctx context.Context, userID uuid.UUID) ([]string, error)
}

// Balances 是工单提醒要的用户余额（属 ledger），由 app 适配器实现。
type Balances interface {
	// UserBalances 批量取用户余额；没有账户的用户为 0。
	UserBalances(ctx context.Context, userIDs []uuid.UUID) (balances map[uuid.UUID]decimal.Decimal, err error)
}

// Admins 是管理员名单（属 auth）；*authsvc.Service 直接满足。在 TG 里回复工单前校验发消息的人此刻仍是可登录的管理员。
type Admins interface {
	IsActiveAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// TicketReplier 以管理员身份公开回复工单（属 support），由 app 适配器实现：与后台回复走同一个 service 方法、同样写审计
// （审计 data 带 via=telegram）。调用前 notify 已核对过群组、绑定与管理员身份。
type TicketReplier interface {
	ReplyAsAdmin(ctx context.Context, adminID, ticketID uuid.UUID, body string) error
}

// xbStore 是 xb-tg 新增的 repo 方法（*repo.Repo 实现，见 repo/telegram_xb.go）。
type xbStore interface {
	// BindingByChatID 按 chat_id 读绑定（私聊的 chat id 就是 Telegram 用户 id）；没有 ok 为 false。
	BindingByChatID(ctx context.Context, chatID int64) (model.TelegramBinding, bool, error)
	// InsertTicketMessage 写一条工单提醒映射；(chat_id, message_id) 已存在时什么都不做。
	InsertTicketMessage(ctx context.Context, m *model.TelegramTicketMessage) error
	// TicketForMessage 按 (chat_id, message_id) 找工单 id；没有 ok 为 false。
	TicketForMessage(ctx context.Context, chatID, messageID int64) (uuid.UUID, bool, error)
	// PurgeTicketMessages 删 created_at < before 的映射，每次至多 batch 行，返回删除行数。
	PurgeTicketMessages(ctx context.Context, before time.Time, batch int) (int, error)
}

// TicketMessageRetentionDays 是工单提醒映射的保留期（与投递记录相同）：更早的提醒消息再被回复就不处理了。
const TicketMessageRetentionDays = RetentionDays

// UseSubscriptionSummaries 只在启动装配时调用（不并发安全）。
func (s *Service) UseSubscriptionSummaries(x SubscriptionSummaries) { s.summaries = x }

// UseSubscribeLinks 只在启动装配时调用（不并发安全）。
func (s *Service) UseSubscribeLinks(x SubscribeLinks) { s.links = x }

// UseBalances 只在启动装配时调用（不并发安全）。
func (s *Service) UseBalances(x Balances) { s.balances = x }

// UseAdmins 只在启动装配时调用（不并发安全）。
func (s *Service) UseAdmins(x Admins) { s.admins = x }

// UseTicketReplier 只在启动装配时调用（不并发安全）。
func (s *Service) UseTicketReplier(x TicketReplier) { s.replier = x }

// missingXBPorts 是 xb-tg 端口里没装配的（MissingPorts 合并进去）。
func (s *Service) missingXBPorts() []string {
	var out []string
	if s.summaries == nil {
		out = append(out, "SubscriptionSummaries")
	}
	if s.links == nil {
		out = append(out, "SubscribeLinks")
	}
	if s.balances == nil {
		out = append(out, "Balances")
	}
	if s.admins == nil {
		out = append(out, "Admins")
	}
	if s.replier == nil {
		out = append(out, "TicketReplier")
	}
	return out
}

// PurgeTicketMessages 删除早于 before 的工单提醒映射（retention:notify）。
func (s *Service) PurgeTicketMessages(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.store.PurgeTicketMessages(ctx, before, batch)
}
