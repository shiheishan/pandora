package service

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// MaxLabelRunes 是备注名的最大字数，与迁移 00011 的 CHECK 一致。
const MaxLabelRunes = 16

// 备注名校验失败的文案，直接放进表单错误。
const (
	msgLabelTooLong = "名字最多 16 个字"
	msgLabelInvalid = "名字里不能有换行、制表符之类的特殊字符"
)

// defaultSiteName 是站点名没配时配置名里用的名字。
const defaultSiteName = "Pandora"

// profileNameSeparator 是配置名里站点名与订阅名的分隔符。
const profileNameSeparator = " · "

// NormalizeLabel 规范化订阅备注名：去首尾空白；空串表示不起名；最多 16 个字；
// 不含控制字符与不可见的格式字符（会进 Content-Disposition 头，也会显示在 App 里）。允许零宽连接符 U+200D（组合表情要用）。
func NormalizeLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !utf8.ValidString(s) {
		return "", errs.Invalid(map[string]string{"label": msgLabelInvalid})
	}
	if utf8.RuneCountInString(s) > MaxLabelRunes {
		return "", errs.Invalid(map[string]string{"label": msgLabelTooLong})
	}
	for _, r := range s {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200d') || r == '\u2028' || r == '\u2029' {
			return "", errs.Invalid(map[string]string{"label": msgLabelInvalid})
		}
	}
	return s, nil
}

// LabelWithSuffix 给重名的备注名加序号：n<=1 原样；否则截断到总长不超过 16 个字、去掉尾部空格后加「 n」。
func LabelWithSuffix(label string, n int) string {
	if n <= 1 {
		return label
	}
	suffix := " " + strconv.Itoa(n)
	runes := []rune(label)
	if keep := MaxLabelRunes - utf8.RuneCountInString(suffix); len(runes) > keep {
		runes = runes[:keep]
	}
	return strings.TrimRight(string(runes), " ") + suffix
}

// ProfileName 是订阅在 App 里显示的配置名：「站点名 · 备注名」，没起名时「站点名 · 套餐名」，都没有时只有站点名。
func ProfileName(site, label, planName string) string {
	site = strings.TrimSpace(site)
	if site == "" {
		site = defaultSiteName
	}
	name := strings.TrimSpace(label)
	if name == "" {
		name = strings.TrimSpace(planName)
	}
	if name == "" {
		return site
	}
	return site + profileNameSeparator + name
}
