// 入口页与门户外观用的站点信息快照：站点名取系统设置 site.name，标语取生效主题的 branding（已按白名单过滤）；
// SEO 关键词、描述、是否允许收录与站点对外根地址取系统设置；robots.txt 也读这份快照。
//
// 入口页是门户最热的地址，不能每次回源都查库，所以缓存进程内快照：
//   - 成功缓存 30 秒；本进程保存站点设置、保存或激活主题后都在提交后立刻失效，30 秒只是兜底；
//   - 读失败也缓存 5 秒（库慢或挂了时不让每个入口页请求都去等一次超时），失败期间沿用上次成功的值；
//   - 同一时刻只有一趟读库（singleflight），读库不持锁、不跟随第一个调用者的取消，上限 2 秒。

package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	settingsvc "github.com/aegispanel/aegis/internal/module/settings/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// DefaultSiteName 是读不到站点设置时的站点名，与 settings 的缺省站点名一致。
const DefaultSiteName = "Pandora"

// PageMeta 是入口页 <head> 与门户外观用的站点信息：站点名取系统设置 site.name，标语取生效主题（已按白名单过滤）；
// SEO 设置与站点对外根地址取系统设置。
type PageMeta struct {
	SiteName       string // 永不为空：读不到时是 DefaultSiteName
	Tagline        string // 没有为空
	SEOKeywords    string // 站点设置的关键词；没有为空
	SEODescription string // 站点设置的描述；没有为空（入口页再退回标语）
	AllowIndexing  bool   // 是否允许搜索引擎收录门户首页；读不到时是 settingsvc.DefaultSiteAllowIndexing
	SiteURL        string // 站点对外根地址（settings 的 SiteURL），以 / 结尾；没配为空
}

const (
	pageMetaTTL         = 30 * time.Second
	pageMetaErrorTTL    = 5 * time.Second
	pageMetaReadTimeout = 2 * time.Second
)

// pageMetaCache 是 PageMeta 的进程内缓存（含读失败），按 gen 失效；零值可用。
type pageMetaCache struct {
	group singleflight.Group

	mu       sync.Mutex
	val      PageMeta
	ok       bool // 是否成功读到过
	gen      uint64
	expireAt time.Time
}

// pageMetaResult 是 singleflight 里带回的结果。
type pageMetaResult struct {
	val PageMeta
	err error
}

// fallbackPageMeta 是从没成功读过、且当前读失败时的兜底快照（站点名与是否允许收录用缺省值）。
func fallbackPageMeta() PageMeta {
	return PageMeta{
		SiteName:      DefaultSiteName,
		AllowIndexing: settingsvc.DefaultSiteAllowIndexing,
	}
}

// cached 在锁内判断缓存是否对 gen 有效，有效时返回快照（从没成功过时站点名是缺省值）。
func (c *pageMetaCache) cached(gen uint64, now time.Time) (PageMeta, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || !now.Before(c.expireAt) {
		return PageMeta{}, false
	}
	if !c.ok {
		return fallbackPageMeta(), true
	}
	return c.val, true
}

// get 返回快照：缓存有效直接回；否则经 singleflight 调 load 读库。
// 读库失败时保留上次成功的值（ok 不变），只把过期时间推近 5 秒。
func (c *pageMetaCache) get(ctx context.Context, gen uint64, now time.Time, load func(context.Context) (PageMeta, error), logErr func(error)) PageMeta {
	if v, ok := c.cached(gen, now); ok {
		return v
	}

	// 键带上 gen：失效之后来的调用不会挂到失效前开始的那趟读库上拿旧值
	r, _, _ := c.group.Do(strconv.FormatUint(gen, 10), func() (any, error) {
		// 前一趟同 gen 的读库可能刚写回：再看一眼，免得紧随其后的调用又读一次库
		if v, ok := c.cached(gen, now); ok {
			return pageMetaResult{val: v}, nil
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pageMetaReadTimeout)
		defer cancel()
		v, err := load(rctx)
		ttl := pageMetaTTL
		if err != nil {
			ttl = pageMetaErrorTTL
		}
		c.mu.Lock()
		// 只按代号往前写：晚到的旧代号读库结果不覆盖新代号的快照（gen 只增不减）
		if gen >= c.gen {
			if err == nil {
				c.val, c.ok = v, true
			}
			c.gen = gen
			c.expireAt = now.Add(ttl)
		}
		c.mu.Unlock()
		if err != nil {
			logErr(err)
		}
		return pageMetaResult{val: v, err: err}, nil
	})
	res := r.(pageMetaResult)
	if res.err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.ok {
			return c.val
		}
		return fallbackPageMeta()
	}
	return res.val
}

// PageMeta 返回进程内快照（入口页每次回源都会调，不能每次查库）：
// 站点名取系统设置 site.name，标语取生效主题；读失败时沿用上次成功的值，从没成功过时站点名是缺省值。
func (s *Service) PageMeta(ctx context.Context) PageMeta {
	return s.meta.get(ctx, s.metaGen.Load(), s.clock(), s.loadPageMeta, func(err error) {
		plog.From(ctx).Warn("读取站点信息失败，入口页沿用上次的值", zap.Error(err))
	})
}

// clock 返回当前时间；now 非 nil 时用它（测试用）。
func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// loadPageMeta 读站点设置（站点名、SEO 三项、对外根地址）与生效主题的 branding（已按白名单过滤），出错返回错误。
func (s *Service) loadPageMeta(ctx context.Context) (PageMeta, error) {
	out := fallbackPageMeta()
	if s.site != nil {
		site, err := s.site.Site(ctx)
		if err != nil {
			return PageMeta{}, err
		}
		if name := strings.TrimSpace(site.Name); name != "" {
			out.SiteName = name
		}
		out.SEOKeywords = strings.TrimSpace(site.SEOKeywords)
		out.SEODescription = strings.TrimSpace(site.SEODescription)
		out.AllowIndexing = site.AllowIndexing
		out.SiteURL = s.site.SiteURL(ctx)
	}
	m, ok, err := s.repo.ActiveTheme(ctx)
	if err != nil {
		return PageMeta{}, err
	}
	if ok {
		var branding map[string]string
		if err := json.Unmarshal(filterBranding(json.RawMessage(m.Branding)), &branding); err == nil {
			out.Tagline = branding["tagline"]
		}
	}
	return out, nil
}

// invalidatePageMeta 让本进程的 PageMeta 快照立刻失效（站点设置或主题保存/激活提交后调用）。
func (s *Service) invalidatePageMeta() { s.metaGen.Add(1) }

// SiteSettingsChanged 实现 settings 的 SiteChangeListener：站点设置（站点名等）提交后让快照立刻失效。
func (s *Service) SiteSettingsChanged() { s.invalidatePageMeta() }
