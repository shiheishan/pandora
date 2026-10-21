package repo

// 订阅模板表（subscription_templates）的读取，属主 subscription；只剩拉取路径读全部模板正文。

import (
	"context"
)

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
