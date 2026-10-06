// [INPUT]: 依赖 encoding/base64、encoding/json，依赖 platform/httpx 的校验错误
// [OUTPUT]: 对外提供 BrandingKeys、MaxSiteNameRunes、MaxTaglineRunes、MaxLogoBytes；包内提供 normalizeBranding（保存校验）与 filterBranding（门户读取过滤）
// [POS]: domain/appearance 的站点品牌规则：主题的 branding 只有站点名、标语、Logo 三个键，service.go 的保存与门户读取都经过这里；与 tokens.go 的令牌白名单是同一种「存前严格校验、读时再过滤」的两道防线

package appearance

// 站点名的唯一来源是生效主题的 branding.site_name：门户标题、邮件 {{site}}
// 与发件人名都从这里取（SiteNameTx），切换主题就是换站点名。所以保存时站点名
// 必填 —— 一套没有站点名的主题被激活，邮件会悄悄落回 DefaultSiteName。
//
// Logo 只收 data:image/… 的 base64：门户的 CSP 是 img-src 'self' data:，
// 外链图片根本加载不出来；SVG 经 <img> 渲染时其中的脚本不会执行。

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// BrandingKeys 是 branding 允许的全部键。
var BrandingKeys = []string{"site_name", "tagline", "logo"}

const (
	MaxSiteNameRunes = 40
	MaxTaglineRunes  = 80
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
		return nil, httpx.Invalid(map[string]string{"branding": "必须是 JSON 对象"})
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
		if !slices.Contains(BrandingKeys, k) {
			fields["branding"] = "不认识的品牌字段：" + k + "（只允许站点名称、标语与 Logo）"
		}
	}
	out := map[string]string{}
	switch name := str("site_name"); {
	case name == "":
		fields["branding.site_name"] = "站点名称必填：门户标题、邮件里的站点名与发件人名都取自生效主题"
	case utf8.RuneCountInString(name) > MaxSiteNameRunes:
		fields["branding.site_name"] = "站点名称最多 40 个字"
	default:
		out["site_name"] = name
	}
	if tag := str("tagline"); utf8.RuneCountInString(tag) > MaxTaglineRunes {
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
		return nil, httpx.Invalid(fields)
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

// filterBranding 是门户读取时的防线：不论库里存了什么（迁移前的主题、
// 手工改过的行），只放行三个已知键里的非空字符串，Logo 还得是 data:image/。
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
