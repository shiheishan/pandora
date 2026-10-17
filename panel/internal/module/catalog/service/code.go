package service

// 套餐代码由名称自动生成（新建时不让人填，之后也不改），与设计稿「后台 · 套餐」的 autoCode 同一套规则：
// 名称里的常见中文词按词表换成英文（长词优先），其余字符里只留小写字母与数字、别的一律当分隔符，
// 连续分隔符并成一个连字符、去掉首尾，截到 32 个字符；什么都不剩时用 plan，只剩 1 个字符时前面加 plan-。
// 已被占用时依次加 -2、-3……

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// codeWords 是设计稿的中文词表。
var codeWords = map[string]string{
	"标准": "standard", "专业": "pro", "高级": "premium", "家庭": "family", "体验": "trial", "试用": "trial",
	"企业": "enterprise", "定制": "custom", "基础": "basic", "旗舰": "ultimate", "年": "yearly", "季": "quarterly",
	"月": "monthly", "旧": "legacy", "新": "new", "学生": "student", "团队": "team", "个人": "personal",
}

// codeWordOrder 是词表的替换顺序：长词优先，同长按字典序（结果与顺序无关时也保持确定）。
var codeWordOrder = func() []string {
	keys := make([]string, 0, len(codeWords))
	for k := range codeWords {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		li, lj := len([]rune(keys[i])), len([]rune(keys[j]))
		if li != lj {
			return li > lj
		}
		return keys[i] < keys[j]
	})
	return keys
}()

// maxCodeBase 是代码主体（不含重名后缀）的最大长度。
const maxCodeBase = 32

// autoCodeBase 按名称算出代码主体（未查重）。结果满足 ^[a-z0-9][a-z0-9-]{1,31}$。
func autoCodeBase(name string) string {
	s := strings.TrimSpace(name)
	for _, k := range codeWordOrder {
		s = strings.ReplaceAll(s, k, " "+codeWords[k]+" ")
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLower(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	base := b.String()
	if len(base) > maxCodeBase {
		base = strings.TrimRight(base[:maxCodeBase], "-")
	}
	switch len(base) {
	case 0:
		return "plan"
	case 1:
		return "plan-" + base
	}
	return base
}

// pickCode 在已占用的代码里给 base 找一个空位：base 本身没被占用就用它，否则依次试 base-2、base-3……
func pickCode(base string, taken []string) string {
	used := make(map[string]bool, len(taken))
	for _, c := range taken {
		used[c] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		c := base + "-" + strconv.Itoa(i)
		if !used[c] {
			return c
		}
	}
}

// nextPlanCode 在事务里按名称生成一个没被占用的套餐代码。并发建同名套餐撞唯一约束时整笔回滚，由管理员重试。
func (s *Service) nextPlanCode(ctx context.Context, name string) (string, error) {
	base := autoCodeBase(name)
	taken, err := s.repo.CodesWithPrefix(ctx, base)
	if err != nil {
		return "", err
	}
	return pickCode(base, taken), nil
}
