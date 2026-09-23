package notify

// 用户能看到的公告。
//
// 放在 notify 包里而不是新开一个：公告和站内信解决的是同一件事 ——
// 把平台想说的话送到用户眼前，只是一个对所有人、一个对具体某人。

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// Announcement 是给用户看的一条公告。
type Announcement struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Pinned   bool   `json:"pinned"`
	At       any    `json:"published_at"`
}

// VisibleAnnouncements 返回这个用户当前该看到的公告。
//
// 三重过滤：状态、时间窗、定向。定向为空表示面向所有人 ——
// 绝大多数公告都是这种，所以这条分支要放在前面短路掉。
func (s *Service) VisibleAnnouncements(ctx context.Context, tenantID, userID string) ([]Announcement, error) {
	out := []Announcement{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id::text,
			       COALESCE(a.content->>'title',''), COALESCE(a.content->>'body',''),
			       a.severity, a.pinned, COALESCE(a.published_at, a.created_at)
			  FROM announcements a
			 WHERE a.tenant_id = $1
			   AND a.status = 'published'
			   AND (a.publish_at IS NULL OR a.publish_at <= now())
			   AND (a.expires_at IS NULL OR a.expires_at > now())
			   -- 用户组定向：与套餐定向是「并且」关系。
			   -- 两个都填的话，意思是「买了这些套餐、并且属于这些组的人」
			   AND (
			     cardinality(a.target_user_group_ids) = 0
			     OR EXISTS (
			       SELECT 1 FROM users u
			        WHERE u.id = $2::uuid
			          AND u.user_group_id = ANY(a.target_user_group_ids)
			     )
			   )
			   AND (
			     cardinality(a.target_plan_ids) = 0
			     OR EXISTS (
			       SELECT 1 FROM subscriptions s
			        WHERE s.tenant_id = a.tenant_id
			          AND s.user_id = $2::uuid
			          AND s.plan_id = ANY(a.target_plan_ids)
			          AND s.status IN ('active','trialing','grace')
			     )
			   )
			 ORDER BY a.pinned DESC, COALESCE(a.published_at, a.created_at) DESC
			 LIMIT 20`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Announcement
			if err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Severity,
				&a.Pinned, &a.At); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// PublishDueAnnouncements 把到点的定时公告转为已发布。
//
// 定时发布必须有人到点去转正，否则 scheduled 状态会永远停在那里 ——
// 运营以为自己排好了周二早上的维护通知，实际上它一直没发出去。
func (s *Service) PublishDueAnnouncements(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE announcements
			   SET status = 'published', published_at = now(),
			       version = version + 1, updated_at = now()
			 WHERE tenant_id = $1 AND status = 'scheduled'
			   AND publish_at IS NOT NULL AND publish_at <= now()
			 RETURNING id::text, version`, tenantID)
		if err != nil {
			return err
		}
		type publishedAnnouncement struct {
			id      string
			version int
		}
		published := []publishedAnnouncement{}
		for rows.Next() {
			var item publishedAnnouncement
			if err := rows.Scan(&item.id, &item.version); err != nil {
				rows.Close()
				return err
			}
			published = append(published, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range published {
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "system", Action: "announcement.published",
				ResourceType: "announcement", ResourceID: &item.id,
				APIDomain: "admin", Outcome: "success",
				AfterDigest: map[string]any{"status": "published", "version": item.version},
			}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
