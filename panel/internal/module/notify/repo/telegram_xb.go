package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// BindingByChatID 按 chat_id 读绑定（私聊的 chat id 就是 Telegram 用户 id）；没有 ok 为 false。走 telegram_bindings_chat_key。
func (r *Repo) BindingByChatID(ctx context.Context, chatID int64) (model.TelegramBinding, bool, error) {
	var b model.TelegramBinding
	res := r.d.Conn(ctx).Where("chat_id = ?", chatID).Limit(1).Find(&b)
	if res.Error != nil {
		return model.TelegramBinding{}, false, res.Error
	}
	return b, res.RowsAffected == 1, nil
}

// InsertTicketMessage 写一条工单提醒映射；(chat_id, message_id) 已存在时什么都不做。
func (r *Repo) InsertTicketMessage(ctx context.Context, m *model.TelegramTicketMessage) error {
	return constraintErrs.Map(r.d.Conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(m).Error)
}

// TicketForMessage 按 (chat_id, message_id) 找工单 id；没有 ok 为 false。
func (r *Repo) TicketForMessage(ctx context.Context, chatID, messageID int64) (uuid.UUID, bool, error) {
	var m model.TelegramTicketMessage
	res := r.d.Conn(ctx).Where("chat_id = ? AND message_id = ?", chatID, messageID).Limit(1).Find(&m)
	if res.Error != nil {
		return uuid.Nil, false, res.Error
	}
	return m.TicketID, res.RowsAffected == 1, nil
}

// PurgeTicketMessages 删 created_at < before 的映射，每次至多 batch 行。
func (r *Repo) PurgeTicketMessages(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`DELETE FROM telegram_ticket_messages WHERE (chat_id, message_id) IN (
		   SELECT chat_id, message_id FROM telegram_ticket_messages WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
		before, batch)
	return int(res.RowsAffected), res.Error
}
