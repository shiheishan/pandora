package db

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	errTestDup     = errors.New("测试: 唯一约束冲突")
	errTestFK      = errors.New("测试: 外键约束冲突")
	errTestRestr   = errors.New("测试: 只追加表被改写")
	errTestNamedFK = errors.New("测试: 限定的套餐不存在")
)

var testConstraintErrs = ConstraintErrors{
	ByCode: map[string]error{
		CodeUnique:     errTestDup,
		CodeForeignKey: errTestFK,
		CodeRestrict:   errTestRestr,
	},
	ByName: map[string]error{"demo_items_plan_id_fkey": errTestNamedFK},
}

// gormWrap 模拟 GORM 的 TranslateError：用 %w 同时包住 GORM 哨兵与原 PgError。
func gormWrap(sentinel error, pg *pgconn.PgError) error {
	return fmt.Errorf("%w: %w", sentinel, pg)
}

func TestAsViolation(t *testing.T) {
	pg := &pgconn.PgError{Code: CodeUnique, ConstraintName: "demo_items_code_key", TableName: "demo_items"}
	for name, err := range map[string]error{
		"原始 PgError":  pg,
		"GORM 翻译后":    gormWrap(gorm.ErrDuplicatedKey, pg),
		"再包一层 %w 也取得到": fmt.Errorf("写入失败: %w", gormWrap(gorm.ErrDuplicatedKey, pg)),
	} {
		v, ok := AsViolation(err)
		if !ok {
			t.Fatalf("%s: 应取到约束违例", name)
		}
		want := Violation{Code: CodeUnique, Constraint: "demo_items_code_key", Table: "demo_items"}
		if v != want {
			t.Fatalf("%s: got %+v, want %+v", name, v, want)
		}
	}

	for name, err := range map[string]error{
		"nil":      nil,
		"普通错误":     errors.New("其它错误"),
		"非约束类 SQLSTATE": &pgconn.PgError{Code: "40P01"},
		"只有 GORM 哨兵":   gorm.ErrDuplicatedKey,
	} {
		if _, ok := AsViolation(err); ok {
			t.Fatalf("%s: 不应判为约束违例", name)
		}
	}
}

func TestConstraintErrorsMap(t *testing.T) {
	if testConstraintErrs.Map(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
	other := errors.New("其它错误")
	if got := testConstraintErrs.Map(other); got != other {
		t.Fatalf("非约束错误应原样返回: %v", got)
	}
	check := &pgconn.PgError{Code: CodeCheck}
	if got := testConstraintErrs.Map(check); got != error(check) {
		t.Fatalf("表里没有的 SQLSTATE 应原样返回: %v", got)
	}

	cases := []struct {
		name    string
		err     error
		want    error
		notWant error
	}{
		{"原始 PgError 按码", &pgconn.PgError{Code: CodeUnique}, errTestDup, nil},
		{"GORM 翻译后按码", gormWrap(gorm.ErrDuplicatedKey, &pgconn.PgError{Code: CodeUnique}), errTestDup, nil},
		{"23001 按码", &pgconn.PgError{Code: CodeRestrict}, errTestRestr, nil},
		{"约束名优先于码", &pgconn.PgError{Code: CodeForeignKey, ConstraintName: "demo_items_plan_id_fkey"}, errTestNamedFK, errTestFK},
		{"GORM 翻译后约束名仍优先", gormWrap(gorm.ErrForeignKeyViolated,
			&pgconn.PgError{Code: CodeForeignKey, ConstraintName: "demo_items_plan_id_fkey"}), errTestNamedFK, errTestFK},
		{"约束名不在表里退回按码", &pgconn.PgError{Code: CodeForeignKey, ConstraintName: "demo_items_group_id_fkey"}, errTestFK, errTestNamedFK},
		{"只有 GORM 哨兵也按码", gorm.ErrForeignKeyViolated, errTestFK, nil},
	}
	for _, c := range cases {
		got := testConstraintErrs.Map(c.err)
		if !errors.Is(got, c.want) {
			t.Fatalf("%s: got %v, want errors.Is %v", c.name, got, c.want)
		}
		if !errors.Is(got, c.err) {
			t.Fatalf("%s: 翻译后应仍包着原错误", c.name)
		}
		if c.notWant != nil && errors.Is(got, c.notWant) {
			t.Fatalf("%s: 不应判为 %v", c.name, c.notWant)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	for _, code := range []string{"40P01", "40001"} {
		if !IsRetryable(fmt.Errorf("提交: %w", &pgconn.PgError{Code: code})) {
			t.Fatalf("%s 应可整笔重试", code)
		}
	}
	for name, err := range map[string]error{
		"nil":   nil,
		"唯一冲突":  &pgconn.PgError{Code: CodeUnique},
		"普通错误":  errors.New("其它错误"),
	} {
		if IsRetryable(err) {
			t.Fatalf("%s: 不应可重试", name)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"abc":       "abc",
		"50%":       `50\%`,
		"a_b":       `a\_b`,
		`C:\dir`:    `C:\\dir`,
		`中文%_\混合`:   `中文\%\_\\混合`,
		`\%_`:       `\\\%\_`,
	}
	for in, want := range cases {
		if got := EscapeLike(in); got != want {
			t.Fatalf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ContainsPattern("a_b"); got != `%a\_b%` {
		t.Fatalf("ContainsPattern = %q", got)
	}
}
