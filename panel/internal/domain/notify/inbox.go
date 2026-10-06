// [INPUT]: 依赖 platform/db 的租户事务，依赖同包 notify.go 的 Render；读写 notification_deliveries（inapp 渠道）、notification_templates、notification_preferences
// [OUTPUT]: 对外提供 InboxItem、PreferenceOverride，以及 Service 的 Inbox / MarkInboxRead / MarkAllInboxRead / PreferenceOverrides / SetPreference
// [POS]: notify 的门户读写面：站内信收件箱、已读标记与通知偏好覆盖项，从 api/public/notifications.go 下沉；偏好目录、锁定项与参数校验仍在处理器

package notify

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 站内信不是单独的一套东西，就是投递记录里 channel='inapp' 的那些。
// 用户拉取即视为送达，读了才标 read_at —— 这两件事分开记，
// 因为「收到了」和「看了」在运营上是不同的信号。

// InboxItem 是收件箱里的一条站内信，主题与正文已用记录里的变量渲染。
type InboxItem struct {
	ID      string     `json:"id"`
	Code    string     `json:"code"`
	Subject string     `json:"subject"`
	Body    string     `json:"body"`
	SentAt  *time.Time `json:"sent_at"`
	ReadAt  *time.Time `json:"read_at"`
}

// Inbox 返回本人最近的站内信（onlyUnread 只看未读）与全部未读数，同一事务读出。
// 列表为空时返回 []InboxItem{}，不是 nil。
func (s *Service) Inbox(ctx context.Context, tenantID, userID string, onlyUnread bool, limit int) ([]InboxItem, int, error) {
	out := []InboxItem{}
	unread := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			// 模板与投递记录一起查：正文要用记录里的变量渲染模板，
			// 分两次查会让 N 条站内信变成 N+1 次查询
			q := `
				SELECT d.id::text, d.template_code,
				       COALESCE(t.subject,''), COALESCE(t.body,''),
				       COALESCE(d.payload,'{}'::jsonb), d.sent_at, d.read_at
				  FROM notification_deliveries d
				  LEFT JOIN notification_templates t
				    ON t.tenant_id = d.tenant_id AND t.code = d.template_code
				   AND t.channel = 'inapp' AND t.locale = 'zh-CN' AND t.status = 'active'
				 WHERE d.tenant_id = $1 AND d.user_id = $2::uuid
				   AND d.channel = 'inapp' AND d.status = 'sent'`
			if onlyUnread {
				q += ` AND d.read_at IS NULL`
			}
			q += ` ORDER BY d.created_at DESC LIMIT $3`

			rows, err := tx.Query(ctx, q, tenantID, userID, limit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var it InboxItem
				var vars map[string]any
				if err := rows.Scan(&it.ID, &it.Code, &it.Subject, &it.Body,
					&vars, &it.SentAt, &it.ReadAt); err != nil {
					return err
				}
				it.Subject = Render(it.Subject, vars)
				it.Body = Render(it.Body, vars)
				out = append(out, it)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `
				SELECT count(*) FROM notification_deliveries
				 WHERE tenant_id = $1 AND user_id = $2::uuid
				   AND channel = 'inapp' AND status = 'sent' AND read_at IS NULL`,
				tenantID, userID).Scan(&unread)
		})
	if err != nil {
		return nil, 0, err
	}
	return out, unread, nil
}

// MarkInboxRead 把本人的一条站内信标为已读；id 必须已是 UUID 形状（调用方先挡），
// 不存在、不属于本人或已读都不算错误。
func (s *Service) MarkInboxRead(ctx context.Context, tenantID, userID, id string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			// 条件里带 user_id：光凭 id 就能改的话，
			// 拿到别人的通知 ID 就能替他标已读
			_, err := tx.Exec(ctx, `
				UPDATE notification_deliveries SET read_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
				   AND channel = 'inapp' AND read_at IS NULL`,
				tenantID, id, userID)
			return err
		})
}

// MarkAllInboxRead 把本人全部未读站内信标为已读。
func (s *Service) MarkAllInboxRead(ctx context.Context, tenantID, userID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE notification_deliveries SET read_at = now()
				 WHERE tenant_id = $1 AND user_id = $2::uuid
				   AND channel = 'inapp' AND read_at IS NULL`,
				tenantID, userID)
			return err
		})
}

// PreferenceOverride 是用户存下的一条偏好覆盖项；没存的组合沿用默认值。
type PreferenceOverride struct {
	Category string
	Channel  string
	Enabled  bool
}

// PreferenceOverrides 读本人存下的全部偏好覆盖项。
func (s *Service) PreferenceOverrides(ctx context.Context, tenantID, userID string) ([]PreferenceOverride, error) {
	var out []PreferenceOverride
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT category, channel, enabled
				  FROM notification_preferences
				 WHERE tenant_id = $1 AND user_id = $2::uuid`, tenantID, userID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var o PreferenceOverride
				if err := rows.Scan(&o.Category, &o.Channel, &o.Enabled); err != nil {
					return err
				}
				out = append(out, o)
			}
			return rows.Err()
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetPreference 按主键 upsert 一条偏好；类别与渠道的合法性、交易类不可关闭由调用方先判，
// 数据库约束再兜底。
func (s *Service) SetPreference(ctx context.Context, tenantID, userID, category, channel string, enabled bool) error {
	// 冲突目标必须与表主键 (user_id, category, channel) 一致（00008）：
	// 多写一个 tenant_id 就没有匹配的唯一约束，PostgreSQL 直接拒绝这条语句（缺陷 7）。
	// 用户只属于一个租户，按主键冲突不会跨租户误改。
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO notification_preferences
					(tenant_id, user_id, category, channel, enabled)
				VALUES ($1,$2::uuid,$3,$4,$5)
				ON CONFLICT (user_id, category, channel)
				DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
				tenantID, userID, category, channel, enabled)
			return err
		})
}
