package purchase

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLabelRunes 是备注名的最大字数，与迁移 00135 的 CHECK 一致。
const MaxLabelRunes = 16

// 备注名校验失败的原因，文案可以直接放进表单错误。
var (
	ErrLabelTooLong = errors.New("名字最多 16 个字")
	ErrLabelInvalid = errors.New("名字里不能有换行、制表符之类的特殊字符")
)

// NormalizeLabel 规范化订阅备注名：去首尾空白；空串表示不起名；1–16 个字；
// 不含控制字符与不可见的格式字符（它会进 Content-Disposition 头，也会显示在 App 里）。
//
// 允许零宽连接符 U+200D：组合表情（👨‍👩‍👧）要用它。
func NormalizeLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !utf8.ValidString(s) {
		return "", ErrLabelInvalid
	}
	if utf8.RuneCountInString(s) > MaxLabelRunes {
		return "", ErrLabelTooLong
	}
	for _, r := range s {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '‍') {
			return "", ErrLabelInvalid
		}
		// 行分隔符、段分隔符不算控制字符，但同样会把头部或界面折行
		if r == ' ' || r == ' ' {
			return "", ErrLabelInvalid
		}
	}
	return s, nil
}
