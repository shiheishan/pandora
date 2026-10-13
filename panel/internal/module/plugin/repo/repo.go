// Package repo 是 plugin 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 plugin 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突。
var ErrDuplicate = errors.New("plugin: 唯一约束冲突")

// mapErr 把 GORM 翻译后的约束错误转成模块哨兵。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}

const maxErrorRunes = 500

// truncateRunes 按字符截断到 n。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// HookStat 是一个钩子的投递计数。
type HookStat struct {
	HookID     uuid.UUID  `gorm:"column:hook_id"`
	Queued     int64      `gorm:"column:queued"`
	Failed     int64      `gorm:"column:failed"`
	Sent       int64      `gorm:"column:sent"`
	LastSentAt *time.Time `gorm:"column:last_sent_at"`
}
