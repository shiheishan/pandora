// Package repo 是 support 模块唯一使用 GORM 的数据访问层：工单、工单消息、快捷回复。
package repo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 support 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

var (
	// ErrDuplicate 表示唯一约束冲突。
	ErrDuplicate = errors.New("support: 唯一约束冲突")
	// ErrForeignKey 表示外键约束冲突。
	ErrForeignKey = errors.New("support: 外键约束冲突")
	// ErrCheck 表示 CHECK 约束不满足。
	ErrCheck = errors.New("support: CHECK 约束不满足")
	// ErrAppendOnly 表示只追加或禁删的表被改写（触发器的 restrict_violation 23001）。
	ErrAppendOnly = errors.New("support: 只追加或禁删的表被改写")
)

// mapErr 把 GORM 翻译后的约束错误与触发器的 23001 转成模块哨兵，并包住原错误。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		return fmt.Errorf("%w: %v", ErrForeignKey, err)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: %v", ErrCheck, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23001":
			return fmt.Errorf("%w: %v", ErrAppendOnly, err)
		case "23505":
			return fmt.Errorf("%w: %v", ErrDuplicate, err)
		case "23503":
			return fmt.Errorf("%w: %v", ErrForeignKey, err)
		case "23514":
			return fmt.Errorf("%w: %v", ErrCheck, err)
		}
	}
	return err
}

// now 返回写库用的当前时刻（UTC、截到微秒）。
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// likePattern 把用户输入转成 ILIKE 的包含模式：先转义 \ % _，再两头加 %。
func likePattern(q string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range q {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// TicketRow 是列表读出的一行：工单本身加消息统计。
type TicketRow struct {
	model.Ticket
	// MessageCount 是消息条数（门户只数非内部备注，后台数全部）
	MessageCount int `gorm:"column:message_count"`
	// LastReplyAt 是最后一条（同上口径）消息的时间，没有消息时为建单时间
	LastReplyAt time.Time `gorm:"column:last_reply_at"`
	// LastAuthorKind 是最后一条非内部备注消息的作者类型，没有时为空串（只后台队列填）
	LastAuthorKind string `gorm:"column:last_author_kind"`
}

// QueueFilter 是后台队列的筛选。零值字段不参与筛选。
type QueueFilter struct {
	// Statuses 非空时只取这些状态
	Statuses []string
	Priority string
	Category string
	// AssignedTo 非 nil 时只取指派给他的
	AssignedTo *uuid.UUID
	// Query 非空时按工单号、标题包含匹配，或提单人在 UserIDs 里
	Query   string
	UserIDs []uuid.UUID
	// OnlyBreached 只取首次响应已超时且未响应、未解决未关闭的（以 Now 为准）
	OnlyBreached bool
	Now          time.Time
	Limit        int
	Offset       int
}

// Escalated 是 SLA 升级里被改动的一张工单。
type Escalated struct {
	ID             uuid.UUID `gorm:"column:id"`
	UserID         uuid.UUID `gorm:"column:user_id"`
	BeforeStatus   string    `gorm:"column:before_status"`
	BeforePriority string    `gorm:"column:before_priority"`
	Priority       string    `gorm:"column:priority"`
}
