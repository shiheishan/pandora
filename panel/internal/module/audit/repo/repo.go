// Package repo 是 audit 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"context"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Repo 封装 audit_events 的读写。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// Insert 写一行；id、occurred_at 由库生成（model 里带 default 标签，GORM 会省略零值并 RETURNING）。
func (r *Repo) Insert(ctx context.Context, e *model.Event) error {
	return r.d.Conn(ctx).Create(e).Error
}

// Query 是列表、计数、导出共用的筛选；零值表示不筛。
type Query struct {
	ActionPrefix   string      // action LIKE 前缀（转义 % _ \ ）
	ActorKind      string      // actor_kind = ?
	Outcome        string      // outcome = ?
	Text           string      // 搜索框：非空时加 (action ILIKE '%'||转义后||'%' OR actor_id IN TextActorIDs OR resource_id = TextResourceID)，
	TextActorIDs   []uuid.UUID //   后两项分别只在切片非空 / 指针非空时出现在 OR 里
	TextResourceID *uuid.UUID
	From, To       *time.Time // occurred_at >= From、occurred_at < To
	AsOf           *time.Time // occurred_at <= AsOf（导出用，冻结范围）
}

// Record 是查询出的一行（只取后台要的列）。
type Record struct {
	ID            uuid.UUID  `gorm:"column:id"`
	OccurredAt    time.Time  `gorm:"column:occurred_at"`
	ActorKind     string     `gorm:"column:actor_kind"`
	ActorID       *uuid.UUID `gorm:"column:actor_id"`
	ActorAudience *string    `gorm:"column:actor_audience"`
	AuthContext   *string    `gorm:"column:auth_context"`
	Action        string     `gorm:"column:action"`
	ResourceType  *string    `gorm:"column:resource_type"`
	ResourceID    *uuid.UUID `gorm:"column:resource_id"`
	Outcome       string     `gorm:"column:outcome"`
	Reason        *string    `gorm:"column:reason"`
	IPEnc         []byte     `gorm:"column:ip_enc"`
}

// Count 返回命中行数和库里的 now()（一条 SQL：SELECT count(*), now() ...）。
func (r *Repo) Count(ctx context.Context, q Query) (int64, time.Time, error) {
	var total int64
	var asOf time.Time
	err := apply(r.d.Conn(ctx).Table("audit_events"), q).
		Select("count(*) AS cnt, now() AS as_of").
		Row().Scan(&total, &asOf)
	return total, asOf, err
}

// List 按 occurred_at DESC, id DESC 取一页。
func (r *Repo) List(ctx context.Context, q Query, limit, offset int) ([]Record, error) {
	var recs []Record
	err := selectRecords(apply(r.d.Conn(ctx).Table("audit_events"), q)).
		Order("occurred_at DESC, id DESC").
		Limit(limit).Offset(offset).
		Scan(&recs).Error
	if err != nil {
		return nil, err
	}
	if recs == nil {
		return []Record{}, nil
	}
	return recs, nil
}

// Cursor 是导出的键集分页位置。
type Cursor struct {
	OccurredAt time.Time
	ID         uuid.UUID
}

// ListAfter 键集分页：after 非空时加 (occurred_at, id) < (?, ?)，同样按 occurred_at DESC, id DESC，LIMIT limit。
func (r *Repo) ListAfter(ctx context.Context, q Query, after *Cursor, limit int) ([]Record, error) {
	tx := apply(r.d.Conn(ctx).Table("audit_events"), q)
	if after != nil {
		tx = tx.Where("(occurred_at, id) < (?, ?)", after.OccurredAt, after.ID)
	}
	var recs []Record
	err := selectRecords(tx).
		Order("occurred_at DESC, id DESC").
		Limit(limit).
		Scan(&recs).Error
	if err != nil {
		return nil, err
	}
	if recs == nil {
		return []Record{}, nil
	}
	return recs, nil
}

// Purge 调库函数 purge_audit_events(before, batch)，返回删除行数。
func (r *Repo) Purge(ctx context.Context, before time.Time, batch int) (int, error) {
	var n int
	err := r.d.Conn(ctx).Raw("SELECT purge_audit_events(?, ?)", before, batch).Scan(&n).Error
	return n, err
}

func selectRecords(tx *gorm.DB) *gorm.DB {
	return tx.Select(
		"id", "occurred_at", "actor_kind", "actor_id", "actor_audience", "auth_context",
		"action", "resource_type", "resource_id", "outcome",
		"data->>'reason' AS reason", "ip_enc",
	)
}

func apply(tx *gorm.DB, q Query) *gorm.DB {
	if q.ActionPrefix != "" {
		pat := db.EscapeLike(q.ActionPrefix) + "%"
		tx = tx.Where("action LIKE ? ESCAPE '\\'", pat)
	}
	if q.ActorKind != "" {
		tx = tx.Where("actor_kind = ?", q.ActorKind)
	}
	if q.Outcome != "" {
		tx = tx.Where("outcome = ?", q.Outcome)
	}
	if q.From != nil {
		tx = tx.Where("occurred_at >= ?", *q.From)
	}
	if q.To != nil {
		tx = tx.Where("occurred_at < ?", *q.To)
	}
	if q.AsOf != nil {
		tx = tx.Where("occurred_at <= ?", *q.AsOf)
	}
	if q.Text != "" {
		pat := db.ContainsPattern(q.Text)
		parts := []string{"action ILIKE ? ESCAPE '\\'"}
		args := []any{pat}
		if len(q.TextActorIDs) > 0 {
			parts = append(parts, "actor_id IN ?")
			args = append(args, q.TextActorIDs)
		}
		if q.TextResourceID != nil {
			parts = append(parts, "resource_id = ?")
			args = append(args, *q.TextResourceID)
		}
		tx = tx.Where("("+strings.Join(parts, " OR ")+")", args...)
	}
	return tx
}
