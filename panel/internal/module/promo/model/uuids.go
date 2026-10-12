// uuid[] 列的 PostgreSQL 文本数组编解码（如 {uuid1,uuid2}）。
package model

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// UUIDs 是 coupons.applicable_plan_ids 等 uuid[] 列在 Go 里的形态。
type UUIDs []uuid.UUID

// Scan 从 PG 数组文本（{} 或 {a,b}）解码。
func (u *UUIDs) Scan(src any) error {
	if src == nil {
		*u = UUIDs{}
		return nil
	}
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("promo.UUIDs: 不支持的类型 %T", src)
	}
	text = strings.TrimSpace(text)
	if text == "" || text == "{}" {
		*u = UUIDs{}
		return nil
	}
	if len(text) < 2 || text[0] != '{' || text[len(text)-1] != '}' {
		return fmt.Errorf("promo.UUIDs: 非法数组文本 %q", text)
	}
	inner := strings.TrimSpace(text[1 : len(text)-1])
	if inner == "" {
		*u = UUIDs{}
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make(UUIDs, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		id, err := uuid.Parse(p)
		if err != nil {
			return fmt.Errorf("promo.UUIDs: 解析 %q: %w", p, err)
		}
		out = append(out, id)
	}
	*u = out
	return nil
}

// Value 编码为 PG 数组文本。
func (u UUIDs) Value() (driver.Value, error) {
	if len(u) == 0 {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range u {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String(), nil
}
