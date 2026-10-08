package subscription

// 订阅响应的 Content-Disposition。
//
// Clash Verge、Mihomo Party、FlClash、sing-box 系客户端都拿它的文件名当配置名；
// 没有这个头时配置名显示成订阅 URL（一长串随机令牌），用户分不清哪条是哪家。
// 文件名是完整的配置名 ProfileName（label.go）：「站点名 · 备注名」或「站点名 · 套餐名」，
// 站点名取生效主题的 branding.site_name（与门户标题、邮件同一来源）。

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// siteNameCacheTTL：站点名只在后台换主题时变，订阅拉取是热路径，不为它每次查库。
// 换主题后最多这么久生效。
const siteNameCacheTTL = time.Minute

// siteNames 按租户缓存站点名。放在包级而不是 Service 上：它与 Service 的其余
// 状态无关，进程内一份即可；条目有上限、有 TTL（prefixCache 的规矩）。
var siteNames = newPrefixCache(siteNameCacheTTL)

// SiteName 返回租户的站点名，用于订阅文件名。读库失败时回落到默认站点名，
// 绝不因为一个展示用的名字让订阅拉取失败；失败不缓存，下次重试。
func (s *Service) SiteName(ctx context.Context, tenantID string) string {
	if name, ok := siteNames.get(tenantID); ok {
		return name
	}
	name := appearance.DefaultSiteName
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		name, err = appearance.SiteNameTx(ctx, tx, tenantID)
		return err
	})
	if err != nil {
		return appearance.DefaultSiteName
	}
	siteNames.put(tenantID, name)
	return name
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
		name = appearance.DefaultSiteName
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
