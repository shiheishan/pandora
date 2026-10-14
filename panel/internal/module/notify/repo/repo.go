// Package repo 是 notify 模块唯一使用 GORM 的地方。
package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repo 是 notification_templates 与 notification_deliveries 的访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ListTemplates 按 code、channel 升序返回全部模板。
func (r *Repo) ListTemplates(ctx context.Context) ([]model.Template, error) {
	out := []model.Template{}
	if err := r.d.Conn(ctx).Order("code, channel").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetTemplate 读一个模板；不存在 ok 为 false。
func (r *Repo) GetTemplate(ctx context.Context, code, channel string) (model.Template, bool, error) {
	return r.getTemplate(r.d.Conn(ctx), code, channel)
}

// LockTemplate 同 GetTemplate，并加 FOR UPDATE。
func (r *Repo) LockTemplate(ctx context.Context, code, channel string) (model.Template, bool, error) {
	return r.getTemplate(r.d.Conn(ctx).Clauses(clause.Locking{Strength: "UPDATE"}), code, channel)
}

func (r *Repo) getTemplate(q *gorm.DB, code, channel string) (model.Template, bool, error) {
	var t model.Template
	res := q.Where("code = ? AND channel = ?", code, channel).Limit(1).Find(&t)
	if res.Error != nil {
		return model.Template{}, false, res.Error
	}
	return t, res.RowsAffected == 1, nil
}

// UpdateTemplateContent 改主题与正文、version 加 1，返回改后的行；行不存在返回 gorm.ErrRecordNotFound。
func (r *Repo) UpdateTemplateContent(ctx context.Context, id uuid.UUID, subject, body string) (model.Template, error) {
	var t model.Template
	res := r.d.Conn(ctx).Raw(
		`UPDATE notification_templates
		    SET subject = ?, body = ?, version = version + 1, updated_at = now()
		  WHERE id = ?
		RETURNING *`, subject, body, id).Scan(&t)
	if res.Error != nil {
		return model.Template{}, res.Error
	}
	if res.RowsAffected == 0 {
		return model.Template{}, gorm.ErrRecordNotFound
	}
	return t, nil
}
