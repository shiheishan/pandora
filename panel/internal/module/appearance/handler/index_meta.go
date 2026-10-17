package handler

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/aegispanel/aegis/internal/module/appearance/service"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
)

// Page 区分两个入口页：标题写法不同。
type Page int

const (
	PagePortal Page = iota // 门户：<title> 就是站点名（与门户前端 document.title 一致）
	PageAdmin              // 后台：<title> 是「站点名 控制台」（与后台前端的「… · 站点名 控制台」一致）
)

// PageMetaSource 是入口页要的外观快照；*service.Service 满足。
type PageMetaSource interface {
	PageMeta(ctx context.Context) service.PageMeta
}

var _ PageMetaSource = (*service.Service)(nil)

// defaultIconSVG 是设计稿 pandora 字标里的环形标记（同 frontend/src/shell/Logo.tsx 的 SVG），颜色取默认主题的正文色与品牌色。
const defaultIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><circle cx="24" cy="24" r="15" transform="rotate(-15 24 24)" fill="none" stroke="#1c1c1f" stroke-width="8" stroke-dasharray="78.5 15.7"/><circle cx="38.1" cy="9.9" r="4" fill="#b9442b"/></svg>`

// DefaultIcon 是内置默认图标的 data URL。
var DefaultIcon = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(defaultIconSVG))

// IndexMeta 把外观快照转成入口页 <head> 的改写内容，供装配处传给 webapp.WithIndexMeta。
// 站点名取系统设置，描述用主题标语（没有就用站点名），图标用主题 Logo（没有就用内置默认图标）。
func IndexMeta(src PageMetaSource, page Page) func(ctx context.Context) webapp.IndexMeta {
	return func(ctx context.Context) webapp.IndexMeta {
		m := src.PageMeta(ctx)
		site := strings.TrimSpace(m.SiteName)
		if site == "" {
			site = service.DefaultSiteName
		}

		title := site
		if page == PageAdmin {
			title = site + " 控制台"
		}
		desc := strings.TrimSpace(m.Tagline)
		if desc == "" {
			desc = site
		}
		icon := m.Logo
		if !strings.HasPrefix(icon, "data:image/") {
			icon = DefaultIcon
		}
		return webapp.IndexMeta{
			Title:       title,
			Description: desc,
			AppName:     site,
			Icon:        icon,
		}
	}
}
