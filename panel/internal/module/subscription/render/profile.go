package render

// 订阅在 App 里的配置名与响应的 Content-Disposition。
//
// Clash Verge、Mihomo Party、FlClash、sing-box 系客户端都拿 Content-Disposition 的文件名当配置名；
// 没有这个头时配置名显示成订阅 URL（一长串随机令牌），用户分不清哪条是哪家。
// 文件名是完整的配置名 ProfileName：「站点名 · 备注名」或「站点名 · 套餐名」。
// 门户订阅列表的 client_name 也只经 ProfileName 拼，页面上写的「App 里显示为：…」
// 与客户端真正显示的一致。

import "strings"

// defaultSiteName 是站点名没配时的回落（与 settings 的缺省站点名一致）。
const defaultSiteName = "Pandora"

// profileNameSeparator 是站点名与备注名（或套餐名）之间的分隔。
const profileNameSeparator = " · "

// ProfileName 是订阅在 App 里显示的配置名，全站唯一来源：「站点名 · 备注名」，
// 没起名时「站点名 · 套餐名」，套餐名也取不到时只有站点名。
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

// ContentDisposition 生成订阅响应的 Content-Disposition 头，入参是完整的配置名
// （ProfileName 的结果）。
//
// 按 RFC 6266：filename 给一个只含安全 ASCII 的回退名（老客户端只认它），
// filename* 给 UTF-8 百分号编码的原名（中文站点名与备注名靠它）。两者都经过白名单
// 编码，名字里的引号、分号、换行一律进不了头部。
func ContentDisposition(profileName string) string {
	name := strings.TrimSpace(profileName)
	if name == "" {
		name = defaultSiteName
	}
	fallback := asciiFilename(name)
	if fallback == "" {
		fallback = "subscription"
	}
	return `attachment; filename="` + fallback + `"; filename*=UTF-8''` + rfc5987Encode(name)
}

// asciiFilename 只保留字母、数字、空格与 . _ -，其余丢掉；连续空白并成一个（「站点 · 名字」
// 去掉中点后不留双空格），首尾空白去掉。
func asciiFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// rfc5987Encode 按 RFC 5987 的 attr-char 编码：字母数字与 !#$&+-.^_`|~ 原样，
// 其余字节一律 %XX。
func rfc5987Encode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
