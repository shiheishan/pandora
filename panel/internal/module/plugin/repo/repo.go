// Package repo 是 plugin 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 plugin 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突。
var ErrDuplicate = errors.New("plugin: 唯一约束冲突")

// constraintErrs 把约束违例翻译成本模块哨兵（统一做法见 internal/pkg/db/constraint.go）。
var constraintErrs = db.ConstraintErrors{ByCode: map[string]error{
	db.CodeUnique: ErrDuplicate,
}}

// mapErr 把约束违例翻译成本模块哨兵，并包住原错误。
func mapErr(err error) error { return constraintErrs.Map(err) }

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
