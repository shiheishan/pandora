package repo

import (
	"context"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
)

// AnnouncementRow 是一条公告连同两个定向数组（永不为 nil）。
type AnnouncementRow struct {
	model.Announcement
	TargetPlanIDs      []uuid.UUID
	TargetUserGroupIDs []uuid.UUID
}

// PublishedAnnouncement 是定时发布转正的一条公告。
type PublishedAnnouncement struct {
	ID      uuid.UUID
	Version int
}

type announcementScanRow struct {
	model.Announcement
	PlanIDs  string `gorm:"column:plan_ids"`
	GroupIDs string `gorm:"column:group_ids"`
}

const announcementSelectCols = `a.id, a.title, a.body, a.severity, a.pinned, a.status, a.version,
       a.publish_at, a.expires_at, a.published_at, a.withdrawn_at, a.withdrawn_by,
       a.created_by, a.created_at, a.updated_at,
       array_to_string(a.target_plan_ids, ',') AS plan_ids,
       array_to_string(a.target_user_group_ids, ',') AS group_ids`

const announcementInsertReturning = `id, title, body, severity, pinned, status, version,
       publish_at, expires_at, published_at, withdrawn_at, withdrawn_by,
       created_by, created_at, updated_at,
       array_to_string(target_plan_ids, ',') AS plan_ids,
       array_to_string(target_user_group_ids, ',') AS group_ids`

// uuidArray 把 uuid 切片编成 PostgreSQL uuid 数组字面量。
func uuidArray(ids []uuid.UUID) string {
	if len(ids) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String()
}

func parseUUIDList(s string) []uuid.UUID {
	if s == "" {
		return []uuid.UUID{}
	}
	parts := strings.Split(s, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := uuid.Parse(p)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	if out == nil {
		return []uuid.UUID{}
	}
	return out
}

func scanAnnouncementRows(rows []announcementScanRow) []AnnouncementRow {
	if len(rows) == 0 {
		return []AnnouncementRow{}
	}
	out := make([]AnnouncementRow, len(rows))
	for i, row := range rows {
		out[i] = AnnouncementRow{
			Announcement:       row.Announcement,
			TargetPlanIDs:      parseUUIDList(row.PlanIDs),
			TargetUserGroupIDs: parseUUIDList(row.GroupIDs),
		}
	}
	return out
}

// ListAnnouncements 后台列表：未撤回/未下线的在前，再按 pinned 降序、发布时间降序取前 limit 条；
// 超过 limit 时只会截掉已撤回或已下线的旧公告。
func (r *Repo) ListAnnouncements(ctx context.Context, limit int) ([]AnnouncementRow, error) {
	var rows []announcementScanRow
	err := r.d.Conn(ctx).Raw(
		`SELECT `+announcementSelectCols+`
		   FROM announcements a
		  ORDER BY (a.status = 'withdrawn' OR (a.expires_at IS NOT NULL AND a.expires_at <= now())),
		           a.pinned DESC, COALESCE(a.published_at, a.created_at) DESC, a.id DESC
		  LIMIT ?`, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return scanAnnouncementRows(rows), nil
}

// LockAnnouncement 读一条公告并加 FOR UPDATE；不存在 ok 为 false。
func (r *Repo) LockAnnouncement(ctx context.Context, id uuid.UUID) (AnnouncementRow, bool, error) {
	var rows []announcementScanRow
	// Raw 不套用 clause.Locking，FOR UPDATE 直接写进 SQL
	err := r.d.Conn(ctx).Raw(
		`SELECT `+announcementSelectCols+`
		   FROM announcements a
		  WHERE a.id = ?
		    FOR UPDATE`, id,
	).Scan(&rows).Error
	if err != nil {
		return AnnouncementRow{}, false, err
	}
	if len(rows) == 0 {
		return AnnouncementRow{}, false, nil
	}
	return scanAnnouncementRows(rows)[0], true, nil
}

// InsertAnnouncement 新建公告，回填 ID、Version、CreatedAt、UpdatedAt。
func (r *Repo) InsertAnnouncement(ctx context.Context, a *AnnouncementRow) error {
	var got announcementScanRow
	err := r.d.Conn(ctx).Raw(
		`INSERT INTO announcements (
		   title, body, severity, pinned, target_plan_ids, target_user_group_ids,
		   status, publish_at, expires_at, published_at, created_by
		 ) VALUES (
		   ?, ?, ?, ?, CAST(? AS uuid[]), CAST(? AS uuid[]),
		   ?, ?, ?, ?, ?
		 )
		 RETURNING `+announcementInsertReturning,
		a.Title, a.Body, a.Severity, a.Pinned,
		uuidArray(a.TargetPlanIDs), uuidArray(a.TargetUserGroupIDs),
		a.Status, a.PublishAt, a.ExpiresAt, a.PublishedAt, a.CreatedBy,
	).Scan(&got).Error
	if err != nil {
		return err
	}
	row := scanAnnouncementRows([]announcementScanRow{got})[0]
	a.Announcement = row.Announcement
	a.TargetPlanIDs = row.TargetPlanIDs
	a.TargetUserGroupIDs = row.TargetUserGroupIDs
	return nil
}

// UpdateAnnouncement 按版本 CAS 改内容、定向、状态与时间，version 加 1；版本不符 ok 为 false。
func (r *Repo) UpdateAnnouncement(ctx context.Context, a AnnouncementRow, expectedVersion int) (newVersion int, ok bool, err error) {
	var ver int
	res := r.d.Conn(ctx).Raw(
		`UPDATE announcements SET
		   title = ?, body = ?, severity = ?, pinned = ?,
		   target_plan_ids = CAST(? AS uuid[]), target_user_group_ids = CAST(? AS uuid[]),
		   status = ?, publish_at = ?, expires_at = ?, published_at = ?,
		   version = version + 1, updated_at = now()
		 WHERE id = ? AND version = ?
		 RETURNING version`,
		a.Title, a.Body, a.Severity, a.Pinned,
		uuidArray(a.TargetPlanIDs), uuidArray(a.TargetUserGroupIDs),
		a.Status, a.PublishAt, a.ExpiresAt, a.PublishedAt,
		a.ID, expectedVersion,
	).Scan(&ver)
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, false, nil
	}
	return ver, true, nil
}

// WithdrawAnnouncement 按版本 CAS 置 withdrawn（记撤回人与时间），version 加 1；版本不符或已撤回 ok 为 false。
func (r *Repo) WithdrawAnnouncement(ctx context.Context, id, by uuid.UUID, expectedVersion int) (newVersion int, ok bool, err error) {
	var ver int
	res := r.d.Conn(ctx).Raw(
		`UPDATE announcements SET
		   status = 'withdrawn', withdrawn_at = now(), withdrawn_by = ?,
		   version = version + 1, updated_at = now()
		 WHERE id = ? AND version = ? AND status <> 'withdrawn'
		 RETURNING version`, by, id, expectedVersion,
	).Scan(&ver)
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, false, nil
	}
	return ver, true, nil
}

// PublishDueAnnouncements 把 publish_at <= now 的 scheduled 公告转为 published（published_at = now，version 加 1）。
func (r *Repo) PublishDueAnnouncements(ctx context.Context, now time.Time) ([]PublishedAnnouncement, error) {
	out := []PublishedAnnouncement{}
	err := r.d.Conn(ctx).Raw(
		`UPDATE announcements SET
		   status = 'published', published_at = ?, version = version + 1, updated_at = now()
		 WHERE status = 'scheduled' AND publish_at <= ?
		 RETURNING id, version`, now, now,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []PublishedAnnouncement{}, nil
	}
	return out, nil
}

// VisibleAnnouncements 门户可见公告：已发布、在时间窗内、定向命中（groupID 为 nil 表示没有分组），
// 按 pinned 降序、COALESCE(published_at, created_at) 降序取前 limit 条。
func (r *Repo) VisibleAnnouncements(ctx context.Context, now time.Time, groupID *uuid.UUID, planIDs []uuid.UUID, limit int) ([]model.Announcement, error) {
	out := []model.Announcement{}
	planLit := uuidArray(planIDs)
	var err error
	if groupID == nil {
		err = r.d.Conn(ctx).Raw(
			`SELECT id, title, body, severity, pinned, status, version,
			        publish_at, expires_at, published_at, withdrawn_at, withdrawn_by,
			        created_by, created_at, updated_at
			   FROM announcements
			  WHERE status = 'published'
			    AND (publish_at IS NULL OR publish_at <= ?)
			    AND (expires_at IS NULL OR expires_at > ?)
			    AND (cardinality(target_user_group_ids) = 0)
			    AND (cardinality(target_plan_ids) = 0 OR target_plan_ids && CAST(? AS uuid[]))
			  ORDER BY pinned DESC, COALESCE(published_at, created_at) DESC, id DESC
			  LIMIT ?`, now, now, planLit, limit,
		).Scan(&out).Error
	} else {
		err = r.d.Conn(ctx).Raw(
			`SELECT id, title, body, severity, pinned, status, version,
			        publish_at, expires_at, published_at, withdrawn_at, withdrawn_by,
			        created_by, created_at, updated_at
			   FROM announcements
			  WHERE status = 'published'
			    AND (publish_at IS NULL OR publish_at <= ?)
			    AND (expires_at IS NULL OR expires_at > ?)
			    AND (cardinality(target_user_group_ids) = 0 OR ? = ANY(target_user_group_ids))
			    AND (cardinality(target_plan_ids) = 0 OR target_plan_ids && CAST(? AS uuid[]))
			  ORDER BY pinned DESC, COALESCE(published_at, created_at) DESC, id DESC
			  LIMIT ?`, now, now, *groupID, planLit, limit,
		).Scan(&out).Error
	}
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []model.Announcement{}, nil
	}
	return out, nil
}
