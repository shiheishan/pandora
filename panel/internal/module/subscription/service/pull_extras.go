package service

// 拉取时用到的后台模板、订阅附加设置（信息节点、协议前缀，在 settings）与订阅域名分组（本模块的表），
// 进程内快照 30 秒过期（serve 是单实例）；附加设置（经 settings 的监听）、模板、订阅域名在本进程保存后立即作废，30 秒只是兜底。

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// ExtrasSettings 是订阅附加设置的读取面；*settingssvc.Service 直接满足。
type ExtrasSettings interface {
	SubscriptionExtras(ctx context.Context) (settingssvc.SubscriptionExtras, error)
}

// extrasRepo 读全部自定义模板与订阅域名分组；*repo.Repo 直接满足。
type extrasRepo interface {
	TemplateContents(ctx context.Context) (map[string]string, error)
	DomainConfig(ctx context.Context) (repo.DomainConfigRows, error)
}

// extrasTTL 是快照的有效期。
const extrasTTL = 30 * time.Second

// extrasRetry 是重读失败、沿用旧快照后再试的间隔（期间不再重读，也不重复打日志）。
const extrasRetry = 5 * time.Second

// extrasSnapshot 是某一刻的模板与附加设置。
type extrasSnapshot struct {
	settings  settingssvc.SubscriptionExtras
	templates map[render.Format]string
	domains   domainSet // 订阅域名分组（只读）
	loadedAt  time.Time
	gen       uint64 // 开始读库前的失效代号；与 pullExtras.gen 不等即已作废（读时比对）
}

// template 返回 f 的自定义模板；没有返回空串（用内置）。
func (x *extrasSnapshot) template(f render.Format) string {
	if x == nil || x.templates == nil {
		return ""
	}
	return x.templates[f]
}

// pullExtras 是快照的进程内缓存；零值可用。
type pullExtras struct {
	mu   sync.Mutex
	gen  atomic.Uint64
	snap atomic.Pointer[extrasSnapshot]
}

// invalidate 让快照立刻失效；失效之前开始、之后才读完的那趟重读不会把旧内容写回，
// 即使在「比完代号、写回之前」这一刻失效、旧内容被写了回去，它带的是旧代号，读时比对作废（fresh）。
func (p *pullExtras) invalidate() {
	p.gen.Add(1)
	p.snap.Store(nil)
}

// SubscriptionExtrasChanged 实现 settings 的 SubscriptionExtrasListener：订阅附加设置提交后让拉取快照立刻失效。
func (s *Service) SubscriptionExtrasChanged() { s.extras.invalidate() }

// fresh 判断快照能不能直接用：代号没变、没过期（时钟回拨也算不新鲜）。
func (p *pullExtras) fresh(snap *extrasSnapshot, now time.Time) bool {
	return snap != nil && snap.gen == p.gen.Load() && !now.Before(snap.loadedAt) && now.Sub(snap.loadedAt) < extrasTTL
}

// get 返回未过期的快照，过期或没有时重读（并发的重读经 mu 合成一次：拿到锁后再看一次是否已被别人刷新）。
// src 为 nil 时附加设置取零值（全部关闭），tr 为 nil 时分组为空。读失败时：有旧快照就沿用它、打一条 Warn，
// extrasRetry 之后再试；没有旧快照才返回错误（不缓存）。
// 判断新鲜见 fresh。
func (p *pullExtras) get(ctx context.Context, src ExtrasSettings, tr extrasRepo, now time.Time) (*extrasSnapshot, error) {
	if snap := p.snap.Load(); p.fresh(snap, now) {
		return snap, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if snap := p.snap.Load(); p.fresh(snap, now) {
		return snap, nil
	}
	g := p.gen.Load()
	settings, raw, groups, err := readExtras(ctx, src, tr)
	if err != nil {
		old := p.snap.Load()
		if old == nil {
			return nil, err
		}
		plog.From(ctx).Warn("订阅附加设置或模板重读失败，沿用旧快照", zap.Error(err))
		// 旧快照的内容发布后只读，浅拷贝共用即可；把加载时刻挪到「extrasRetry 后过期」
		stale := *old
		stale.loadedAt = now.Add(extrasRetry - extrasTTL)
		stale.gen = g
		if p.gen.Load() == g {
			p.snap.Store(&stale)
		}
		return &stale, nil
	}
	templates := make(map[render.Format]string, len(raw))
	for k, v := range raw {
		templates[render.Format(k)] = v
	}
	snap := &extrasSnapshot{
		settings:  settings,
		templates: templates,
		domains:   newDomainSet(groups),
		loadedAt:  now,
		gen:       g,
	}
	if p.gen.Load() == g {
		p.snap.Store(snap)
	}
	return snap, nil
}

// readExtras 读附加设置、全部自定义模板与订阅域名分组；src、tr 为 nil 的那一项取零值。
func readExtras(ctx context.Context, src ExtrasSettings, tr extrasRepo) (settingssvc.SubscriptionExtras, map[string]string, []DomainGroup, error) {
	var settings settingssvc.SubscriptionExtras
	var err error
	if src != nil {
		if settings, err = src.SubscriptionExtras(ctx); err != nil {
			return settingssvc.SubscriptionExtras{}, nil, nil, err
		}
	}
	var raw map[string]string
	var groups []DomainGroup
	if tr != nil {
		if raw, err = tr.TemplateContents(ctx); err != nil {
			return settingssvc.SubscriptionExtras{}, nil, nil, err
		}
		rows, err := tr.DomainConfig(ctx)
		if err != nil {
			return settingssvc.SubscriptionExtras{}, nil, nil, err
		}
		groups = domainGroupsFromRows(rows)
	}
	return settings, raw, groups, nil
}

// liveInput 是组一次正常拉取的渲染请求要的材料。
type liveInput struct {
	Format       render.Format
	Nodes        []render.Node
	Row          repo.PullRow
	UA           string
	Options      render.Options
	Extras       *extrasSnapshot
	Types        string // 查询参数 types 原文
	Filter       string // 查询参数 filter 原文
	Loc          *time.Location
	SubscribeURL string
	Now          time.Time
}

// liveRequest 组 render.Request。
func liveRequest(in liveInput) render.Request {
	var tmpl string
	if render.Templatable(in.Format) && in.Extras != nil {
		tmpl = in.Extras.template(in.Format)
	}
	ui := userinfoOf(in.Row)
	st := render.Status{
		Upload:   ui.Upload,
		Download: ui.Download,
		Total:    ui.Total,
		Expire:   in.Row.PeriodEnd,
		Loc:      in.Loc,
	}
	var infoLines []string
	if in.Extras != nil && in.Extras.settings.InfoNodes {
		// 「距下次重置」用认证查询里顺带取的本期配额行周期末
		infoLines = render.InfoLines(st, in.Row.TrafficPeriodEnd, in.Now)
	}
	prefix := false
	if in.Extras != nil {
		prefix = in.Extras.settings.ProtocolPrefix
	}
	return render.Request{
		Format:         in.Format,
		Nodes:          in.Nodes,
		ProxyUUID:      in.Row.ProxyUUID.String(),
		UA:             in.UA,
		Options:        in.Options,
		Template:       tmpl,
		Types:          render.ParseTypes(in.Types),
		Keywords:       render.ParseKeywords(in.Filter),
		ProtocolPrefix: prefix,
		InfoLines:      infoLines,
		Status:         st,
		SubscribeURL:   in.SubscribeURL,
	}
}

// subscribeURLFor 是 Surfboard 托管配置里写的订阅链接：用户所在组里基础权重最高的订阅域名（托管配置要固定地址，不随机），
// 没有组用面板对外地址 fallback。
func subscribeURLFor(ex *extrasSnapshot, userGroupID *uuid.UUID, fallback, prefix, token string) string {
	base := fallback
	if ex != nil {
		if g := ex.domains.groupFor(userGroupID); g != nil {
			base = stableBaseURL(g)
		}
	}
	return linkURL(base, prefix, token)
}
