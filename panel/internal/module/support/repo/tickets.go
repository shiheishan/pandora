package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/support/model"
)

// InsertTicket 插入工单，回填 ID、CreatedAt、UpdatedAt。
func (r *Repo) InsertTicket(ctx context.Context, t *model.Ticket) error {
	return mapErr(r.d.Conn(ctx).Create(t).Error)
}

// InsertMessages 插入一批消息（一条 SQL），回填 ID、CreatedAt。
func (r *Repo) InsertMessages(ctx context.Context, ms []*model.Message) error {
	if len(ms) == 0 {
		return nil
	}
	return mapErr(r.d.Conn(ctx).Create(ms).Error)
}

// GetTicket 读工单；owner 非 nil 时只读该用户的。不存在回 false。
func (r *Repo) GetTicket(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (model.Ticket, bool, error) {
	conn := r.d.Conn(ctx).Model(&model.Ticket{}).Where("id = ?", id)
	if owner != nil {
		conn = conn.Where("user_id = ?", *owner)
	}
	var t model.Ticket
	res := conn.Limit(1).Find(&t)
	if res.Error != nil {
		return model.Ticket{}, false, mapErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return model.Ticket{}, false, nil
	}
	return t, true, nil
}

// LockTicket 同 GetTicket，带 FOR UPDATE。
func (r *Repo) LockTicket(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (model.Ticket, bool, error) {
	conn := r.d.Conn(ctx).Model(&model.Ticket{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id)
	if owner != nil {
		conn = conn.Where("user_id = ?", *owner)
	}
	var t model.Ticket
	res := conn.Limit(1).Find(&t)
	if res.Error != nil {
		return model.Ticket{}, false, mapErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return model.Ticket{}, false, nil
	}
	return t, true, nil
}

// UpdateTicket 按 id 且 status = from 更新 cols（列名 → 值，nil 写 NULL），并写 updated_at；返回是否改到行。
func (r *Repo) UpdateTicket(ctx context.Context, id uuid.UUID, from string, cols map[string]any) (bool, error) {
	m := make(map[string]any, len(cols)+1)
	for k, v := range cols {
		m[k] = v
	}
	m["updated_at"] = now()
	res := r.d.Conn(ctx).Model(&model.Ticket{}).Where("id = ? AND status = ?", id, from).Updates(m)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// LockUserTickets 取该用户建单的事务级咨询锁（串行化「未结工单上限」）。
func (r *Repo) LockUserTickets(ctx context.Context, userID uuid.UUID) error {
	return mapErr(r.d.Conn(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
		"support:tickets:"+userID.String(),
	).Error)
}

// CountOpenTickets 数该用户未解决、未关闭的工单。
func (r *Repo) CountOpenTickets(ctx context.Context, userID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw(
		"SELECT count(*) FROM tickets WHERE user_id = ? AND status NOT IN ('resolved','closed')",
		userID,
	).Scan(&n).Error
	return n, mapErr(err)
}

// Messages 一张工单的消息，按 created_at、id 升序；includeInternal 为 false 时 SQL 里排除内部备注。
func (r *Repo) Messages(ctx context.Context, ticketID uuid.UUID, includeInternal bool) ([]model.Message, error) {
	conn := r.d.Conn(ctx).Where("ticket_id = ?", ticketID)
	if !includeInternal {
		conn = conn.Where("internal_note = false")
	}
	var out []model.Message
	if err := conn.Order("created_at, id").Find(&out).Error; err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		out = []model.Message{}
	}
	return out, nil
}

// EscalateOverdue 把首次响应超时、未响应、未解决未关闭、从没升级过的工单置为 escalated，返回改动的工单（含 user_id）。
// 只看 escalated_at IS NULL：每张单只自动升级一次，客服手动降回后不会被反复升级、优先级不会一路涨到 urgent。
func (r *Repo) EscalateOverdue(ctx context.Context, t time.Time, limit int) ([]Escalated, error) {
	var out []Escalated
	err := r.d.Conn(ctx).Raw(`
WITH candidates AS MATERIALIZED (
  SELECT id, status AS before_status, priority AS before_priority
    FROM tickets
   WHERE first_responded_at IS NULL
     AND sla_first_response_due < ?
     AND escalated_at IS NULL
     AND status NOT IN ('resolved', 'closed', 'escalated')
   ORDER BY sla_first_response_due, id
   LIMIT ?
   FOR UPDATE SKIP LOCKED
), updated AS (
  UPDATE tickets AS t
     SET status = 'escalated', escalated_at = ?, updated_at = ?,
         priority = CASE t.priority WHEN 'low' THEN 'normal' WHEN 'normal' THEN 'high' ELSE 'urgent' END
    FROM candidates AS c
   WHERE t.id = c.id
  RETURNING t.id, t.user_id, c.before_status, c.before_priority, t.priority
)
SELECT id, user_id, before_status, before_priority, priority FROM updated ORDER BY id`,
		t, limit, t, t,
	).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// TicketNos 批量取工单号。
func (r *Repo) TicketNos(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID       uuid.UUID `gorm:"column:id"`
		TicketNo string    `gorm:"column:ticket_no"`
	}
	if err := r.d.Conn(ctx).Raw("SELECT id, ticket_no FROM tickets WHERE id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.ID] = row.TicketNo
	}
	return out, nil
}

// UserTickets 门户「我的工单」：按 updated_at、id 倒序，最多 limit 张；统计只数非内部备注。
func (r *Repo) UserTickets(ctx context.Context, userID uuid.UUID, limit int) ([]TicketRow, error) {
	var rows []TicketRow
	err := r.d.Conn(ctx).Raw(`
SELECT t.*, coalesce(m.cnt, 0) AS message_count, coalesce(m.last_at, t.created_at) AS last_reply_at, '' AS last_author_kind
  FROM tickets t
  LEFT JOIN LATERAL (
    SELECT count(*)::int AS cnt, max(created_at) AS last_at
      FROM ticket_messages
     WHERE ticket_id = t.id AND internal_note = false
  ) m ON true
 WHERE t.user_id = ?
 ORDER BY t.updated_at DESC, t.id DESC
 LIMIT ?`, userID, limit).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		rows = []TicketRow{}
	}
	return rows, nil
}

// LastPublicAuthorKind 一张工单最后一条非内部备注、非系统消息的作者类型；没有这样的消息 ok 为 false。
func (r *Repo) LastPublicAuthorKind(ctx context.Context, ticketID uuid.UUID) (string, bool, error) {
	var authorKind string
	res := r.d.Conn(ctx).Raw(
		"SELECT author_kind FROM ticket_messages WHERE ticket_id = ? AND internal_note = false AND author_kind <> 'system' ORDER BY created_at DESC, id DESC LIMIT 1",
		ticketID,
	).Scan(&authorKind)
	if res.Error != nil {
		return "", false, mapErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return "", false, nil
	}
	return authorKind, true, nil
}
