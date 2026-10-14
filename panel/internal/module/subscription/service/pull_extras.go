package service

// 拉取时用到的后台模板与订阅附加设置（信息节点、协议前缀、订阅域名）。
// 两者很少变，进程内留一份快照、30 秒过期（serve 是单实例），
// 拉取路径因此不每次多查库；后台改动最多 30 秒后生效。

import (
	"context"
	"crypto/rand"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

// ExtrasSettings 是订阅附加设置的读取面；*settingssvc.Service 直接满足。
type ExtrasSettings interface {
	SubscriptionExtras(ctx context.Context) (settingssvc.SubscriptionExtras, error)
}

// templateReader 读全部自定义模板；*repo.Repo 直接满足。
type templateReader interface {
	TemplateContents(ctx context.Context) (map[string]string, error)
}

// extrasTTL 是快照的有效期。
const extrasTTL = 30 * time.Second

// extrasSnapshot 是某一刻的模板与附加设置。
type extrasSnapshot struct {
	settings  settingssvc.SubscriptionExtras
	templates map[render.Format]string
	loadedAt  time.Time
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
	snap atomic.Pointer[extrasSnapshot]
}

// get 返回未过期的快照，过期或没有时重读（并发的重读经 mu 合成一次：拿到锁后再看一次是否已被别人刷新）。
// src 为 nil 时附加设置取零值（全部关闭、没有订阅域名）。读失败返回错误、不缓存。
// 判断新鲜：snap 非 nil 且 !now.Before(snap.loadedAt) 且 now.Sub(snap.loadedAt) < extrasTTL（时钟回拨也重读）。
func (p *pullExtras) get(ctx context.Context, src ExtrasSettings, tr templateReader, now time.Time) (*extrasSnapshot, error) {
	if snap := p.snap.Load(); snap != nil && !now.Before(snap.loadedAt) && now.Sub(snap.loadedAt) < extrasTTL {
		return snap, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if snap := p.snap.Load(); snap != nil && !now.Before(snap.loadedAt) && now.Sub(snap.loadedAt) < extrasTTL {
		return snap, nil
	}
	var settings settingssvc.SubscriptionExtras
	var err error
	if src != nil {
		settings, err = src.SubscriptionExtras(ctx)
		if err != nil {
			return nil, err
		}
	}
	var raw map[string]string
	if tr != nil {
		raw, err = tr.TemplateContents(ctx)
		if err != nil {
			return nil, err
		}
	}
	templates := make(map[render.Format]string, len(raw))
	for k, v := range raw {
		templates[render.Format(k)] = v
	}
	snap := &extrasSnapshot{
		settings:  settings,
		templates: templates,
		loadedAt:  now,
	}
	p.snap.Store(snap)
	return snap, nil
}

// liveInput 是组一次正常拉取的渲染请求要的材料。
type liveInput struct {
	Format       render.Format
	Nodes        []render.Node
	Row          repo.PullRow
	UA           string
	Options      render.Options
	Extras       *extrasSnapshot
	Types        string     // 查询参数 types 原文
	Filter       string     // 查询参数 filter 原文
	NextReset    *time.Time // 本期流量配额的周期末；只在开了信息节点时查
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
		infoLines = render.InfoLines(st, in.NextReset, in.Now)
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

// pickLinkBase 从后台填的订阅域名里随机取一个（crypto/rand 取下标）；没填返回 fallback（面板对外地址）。
func pickLinkBase(bases []string, fallback string) string {
	if len(bases) == 0 {
		return fallback
	}
	if len(bases) == 1 {
		return bases[0]
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(bases))))
	if err != nil {
		return bases[0]
	}
	return bases[n.Int64()]
}

// subscribeURLFor 是 Surfboard 托管配置里写的订阅链接：用面板对外地址（或后台填的第一个订阅域名）拼前缀与令牌。
// bases 非空时用 bases[0]（托管配置要固定地址，不随机）；否则 fallback。
func subscribeURLFor(bases []string, fallback, prefix, token string) string {
	base := fallback
	if len(bases) > 0 {
		base = bases[0]
	}
	return linkURL(base, prefix, token)
}
