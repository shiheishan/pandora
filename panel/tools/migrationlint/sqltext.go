package migrationlint

import (
	"regexp"
	"strings"
)

// executableSQL 把一段迁移 SQL 变成「迁移执行时真正会跑的语句文本」，供规则做正则匹配：
//   - 去掉 -- 与 /* */ 注释（块注释可嵌套）；
//   - 字符串字面量只留一对空引号，注释文案、COMMENT ON 里的字不会被误当成 DDL；
//   - 双引号标识符去掉引号；
//   - 美元引用体：DO 块的体在迁移时就执行，保留并递归处理；函数、过程的体只是定义，
//     迁移时不执行，整段清空。
//
// 动态 SQL（EXECUTE format('...')）是字符串，看不见；这是有意的取舍，见规则文件。
func executableSQL(text string) string {
	var b strings.Builder
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case c == '-' && strings.HasPrefix(text[i:], "--"):
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				return b.String()
			}
			i += end
		case c == '/' && strings.HasPrefix(text[i:], "/*"):
			i = skipBlockComment(text, i)
			b.WriteByte(' ')
		case c == '\'':
			escapes := i > 0 && (text[i-1] == 'E' || text[i-1] == 'e') && (i < 2 || !isIdentByte(text[i-2]))
			i = skipQuoted(text, i, escapes)
			b.WriteString("''")
		case c == '"':
			end := strings.IndexByte(text[i+1:], '"')
			if end < 0 {
				return b.String()
			}
			b.WriteString(text[i+1 : i+1+end])
			i += end + 2
		case c == '$':
			tag, ok := dollarTag(text, i)
			if !ok || (i > 0 && isIdentByte(text[i-1])) {
				b.WriteByte(c)
				i++
				continue
			}
			bodyStart := i + len(tag)
			end := strings.Index(text[bodyStart:], tag)
			if end < 0 {
				return b.String()
			}
			body := text[bodyStart : bodyStart+end]
			isDo := precededByDo(b.String())
			b.WriteString(" $$ ")
			if isDo {
				b.WriteString(executableSQL(body))
			}
			b.WriteString(" $$ ")
			i = bodyStart + end + len(tag)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func skipBlockComment(text string, i int) int {
	depth := 0
	for i < len(text) {
		switch {
		case strings.HasPrefix(text[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(text[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return i
}

func skipQuoted(text string, i int, backslashEscapes bool) int {
	i++ // 起始引号
	for i < len(text) {
		switch {
		case backslashEscapes && text[i] == '\\':
			i += 2
		case text[i] == '\'' && i+1 < len(text) && text[i+1] == '\'':
			i += 2
		case text[i] == '\'':
			return i + 1
		default:
			i++
		}
	}
	return i
}

var dollarTagRe = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

func dollarTag(text string, i int) (string, bool) {
	m := dollarTagRe.FindString(text[i:])
	return m, m != ""
}

var doTailRe = regexp.MustCompile(`(?is)\bDO(\s+LANGUAGE\s+\w+)?\s*$`)

func precededByDo(prefix string) bool {
	return doTailRe.MatchString(prefix)
}

var spaceRe = regexp.MustCompile(`\s+`)

// statements 把可执行文本按分号切开，压缩空白、去掉空句。DO 块体里的分号同样会切，
// 规则按片段匹配，这正好让块里的 UPDATE、CREATE INDEX 各自成句。
func statements(section string) []string {
	var out []string
	for _, part := range strings.Split(executableSQL(section), ";") {
		s := strings.TrimSpace(spaceRe.ReplaceAllString(part, " "))
		if s != "" && s != "$$" {
			out = append(out, s)
		}
	}
	return out
}
