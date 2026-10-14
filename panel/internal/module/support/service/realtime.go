package service

// 工单的实时推送。由本模块在写成功的事务里显式发，
// 提交后才真正发出（realtime.Publisher 内部用 tx.AfterCommit），回滚不发。
//
// 推给谁：
//   - tickets 的新建与每次更新 → tickets.changed，推给工单主人（tickets.user_id）与管理端；
//   - ticket_messages 的新消息 → tickets.changed：公开消息同上，推给工单主人而不是消息作者
//     （客服回复时作者是客服，迁移 00022 就是为这个把消息表从触发器里摘掉的）；
//     内部备注只推管理端，用户不该从推送里知道有备注；
//   - 用户或客服的每条新回复另发 ticket.updated（只推工单主人的频道），门户工单详情据此刷新。

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 推送载荷里的表名。
const (
	rtTableTickets  = "tickets"
	rtTableMessages = "ticket_messages"
)

// UseRealtime 接上实时推送（启动装配时调用，不并发安全）。没接上时不推。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// ticketChanged 在当前事务提交后推一条工单行变更给工单主人与管理端。op 取 realtime.OpInsert / OpUpdate。
func (s *Service) ticketChanged(ctx context.Context, op string, ticketID, owner uuid.UUID) {
	s.rt.Changed(ctx, realtime.TopicTickets, rtTableTickets, op, ticketID, &owner)
}

// messagesAdded 在当前事务提交后推新消息：公开消息推给工单主人与管理端，内部备注只推管理端。
// msgs 必须已插入（ID 已回填）。
func (s *Service) messagesAdded(ctx context.Context, owner uuid.UUID, msgs []*model.Message) {
	for _, m := range msgs {
		if m.InternalNote {
			s.rt.Notify(ctx, realtime.TopicTickets, map[string]any{
				"table": rtTableMessages,
				"op":    realtime.OpInsert,
				"id":    m.ID.String(),
			}, realtime.ChannelAdmin())
			continue
		}
		s.rt.Changed(ctx, realtime.TopicTickets, rtTableMessages, realtime.OpInsert, m.ID, &owner)
	}
}

// ticketReplied 在当前事务提交后给工单主人推 ticket.updated（用户或客服的新公开回复；内部备注不调）。
func (s *Service) ticketReplied(ctx context.Context, ticketID, owner uuid.UUID) {
	s.rt.Notify(ctx, realtime.TopicTicketUpdated, map[string]any{"ticket_id": ticketID.String()},
		realtime.ChannelUser(owner))
}
