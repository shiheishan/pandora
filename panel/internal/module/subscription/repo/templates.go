package repo

// 订阅模板表（subscription_templates）的读写，属主 subscription。

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// TemplateSummary 是一种格式的模板概要（列表用，不带正文）。
type TemplateSummary struct {
	Format    string    `gorm:"column:format"`
	Bytes     int       `gorm:"column:bytes"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TemplateSummaries 读所有自定义模板的概要列表，按 format 升序。
func (r *Repo) TemplateSummaries(ctx context.Context) ([]TemplateSummary, error) {
	var list []TemplateSummary
	err := r.d.Conn(ctx).Raw("SELECT format, octet_length(content) AS bytes, updated_at FROM subscription_templates ORDER BY format").Scan(&list).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if list == nil {
		list = []TemplateSummary{}
	}
	return list, nil
}

// Template 读一种格式的自定义模板；lock 指定行锁。没有该格式的自定义模板时返回 false。
func (r *Repo) Template(ctx context.Context, format string, lock Lock) (model.SubscriptionTemplate, bool, error) {
	var tpl model.SubscriptionTemplate
	err := r.d.Conn(ctx).Raw(
		"SELECT format, content, updated_at FROM subscription_templates WHERE format = ?"+lock.suffix(),
		format,
	).Scan(&tpl).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.SubscriptionTemplate{}, false, nil
		}
		return model.SubscriptionTemplate{}, false, mapErr(err)
	}
	if tpl.Format == "" {
		return model.SubscriptionTemplate{}, false, nil
	}
	return tpl, true, nil
}

// UpsertTemplate 写入或覆盖一种格式的自定义模板。
func (r *Repo) UpsertTemplate(ctx context.Context, format, content string, at time.Time) error {
	err := r.d.Conn(ctx).Exec(
		`INSERT INTO subscription_templates (format, content, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (format) DO UPDATE SET content = EXCLUDED.content, updated_at = EXCLUDED.updated_at`,
		format, content, at,
	).Error
	return mapErr(err)
}

// DeleteTemplate 删除一种格式的自定义模板（恢复缺省）。
func (r *Repo) DeleteTemplate(ctx context.Context, format string) (bool, error) {
	res := r.d.Conn(ctx).Exec("DELETE FROM subscription_templates WHERE format = ?", format)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected > 0, nil
}

// TemplateContents 读出全部自定义模板的正文映射（format → content）。
// 拉取路径用，结果由 service 层进程内缓存，不是每次拉取都查。
func (r *Repo) TemplateContents(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		Format  string `gorm:"column:format"`
		Content string `gorm:"column:content"`
	}
	err := r.d.Conn(ctx).Raw("SELECT format, content FROM subscription_templates").Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Format] = row.Content
	}
	return out, nil
}
