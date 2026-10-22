package service

// 主题 branding 只管门户的标语；站点名只认系统设置 site.name；Logo 10-21 删了（后台没有上传入口）。

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// BrandingKeys 是 branding 允许的全部键。
var BrandingKeys = []string{"tagline"}

const MaxTaglineRunes = 80

// normalizeBranding 校验管理员提交的 branding，返回只含已知键的规范 JSON。
// 错误逐键标在 branding.<键> 上，后台据此把提示挂到对应输入框。
func normalizeBranding(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return nil, errs.Invalid(map[string]string{"branding": "必须是 JSON 对象"})
	}
	fields := map[string]string{}
	str := func(k string) string {
		v, ok := in[k]
		if !ok || v == nil {
			return ""
		}
		s, ok := v.(string)
		if !ok {
			fields["branding."+k] = "必须是字符串"
		}
		return strings.TrimSpace(s)
	}
	for k := range in {
		if k == "site_name" {
			fields["branding.site_name"] = "站点名不在主题里设置：到「内容与外观 → 主题与插槽」的站点设置里改"
		} else if !slices.Contains(BrandingKeys, k) {
			fields["branding"] = "不认识的品牌字段：" + k + "（只允许标语）"
		}
	}
	out := map[string]string{}
	if tag := str("tagline"); strings.ContainsRune(tag, 0) {
		fields["branding.tagline"] = "不能包含空字符"
	} else if utf8.RuneCountInString(tag) > MaxTaglineRunes {
		fields["branding.tagline"] = "标语最多 80 个字"
	} else if tag != "" {
		out["tagline"] = tag
	}
	if len(fields) > 0 {
		return nil, errs.Invalid(fields)
	}
	b, err := json.Marshal(out)
	return b, err
}

// filterBranding 是门户读取时的防线：只放行标语这一个已知键的非空字符串，
// 库里旧行带的 site_name、logo 都在这里丢掉（Logo 10-21 删了）。
func filterBranding(raw json.RawMessage) json.RawMessage {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	out := map[string]string{}
	for _, k := range BrandingKeys {
		s, ok := in[k].(string)
		if s = strings.TrimSpace(s); !ok || s == "" {
			continue
		}
		out[k] = s
	}
	b, _ := json.Marshal(out)
	return b
}
