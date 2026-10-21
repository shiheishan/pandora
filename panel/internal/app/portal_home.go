// 门户首页片段的内容：站点名、标语取 appearance 的入口页快照（PageMeta，自己有缓存与失效），
// 首页开关、介绍、常见问题与注册方式（RegistrationPolicy，已算上 auth.registration 总闸）取 settings，
// 套餐与价格取 catalog 的匿名目录；后几样缓存进程内快照：入口页是门户最热的地址，不能每次打开都查库。
// 成功缓存 30 秒，本进程保存首页设置后经 HomeSettingsChanged 立刻失效（注册方式与套餐变动只靠 30 秒 TTL）；
// 读失败缓存 5 秒、这段时间不出片段（照常回 SPA，不让 / 报错）；
// 同一时刻只有一趟读库（singleflight），读库不跟随第一个调用者的取消，上限 2 秒。
package app

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appearancesvc "github.com/aegispanel/aegis/internal/module/appearance/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// homeSettingsReader 是首页要的设置读取面；*settingssvc.Service 满足。
type homeSettingsReader interface {
	Home(ctx context.Context) (settingssvc.Home, error)
	RegistrationPolicy(ctx context.Context) (settingssvc.Registration, error)
}

// homeCatalogReader 是首页要的套餐目录读取面；*catalogsvc.Service 满足。
type homeCatalogReader interface {
	PortalCatalog(ctx context.Context, userID *uuid.UUID) ([]catalogsvc.OfferPlan, error)
}

// homePageMeta 是首页要的站点名与标语；*appearancesvc.Service 满足。
type homePageMeta interface {
	PageMeta(ctx context.Context) appearancesvc.PageMeta
}

const (
	portalHomeTTL         = 30 * time.Second
	portalHomeErrorTTL    = 5 * time.Second
	portalHomeReadTimeout = 2 * time.Second
)

// homeParts 是缓存的那部分：设置与套餐（站点名等每次从 PageMeta 取）。
type homeParts struct {
	enabled bool
	signup  webapp.Signup
	intro   string
	faq     []webapp.HomeFAQ
	plans   []webapp.HomePlan
}

// portalHome 组装门户首页内容；实现 settingssvc.HomeChangeListener。
type portalHome struct {
	settings homeSettingsReader
	catalog  homeCatalogReader
	meta     homePageMeta
	now      func() time.Time
	group    singleflight.Group
	gen      atomic.Uint64 // HomeSettingsChanged 加一

	mu       sync.Mutex
	parts    homeParts
	err      error
	cacheGen uint64
	expireAt time.Time
	has      bool // 是否已有缓存项（成功或失败）
}

type homeLoadResult struct {
	parts homeParts
	err   error
}

var _ settingssvc.HomeChangeListener = (*portalHome)(nil)

func newPortalHome(s homeSettingsReader, c homeCatalogReader, m homePageMeta) *portalHome {
	return &portalHome{
		settings: s,
		catalog:  c,
		meta:     m,
		now:      time.Now,
	}
}

// HomeSettingsChanged 清缓存：首页设置提交后由 settings 同步调用。
func (p *portalHome) HomeSettingsChanged() { p.gen.Add(1) }

// Home 给 webapp.WithHome：首页关着或读失败时返回 false（入口页原样下发）。
func (p *portalHome) Home(ctx context.Context) (webapp.Home, bool) {
	parts, err := p.get(ctx)
	if err != nil {
		return webapp.Home{}, false
	}
	if !parts.enabled {
		return webapp.Home{}, false
	}
	m := p.meta.PageMeta(ctx)
	return webapp.Home{
		SiteName: m.SiteName,
		Tagline:  m.Tagline,
		Signup:   parts.signup,
		Intro:    parts.intro,
		Plans:    parts.plans,
		FAQ:      parts.faq,
	}, true
}

func (p *portalHome) get(ctx context.Context) (homeParts, error) {
	g := p.gen.Load()
	p.mu.Lock()
	if p.has && p.cacheGen == g && p.now().Before(p.expireAt) {
		parts, err := p.parts, p.err
		p.mu.Unlock()
		return parts, err
	}
	p.mu.Unlock()

	r, _, _ := p.group.Do(strconv.FormatUint(g, 10), func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), portalHomeReadTimeout)
		defer cancel()
		parts, err := p.load(rctx)
		ttl := portalHomeTTL
		if err != nil {
			ttl = portalHomeErrorTTL
			plog.From(rctx).Warn("读取首页内容失败，门户入口页不出首页", zap.Error(err))
		}
		p.mu.Lock()
		if p.gen.Load() == g {
			p.parts, p.err, p.cacheGen, p.expireAt, p.has = parts, err, g, p.now().Add(ttl), true
		}
		p.mu.Unlock()
		return homeLoadResult{parts: parts, err: err}, nil
	})
	res := r.(homeLoadResult)
	return res.parts, res.err
}

func (p *portalHome) load(ctx context.Context) (homeParts, error) {
	h, err := p.settings.Home(ctx)
	if err != nil {
		return homeParts{}, err
	}
	if !h.Enabled {
		return homeParts{enabled: false}, nil
	}
	reg, err := p.settings.RegistrationPolicy(ctx)
	if err != nil {
		return homeParts{}, err
	}
	catalog, err := p.catalog.PortalCatalog(ctx, nil)
	if err != nil {
		return homeParts{}, err
	}
	faq := make([]webapp.HomeFAQ, len(h.FAQ))
	for i, item := range h.FAQ {
		faq[i] = webapp.HomeFAQ{Q: item.Q, A: item.A}
	}
	return homeParts{
		enabled: true,
		signup:  homeSignup(reg.Mode),
		intro:   h.Intro,
		faq:     faq,
		plans:   homePlans(catalog),
	}, nil
}

// homeSignup 把设置里的注册方式换成首页的注册入口；认不出的值按关闭，宁可不出注册入口。
func homeSignup(mode string) webapp.Signup {
	switch mode {
	case settingssvc.RegistrationOpen:
		return webapp.SignupOpen
	case settingssvc.RegistrationInviteOnly:
		return webapp.SignupInvite
	default:
		return webapp.SignupClosed
	}
}

// homePlans 把匿名目录转成首页套餐卡：只列能新购、有价格的；文字照门户选购页的说法（portal/screens/common/catalog.ts）。
func homePlans(plans []catalogsvc.OfferPlan) []webapp.HomePlan {
	work := make([]catalogsvc.OfferPlan, 0, len(plans))
	for _, op := range plans {
		if !op.Plan.AllowNewPurchase || len(op.Prices) == 0 {
			continue
		}
		work = append(work, op)
	}
	// 按时间的在前、一次性在后，同类保持目录原顺序（目录已按套餐排序）
	slices.SortStableFunc(work, func(a, b catalogsvc.OfferPlan) int {
		return homeKindOrder(a.Plan.Kind) - homeKindOrder(b.Plan.Kind)
	})

	out := make([]webapp.HomePlan, 0, len(work))
	for _, op := range work {
		out = append(out, homePlanOne(op))
	}
	return out
}

func homeKindOrder(kind string) int {
	switch kind {
	case catalogsvc.KindSubscription:
		return 0
	case catalogsvc.KindOneTime:
		return 1
	default:
		return 2
	}
}

func homePlanOne(op catalogsvc.OfferPlan) webapp.HomePlan {
	plan := webapp.HomePlan{
		Name: op.Plan.Name,
		Kind: homeKindLabel(op.Plan.Kind),
	}
	if op.Plan.Recommended {
		plan.Badge = "推荐"
	}
	if op.Plan.Description != nil {
		plan.Desc = strings.TrimSpace(*op.Plan.Description)
	}
	plan.Usage = homeUsage(op)
	switch op.Plan.Kind {
	case catalogsvc.KindSubscription:
		plan.Price, plan.PriceNote = homeSubscriptionPrice(op.Prices)
	case catalogsvc.KindOneTime:
		plan.Price, plan.PriceNote = homeOneTimePrice(op.Prices[0])
	}
	return plan
}

func homeKindLabel(kind string) string {
	switch kind {
	case catalogsvc.KindSubscription:
		return "按时间"
	case catalogsvc.KindOneTime:
		return "一次性"
	default:
		return kind
	}
}

func homeUsage(op catalogsvc.OfferPlan) string {
	var parts []string
	if lim := op.Version.TrafficLimit(); lim == nil {
		parts = append(parts, "不限流量")
	} else if op.Plan.Kind == catalogsvc.KindOneTime {
		parts = append(parts, "共 "+gb(*lim))
	} else {
		parts = append(parts, "每期 "+gb(*lim))
	}
	if op.Version.MaxDevices == nil {
		parts = append(parts, "不限台数")
	} else {
		parts = append(parts, strconv.Itoa(*op.Version.MaxDevices)+" 台设备")
	}
	if op.Version.ThrottleKbps != nil {
		mbps := strconv.FormatFloat(float64(*op.Version.ThrottleKbps)/1000, 'f', -1, 64)
		parts = append(parts, "限速 "+mbps+" Mbps")
	}
	return strings.Join(parts, " · ")
}

func homeSubscriptionPrice(prices []catalogsvc.Price) (price, note string) {
	var head *catalogsvc.Price
	for i := range prices {
		p := &prices[i]
		if p.BillingInterval == catalogsvc.IntervalMonth && p.IntervalCount == 1 {
			head = p
			break
		}
	}
	if head == nil {
		head = &prices[0]
	}
	_, unit, ok := tierName(head.BillingInterval, head.IntervalCount)
	if !ok {
		unit = ""
	}
	if unit != "" {
		price = formatYuan(money.MustCents(head.UnitAmount)) + " / " + unit
	} else {
		price = formatYuan(money.MustCents(head.UnitAmount))
	}
	var notes []string
	for _, p := range prices {
		if p.BillingInterval == head.BillingInterval && p.IntervalCount == head.IntervalCount {
			continue
		}
		n, _, ok := tierName(p.BillingInterval, p.IntervalCount)
		if !ok {
			continue
		}
		notes = append(notes, n+" "+formatYuan(money.MustCents(p.UnitAmount)))
	}
	return price, strings.Join(notes, " · ")
}

func homeOneTimePrice(p catalogsvc.Price) (price, note string) {
	price = formatYuan(money.MustCents(p.UnitAmount))
	if p.ValidityDays == nil {
		note = "永久有效"
	} else {
		note = "有效 " + strconv.Itoa(*p.ValidityDays) + " 天"
	}
	return price, note
}

// tierName 把价格档映射成展示用的档名与价签单位。
func tierName(interval string, count int) (name, unit string, ok bool) {
	switch {
	case interval == catalogsvc.IntervalOneTime && count == 1:
		return "一次性", "", true
	case interval == catalogsvc.IntervalMonth && count == 1:
		return "月付", "月", true
	case interval == catalogsvc.IntervalMonth && count == 3:
		return "季付", "季", true
	case interval == catalogsvc.IntervalMonth && count == 6:
		return "半年付", "半年", true
	case (interval == catalogsvc.IntervalYear && count == 1) || (interval == catalogsvc.IntervalMonth && count == 12):
		return "年付", "年", true
	default:
		return "", "", false
	}
}

// formatYuan 与前端 formatMoney 一致：整数分 → ¥ 展示串。
func formatYuan(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	units := cents / 100
	frac := cents % 100
	grouped := groupThousands(units)
	return sign + "¥" + grouped + "." + pad2(frac)
}

func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// gb 与前端 catalog.gb 一致。
func gb(bytes int64) string {
	g := float64(max(0, bytes)) / float64(1<<30)
	if g >= 1024 {
		return trimGB(g/1024) + "T"
	}
	if g >= 1 {
		return trimGB(g) + "G"
	}
	return strconv.FormatInt(int64(math.Round(float64(max(0, bytes))/float64(1<<20))), 10) + "M"
}

func trimGB(n float64) string {
	if n >= 10 {
		return strconv.FormatInt(int64(math.Round(n)), 10)
	}
	return strconv.FormatFloat(math.Round(n*10)/10, 'f', -1, 64)
}
