package db

// 约束违例与 LIKE 转义：全面板只有这一份。repo 不直接 import pgconn 判 SQLSTATE，
// 一律经 ConstraintErrors 把约束违例翻译成本模块的哨兵错误。

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// SQLSTATE 里常用的约束违例码
const (
	CodeUnique     = "23505"
	CodeForeignKey = "23503"
	CodeCheck      = "23514"
	CodeNotNull    = "23502"
	CodeRestrict   = "23001" // 触发器拒绝（只追加表、不可改字段），以及 ON DELETE RESTRICT 的外键
	CodeExclusion  = "23P01"
)

// 整笔事务已被回滚、可以整笔重来的 SQLSTATE
const (
	codeDeadlock      = "40P01"
	codeSerialization = "40001"
)

// Violation 是从错误链里取出的 PG 约束违例。
type Violation struct{ Code, Constraint, Table string }

// AsViolation 从 err 的错误链里取 *pgconn.PgError；不是约束违例（code 不以 23 开头）时 ok=false。
// GORM 开了 TranslateError，翻译后的错误仍用 %w 包着原 PgError，所以 errors.As 取得到。
func AsViolation(err error) (Violation, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !strings.HasPrefix(pgErr.Code, "23") {
		return Violation{}, false
	}
	return Violation{Code: pgErr.Code, Constraint: pgErr.ConstraintName, Table: pgErr.TableName}, true
}

// IsRetryable 判断是不是死锁（40P01）或序列化失败（40001）：整个事务已回滚，整笔重来是安全的。
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == codeDeadlock || pgErr.Code == codeSerialization)
}

// ConstraintErrors 把约束违例翻译成模块哨兵：先按约束名（ByName），再按 SQLSTATE（ByCode）；都没命中原样返回 err。
// 命中时返回 fmt.Errorf("%w: %w", 哨兵, err)，errors.Is 同时认得哨兵与原错误。
// 约束名在全库唯一（迁移里起名时保证），所以按名命中就用，不再看码；加约束时别起和别处重名的名字。
type ConstraintErrors struct {
	ByCode map[string]error
	ByName map[string]error
}

// Map 翻译 err；nil 返回 nil。
func (m ConstraintErrors) Map(err error) error {
	if err == nil {
		return nil
	}
	v, ok := violationOf(err)
	if !ok {
		return err
	}
	if v.Constraint != "" {
		if sentinel, hit := m.ByName[v.Constraint]; hit {
			return fmt.Errorf("%w: %w", sentinel, err)
		}
	}
	if sentinel, hit := m.ByCode[v.Code]; hit {
		return fmt.Errorf("%w: %w", sentinel, err)
	}
	return err
}

// violationOf 先取错误链里的 PgError；取不到时退回 GORM 的翻译哨兵（只有 SQLSTATE、没有约束名）。
// pgx 驱动下 GORM 的哨兵总包着 PgError，后一种只在错误被别处重新构造时出现，保留它让语义与原来逐模块的判断一致。
func violationOf(err error) (Violation, bool) {
	if v, ok := AsViolation(err); ok {
		return v, true
	}
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return Violation{Code: CodeUnique}, true
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		return Violation{Code: CodeForeignKey}, true
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return Violation{Code: CodeCheck}, true
	}
	return Violation{}, false
}

// EscapeLike 转义 LIKE / ILIKE 模式里的 \ % _（PG 缺省的转义字符就是 \，SQL 里也写明 ESCAPE '\'）。
func EscapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ContainsPattern 返回 "%"+EscapeLike(s)+"%"：把用户输入转成 ILIKE 的包含模式。
func ContainsPattern(s string) string {
	return "%" + EscapeLike(s) + "%"
}
