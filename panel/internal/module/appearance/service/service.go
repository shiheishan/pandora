// Package service 是 appearance 模块的业务层：门户主题（配色令牌与品牌）、内容插槽（净化后的 HTML），
// 以及门户一次取齐的外观数据。别的模块只调本包。
//
// 主题与插槽都会渲染进门户、被所有访客看到，所以写入一律走白名单：令牌只收设计稿的 CSS 变量（tokens.go），
// 品牌只收标语与 data: 形式的 Logo（站点名只认系统设置 site.name），插槽 HTML 经 SanitizeHTML 净化后才落库（sanitize.go）。
// 门户读取时再按同一套白名单过滤一次，库里被手工改过的行也出不去。
package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/appearance/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	settingsvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// SiteSettings 是 settings service 的只读面：插槽的更新时间按站点时区显示，站点名也从这里取（PageMeta）。*settingsvc.Service 直接满足。
type SiteSettings interface {
	Site(ctx context.Context) (settingsvc.Site, error)
}

// Service 是 appearance 模块对外的业务入口。
type Service struct {
	repo  *repo.Repo
	tx    txRunner
	audit auditWriter
	site  SiteSettings

	// meta 是 PageMeta 的进程内缓存；metaGen 是它的失效代号，保存或激活主题后加一。
	meta    pageMetaCache
	metaGen atomic.Uint64
	// now 是测试用的时钟，nil 表示 time.Now。
	now func() time.Time
}

// New 构造 appearance service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, site SiteSettings) *Service {
	return &Service{repo: r, tx: txm, audit: audit, site: site}
}

// 审计动作名。
const (
	ActionThemeSave     = "appearance.theme.save"
	ActionThemeActivate = "appearance.theme.activate"
	ActionThemeDelete   = "appearance.theme.delete"
	ActionSlotSave      = "appearance.slot.save"
)

// Theme 是一套主题；Tokens、Branding 是 JSON 对象原文。自定义 CSS 停用，不再有这一项。
type Theme struct {
	ID        uuid.UUID
	Code      string
	Name      string
	IsBuiltin bool
	IsActive  bool
	Tokens    json.RawMessage
	Branding  json.RawMessage
}

// SaveThemeInput 是新建或编辑主题的输入。
type SaveThemeInput struct {
	Code string
	Name string
	// Create 为 true 表示「新建 / 另存为」：code 已被占用回 409；为 false 时按 code 新建或覆盖（编辑已有主题）。
	Create   bool
	Tokens   json.RawMessage
	Branding json.RawMessage
}

// Slot 是后台插槽列表的一项：位置说明取自 SlotCatalog，内容与开关取自库里（从没保存过的为零值）。
type Slot struct {
	Key     string
	Label   string
	Where   string
	Content string
	Enabled bool
	// UpdatedAt 是站点时区的「YYYY-MM-DD HH:MM」；从没保存过的插槽位为空串。
	UpdatedAt string
}

// SaveSlotInput 是保存一个插槽的输入。
type SaveSlotInput struct {
	Key     string
	Content string
	Enabled bool
}

// PublicAppearance 是门户渲染需要的全部外观数据。
type PublicAppearance struct {
	// SiteName 是站点名：取系统设置（经 PageMeta 快照，最多晚 30 秒），永不为空。
	SiteName string
	// Theme 是生效主题（已按白名单过滤）；没有生效主题为 nil。
	Theme *Theme
	// Slots 是启用且非空的插槽：键 → 净化后的 HTML；永不为 nil。
	Slots map[string]string
}

// SlotCatalog 是插槽位的说明。
//
// 放在代码里而不是库里：这些位置对应前端模板里的挂载点，两者必须同步改（迁移里的 CHECK 也是这七个）。
var SlotCatalog = []struct{ Key, Label, Where string }{
	{"portal.login.notice", "登录页提示", "登录框上方，未登录访客可见"},
	{"portal.home.banner", "我的套餐横幅", "我的套餐页最顶部，通栏"},
	{"portal.home.aside", "我的套餐附加卡片", "我的套餐页内容下方"},
	{"portal.sidebar.extra", "侧栏附加内容", "侧栏导航下方、账号信息上方"},
	{"portal.subscribe.notice", "订阅页说明", "「我的订阅」页顶部"},
	{"portal.plans.notice", "选购页说明", "「选购套餐」页顶部，适合放退换与发票说明"},
	{"portal.footer", "页脚", "所有页面底部"},
}

// MaxSlotBytes 是净化后插槽内容的上限（与表上的 CHECK 一致）。
const MaxSlotBytes = 32768

// slotTimeLayout 是插槽更新时间的显示格式。
const slotTimeLayout = "2006-01-02 15:04"

// location 取站点时区；取不到或不认识时用 UTC。
func (s *Service) location(ctx context.Context) *time.Location {
	if s.site == nil {
		return time.UTC
	}
	site, err := s.site.Site(ctx)
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(site.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
