package repo

import (
	"context"
	"strings"
)

// buildQueueWhere 拼后台队列 WHERE 子句与参数（用户输入只走占位符）。
func buildQueueWhere(f QueueFilter) (string, []any) {
	var parts []string
	var args []any

	if len(f.Statuses) > 0 {
		parts = append(parts, "t.status IN ?")
		args = append(args, f.Statuses)
	}
	if f.Priority != "" {
		parts = append(parts, "t.priority = ?")
		args = append(args, f.Priority)
	}
	if f.Category != "" {
		parts = append(parts, "t.category = ?")
		args = append(args, f.Category)
	}
	if f.AssignedTo != nil {
		parts = append(parts, "t.assigned_to = ?")
		args = append(args, *f.AssignedTo)
	}
	if f.Query != "" {
		pat := likePattern(f.Query)
		q := "(t.ticket_no ILIKE ? ESCAPE '\\' OR t.subject ILIKE ? ESCAPE '\\'"
		args = append(args, pat, pat)
		if len(f.UserIDs) > 0 {
			q += " OR t.user_id IN ?"
			args = append(args, f.UserIDs)
		}
		q += ")"
		parts = append(parts, q)
	}
	if f.OnlyBreached {
		parts = append(parts, "t.first_responded_at IS NULL AND t.sla_first_response_due < ? AND t.status NOT IN ('resolved','closed')")
		args = append(args, f.Now)
	}
	if len(parts) == 0 {
		return "true", nil
	}
	return strings.Join(parts, " AND "), args
}

// queueOrder 是后台队列的排序：升级的最前，再按优先级、建单时间、id。
const queueOrder = `(t.status = 'escalated') DESC,
          CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,
          t.created_at, t.id`

// Queue 后台队列：升级的最前，再按优先级、建单时间、id；统计数全部消息。返回本页与总数。
func (r *Repo) Queue(ctx context.Context, f QueueFilter) ([]TicketRow, int64, error) {
	where, args := buildQueueWhere(f)
	conn := r.d.Conn(ctx)

	var total int64
	if err := conn.Raw("SELECT count(*) FROM tickets t WHERE "+where, args...).Scan(&total).Error; err != nil {
		return nil, 0, mapErr(err)
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, f.Limit, f.Offset)
	var rows []TicketRow
	// 先按排序取出本页的工单，再只对这一页做消息统计：LATERAL 放在 LIMIT 之前会对全部命中行各跑一遍。
	sql := `WITH page AS MATERIALIZED (
  SELECT t.* FROM tickets t
   WHERE ` + where + `
   ORDER BY ` + queueOrder + `
   LIMIT ? OFFSET ?
)
SELECT t.*, coalesce(m.cnt, 0) AS message_count, coalesce(m.last_at, t.created_at) AS last_reply_at,
       coalesce(lk.author_kind, '') AS last_author_kind
  FROM page t
  LEFT JOIN LATERAL (
    SELECT count(*)::int AS cnt, max(created_at) AS last_at FROM ticket_messages WHERE ticket_id = t.id
  ) m ON true
  LEFT JOIN LATERAL (
    SELECT author_kind FROM ticket_messages
     WHERE ticket_id = t.id AND internal_note = false
     ORDER BY created_at DESC, id DESC LIMIT 1
  ) lk ON true
 ORDER BY ` + queueOrder
	if err := conn.Raw(sql, listArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	if rows == nil {
		rows = []TicketRow{}
	}
	return rows, total, nil
}
