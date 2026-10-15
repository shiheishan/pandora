package service

// 主题 branding 只管门户的标语与 Logo；站点名只认系统设置 site.name（门户标题、入口页 <title>、邮件都取它），主题里不再有站点名。
//
// Logo 只收 data:image/… 的 base64：门户的 CSP 是 img-src 'self' data:，
// 外链图片根本加载不出来；SVG 经 <img> 渲染时其中的脚本不会执行。

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// BrandingKeys 是 branding 允许的全部键。
var BrandingKeys = []string{"tagline", "logo"}

const (
	MaxTaglineRunes = 80
	// MaxLogoBytes 是 Logo 解码后的上限：它随每次 GET v1/appearance 下发，不能大。
	MaxLogoBytes = 48 * 1024
)

var logoMediaTypes = []string{"image/png", "image/jpeg", "image/webp", "image/svg+xml"}

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
			fields["branding"] = "不认识的品牌字段：" + k + "（只允许标语与 Logo）"
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
	if logo := str("logo"); logo != "" {
		if msg := checkLogo(logo); msg != "" {
			fields["branding.logo"] = msg
		} else {
			out["logo"] = logo
		}
	}
	if len(fields) > 0 {
		return nil, errs.Invalid(fields)
	}
	b, err := json.Marshal(out)
	return b, err
}

// checkLogo 返回空串表示合法，否则是给管理员看的原因。
func checkLogo(logo string) string {
	rest, ok := strings.CutPrefix(logo, "data:")
	if !ok {
		return "Logo 只能是上传的图片（data:image/… 形式），门户不加载外链图片"
	}
	mediaType, data, ok := strings.Cut(rest, ";base64,")
	if !ok || !slices.Contains(logoMediaTypes, mediaType) {
		return "Logo 只支持 PNG、JPEG、WebP 或 SVG"
	}
	if base64.StdEncoding.DecodedLen(len(data)) > MaxLogoBytes+3 {
		return "Logo 不能超过 48KB"
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 {
		return "Logo 的图片数据不完整"
	}
	if len(decoded) > MaxLogoBytes {
		return "Logo 不能超过 48KB"
	}
	return ""
}

// filterBranding 是门户读取时的防线：只放行标语与 Logo 两个已知键里的非空字符串，
// Logo 还得是 data:image/；库里旧行带的 site_name 也在这里丢掉。
func filterBranding(raw json.RawMessage) json.RawMessage {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	out := map[string]string{}
	for _, k := range BrandingKeys {
		s, ok := in[k].(string)
		if s = strings.TrimSpace(s); !ok || s == "" {
			continue
		}
		if k == "logo" && checkLogo(s) != "" {
			continue
		}
		out[k] = s
	}
	b, _ := json.Marshal(out)
	return b
}
