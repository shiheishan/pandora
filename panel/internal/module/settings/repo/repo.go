// Package repo 是 settings 模块唯一使用 GORM 的地方。
package repo

import (
	"context"
	"encoding/json"

	"github.com/aegispanel/aegis/internal/module/settings/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/google/uuid"
)

// Repo 是 settings 表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// kvRow 是 settings 键值查询的一行（value 用 ::text 避免驱动缓冲区被别名引用）。
type kvRow struct {
	Key   string
	Value string
}

// Values 读给定键；缺行的键不在结果里。
func (r *Repo) Values(ctx context.Context, keys []string) (map[string]json.RawMessage, error) {
	return r.values(ctx, keys, false)
}

// LockValues 同 Values，并对已存在的行按 key 顺序加 FOR UPDATE。
func (r *Repo) LockValues(ctx context.Context, keys []string) (map[string]json.RawMessage, error) {
	return r.values(ctx, keys, true)
}

func (r *Repo) values(ctx context.Context, keys []string, lock bool) (map[string]json.RawMessage, error) {
	if len(keys) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	sql := "SELECT key, value::text AS value FROM settings WHERE key IN ?"
	if lock {
		sql += " ORDER BY key FOR UPDATE"
	}
	var rows []kvRow
	if err := r.d.Conn(ctx).Raw(sql, keys).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		out[row.Key] = json.RawMessage(row.Value)
	}
	return out, nil
}

// Upsert 写一个键（插入或覆盖值）。
func (r *Repo) Upsert(ctx context.Context, key string, value json.RawMessage, by *uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"INSERT INTO settings (key, value, updated_by) VALUES (?, ?::jsonb, ?) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()",
		key, string(value), by,
	).Error
}

// InsertIfAbsent 只在键不存在时写入，返回是否写入（已存在就不动它）。
func (r *Repo) InsertIfAbsent(ctx context.Context, key string, value json.RawMessage) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?::jsonb) ON CONFLICT (key) DO NOTHING",
		key, string(value),
	)
	return res.RowsAffected == 1, res.Error
}

// Delete 删除给定键；不存在不算错。
func (r *Repo) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return r.d.Conn(ctx).Where("key IN ?", keys).Delete(&model.Setting{}).Error
}
