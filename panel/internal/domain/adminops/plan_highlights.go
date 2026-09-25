// [INPUT]: 依赖 platform/httpx 的 Invalid 字段错误
// [OUTPUT]: 包内提供 normalizeHighlights 与 withHighlightFields：卖点列表的规整、逐条校验与和资料校验错误的合并
// [POS]: adminops 套餐目录的卖点规则（R100）唯一出处，catalog.go 的新建 / 改资料与两个向导共用；数据库 00088 只兜条数与 NULL
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	maxPlanHighlights     = 5
	maxPlanHighlightRunes = 40
)

// normalizeHighlights 去掉每条的首尾空白，按给定顺序返回；逐条问题记在
// highlights.{i}，条数超限记在 highlights。返回值永不为 nil：pgx 会把 nil
// 切片写成 NULL，撞上列的 NOT NULL。
func normalizeHighlights(raw []string) ([]string, map[string]string) {
	out := make([]string, 0, len(raw))
	fields := map[string]string{}
	if len(raw) > maxPlanHighlights {
		fields["highlights"] = fmt.Sprintf("最多 %d 条卖点", maxPlanHighlights)
	}
	seen := map[string]bool{}
	for i, item := range raw {
		item = strings.TrimSpace(item)
		key := fmt.Sprintf("highlights.%d", i)
		switch n := len([]rune(item)); {
		case n == 0:
			fields[key] = "卖点不能为空"
		case n > maxPlanHighlightRunes:
			fields[key] = fmt.Sprintf("每条卖点最多 %d 个字", maxPlanHighlightRunes)
		case seen[item]:
			fields[key] = "卖点不能重复"
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, fields
}

// withHighlightFields 规整 *highlights，并把它的字段错误并进资料校验的结果：
// 一次 422 把两边的问题都标出来，不让管理员改一处、提交、再发现另一处。
func withHighlightFields(profileErr error, highlights *[]string) error {
	clean, fields := normalizeHighlights(*highlights)
	*highlights = clean
	if len(fields) == 0 {
		return profileErr
	}
	if profileErr == nil {
		return httpx.Invalid(fields)
	}
	var he *httpx.Error
	if errors.As(profileErr, &he) && he.Code == httpx.CodeValidationFailed {
		merged := map[string]string{}
		for k, v := range he.Fields {
			merged[k] = v
		}
		for k, v := range fields {
			merged[k] = v
		}
		return httpx.Invalid(merged)
	}
	return profileErr
}
