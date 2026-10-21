package repo

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/content/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

// AdminRow 是后台读到的一个版本，带同 slug 的最大版本号与「同受众里是否最新」。
type AdminRow struct {
	model.Page
	LatestVersion      int  `gorm:"column:latest_version"`
	IsLatestInAudience bool `gorm:"column:is_latest_in_audience"`
}

// AdminFilter 是后台列表筛选。
type AdminFilter struct {
	Kind, Status, Query string
	Limit               int
}

const adminDerivedCols = `,
		(SELECT max(a.version) FROM content_pages a WHERE a.slug = cp.slug) AS latest_version,
		NOT EXISTS (SELECT 1 FROM content_pages n WHERE n.slug = cp.slug AND n.version > cp.version AND n.target_plan_ids = cp.target_plan_ids AND n.visibility = cp.visibility) AS is_latest_in_audience`

const adminPageColsNoBody = `cp.id, cp.slug, cp.kind, cp.category, cp.version, cp.title, cp.summary, '' AS body, cp.sanitizer_version,
		cp.target_plan_ids, cp.visibility, cp.status,
		cp.review_due_at, cp.published_at, cp.created_by, cp.created_at, cp.updated_at`

const adminPageColsWithBody = `cp.id, cp.slug, cp.kind, cp.category, cp.version, cp.title, cp.summary, cp.body, cp.sanitizer_version,
		cp.target_plan_ids, cp.visibility, cp.status,
		cp.review_due_at, cp.published_at, cp.created_by, cp.created_at, cp.updated_at`

// ListAdmin 后台分页列出内容版本（不含正文）。
func (r *Repo) ListAdmin(ctx context.Context, f AdminFilter) ([]AdminRow, error) {
	var where []string
	var args []any
	if strings.TrimSpace(f.Kind) != "" {
		where = append(where, "cp.kind = ?")
		args = append(args, f.Kind)
	}
	if strings.TrimSpace(f.Status) != "" {
		where = append(where, "cp.status = ?")
		args = append(args, f.Status)
	}
	q := strings.TrimSpace(f.Query)
	if q != "" {
		pat := db.ContainsPattern(q)
		where = append(where, "(cp.slug ILIKE ? OR cp.title ILIKE ?)")
		args = append(args, pat, pat)
	}
	sql := "SELECT " + adminPageColsNoBody + adminDerivedCols + " FROM content_pages cp"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit)
	sql += " ORDER BY cp.slug, cp.version DESC LIMIT ?"

	var rows []AdminRow
	if err := r.d.Conn(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		rows = []AdminRow{}
	}
	return rows, nil
}

// GetAdmin 按 id 读后台详情（含正文）。
func (r *Repo) GetAdmin(ctx context.Context, id uuid.UUID) (AdminRow, bool, error) {
	var row AdminRow
	sql := "SELECT " + adminPageColsWithBody + adminDerivedCols + " FROM content_pages cp WHERE cp.id = ?"
	if err := r.d.Conn(ctx).Raw(sql, id).Scan(&row).Error; err != nil {
		return AdminRow{}, false, mapErr(err)
	}
	if row.ID == uuid.Nil {
		return AdminRow{}, false, nil
	}
	return row, true, nil
}

// LatestVersionForUpdate 取 slug 当前最大版本号并加锁；没有任何版本返回 0。
func (r *Repo) LatestVersionForUpdate(ctx context.Context, slug string) (int, error) {
	var version int
	err := r.d.Conn(ctx).Raw(
		`SELECT version FROM content_pages WHERE slug = ? ORDER BY version DESC LIMIT 1 FOR UPDATE`,
		slug,
	).Scan(&version).Error
	if err != nil {
		return 0, mapErr(err)
	}
	return version, nil
}

// Audience 描述「同一受众」的维度（数组字段为 JSON 原文）。
type Audience struct {
	Slug, TargetPlanIDs, Visibility string
}

// Superseded 是被归档的同受众已发布版本。
type Superseded struct {
	ID      uuid.UUID `gorm:"column:id"`
	Version int       `gorm:"column:version"`
}

// ArchivePublishedInAudience 把同受众里已发布的版本归档。
func (r *Repo) ArchivePublishedInAudience(ctx context.Context, a Audience) ([]Superseded, error) {
	var out []Superseded
	err := r.d.Conn(ctx).Raw(`
		UPDATE content_pages SET status = 'archived', updated_at = now()
		 WHERE slug = ? AND status = 'published'
		   AND target_plan_ids = ?::jsonb
		   AND visibility = ?
		 RETURNING id, version`,
		a.Slug, a.TargetPlanIDs, a.Visibility,
	).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		out = []Superseded{}
	}
	return out, nil
}

// InsertPage 插入一个新版本。
func (r *Repo) InsertPage(ctx context.Context, p *model.Page) error {
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// LockPage 按 id 加行锁读取。
func (r *Repo) LockPage(ctx context.Context, id uuid.UUID) (model.Page, bool, error) {
	var p model.Page
	err := r.d.Conn(ctx).Raw("SELECT * FROM content_pages WHERE id = ? FOR UPDATE", id).Scan(&p).Error
	if err != nil {
		return model.Page{}, false, mapErr(err)
	}
	if p.ID == uuid.Nil {
		return model.Page{}, false, nil
	}
	return p, true, nil
}

// ArchivePage 按 id 与版本号归档（已归档则 false）。
func (r *Repo) ArchivePage(ctx context.Context, id uuid.UUID, version int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`
		UPDATE content_pages SET status = 'archived', updated_at = now()
		 WHERE id = ? AND version = ? AND status <> 'archived'`, id, version)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected > 0, nil
}

// VisibleQuery 是门户可见内容的查询条件。
type VisibleQuery struct {
	Kind, Slug, Query string
	PlanIDs           []string
	IncludeBody       bool
}

// VisibleRow 是门户查询的一行，带搜索是否命中。
type VisibleRow struct {
	model.Page
	Matched bool `gorm:"column:matched"`
}

// Visible 查询已发布且对登录用户可见的版本候选（每 slug 多版本，由 service 挑可见版）。
func (r *Repo) Visible(ctx context.Context, q VisibleQuery) ([]VisibleRow, error) {
	bodyCol := "'' AS body"
	if q.IncludeBody {
		bodyCol = "cp.body"
	}
	var where []string
	var args []any

	args = append(args, q.Query, q.Query)
	where = append(where, "cp.status = 'published'")
	where = append(where, "cp.visibility = 'authenticated'")

	if strings.TrimSpace(q.Kind) != "" {
		where = append(where, "cp.kind = ?")
		args = append(args, q.Kind)
	}
	if strings.TrimSpace(q.Slug) != "" {
		where = append(where, "cp.slug = ?")
		args = append(args, q.Slug)
	}
	if len(q.PlanIDs) == 0 {
		where = append(where, "jsonb_array_length(cp.target_plan_ids) = 0")
	} else {
		where = append(where, "(jsonb_array_length(cp.target_plan_ids) = 0 OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(cp.target_plan_ids) AS e(v) WHERE e.v IN ?))")
		args = append(args, q.PlanIDs)
	}

	sql := `SELECT cp.id, cp.slug, cp.kind, cp.category, cp.version, cp.title, cp.summary, ` + bodyCol + `, cp.sanitizer_version,
		cp.target_plan_ids, cp.visibility, cp.status,
		cp.review_due_at, cp.published_at, cp.created_by, cp.created_at, cp.updated_at,
		(? = '' OR strpos(lower(cp.title || ' ' || cp.summary || ' ' || cp.body), lower(?)) > 0) AS matched
		FROM content_pages cp WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY cp.slug, cp.version DESC LIMIT 200`

	var rows []VisibleRow
	if err := r.d.Conn(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		rows = []VisibleRow{}
	}
	return rows, nil
}
