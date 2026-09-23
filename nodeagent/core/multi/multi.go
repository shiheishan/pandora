// Package multi 把多个内核组合成一个 core.Core。
//
// 存在的理由有两层：
//
// 一是不是所有协议都能装进 sing-box —— Mieru 自带监听器，Juicity 以独立进程
// 运行，它们各有各的生命周期。
//
// 二是同一个协议可以由不同内核承载。VLESS 在 sing-box 和 xray-core 上都能跑，
// 但实现细节有差异（XHTTP 传输只有 xray 有，个别客户端也只认 xray 的行为）。
// 该用哪个是运营决策，因此由面板按节点下发 kernel 字段决定，这里负责执行。
//
// 上层的同步循环不需要知道这些差异，它只管「按 tag 加入站、加用户、取流量」。
package multi

import (
	"context"
	"log/slog"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/external"
	"github.com/aegispanel/nodeagent/core/mieru"
	"github.com/aegispanel/nodeagent/core/sing"
	"github.com/aegispanel/nodeagent/core/xray"
)

// standalone 是不依赖通用内核的独立入站需要满足的接口。
type standalone interface {
	Start() error
	Close() error
	AddUsers(users []core.User) error
	DelUsers(uuids []string) error
	Traffic() []core.UserTraffic
	Online() map[int64][]string
}

type Core struct {
	sing *sing.Core
	xray *xray.Core
	log  *slog.Logger

	mu    sync.RWMutex
	alone map[string]standalone // tag -> 独立入站
	// routes 记录每个 tag 归属哪个通用内核。
	// 没有它的话，取流量时就得挨个内核问一遍，
	// 而「问错内核」和「这个用户没流量」在返回值上无法区分。
	routes map[string]core.Core
	ctx    context.Context
}

func New(log *slog.Logger) *Core {
	return &Core{
		sing:   sing.New(log),
		xray:   xray.New(log),
		log:    log,
		alone:  make(map[string]standalone),
		routes: make(map[string]core.Core),
	}
}

func (c *Core) Type() string { return "multi(sing-box+xray-core+mieru)" }

func (c *Core) Start(ctx context.Context) error {
	c.ctx = ctx
	if err := c.sing.Start(ctx); err != nil {
		return err
	}
	// xray 实例先起着，哪怕一个入站都没有 —— 它支持运行时增删入站，
	// 起一个空实例的开销可以忽略，换来的是首个 xray 节点上线时不必等初始化
	return c.xray.Start(ctx)
}

func (c *Core) Close() error {
	c.mu.Lock()
	for _, in := range c.alone {
		_ = in.Close()
	}
	c.alone = make(map[string]standalone)
	c.routes = make(map[string]core.Core)
	c.mu.Unlock()

	err := c.sing.Close()
	if xerr := c.xray.Close(); err == nil {
		err = xerr
	}
	return err
}

// AddInbound 决定由谁承载这个入站。
func (c *Core) AddInbound(cfg *core.InboundConfig) error {
	switch cfg.Protocol {
	case "mieru":
		transport := "TCP"
		if s, ok := cfg.Raw["transport"].(string); ok && s != "" {
			transport = s
		}
		return c.putStandalone(cfg.Tag, mieru.New(cfg.Tag, cfg.Port, transport, c.log))

	case "juicity":
		return c.putStandalone(cfg.Tag, external.NewJuicity(external.JuicityOptions{
			Tag:        cfg.Tag,
			Port:       cfg.Port,
			Binary:     strFrom(cfg.Raw, "binary"),
			WorkDir:    strFrom(cfg.Raw, "work_dir"),
			CertPath:   strFrom(cfg.Raw, "cert_path"),
			KeyPath:    strFrom(cfg.Raw, "key_path"),
			Congestion: strFrom(cfg.Raw, "congestion_control"),
			Log:        c.log,
		}))
	}

	kernel := c.pick(cfg)

	c.mu.RLock()
	old, hasOld := c.routes[cfg.Tag]
	c.mu.RUnlock()

	// 换内核时必须先摘旧的再挂新的。反过来做会踩到端口冲突：
	// 旧内核还占着监听端口，新内核 bind 失败 —— 而 sing-box 是单实例，
	// 一个入站起不来会让整个实例启动失败，同实例的其它入站一起下线。
	// 代价是切换期间这个节点有短暂空窗，那是不可避免的：
	// 一个端口同一时刻只能被一个监听者持有。
	if hasOld && old != kernel {
		if err := old.DelInbound(cfg.Tag); err != nil {
			c.log.Warn("摘除旧内核上的入站失败", "tag", cfg.Tag,
				"内核", old.Type(), "err", err)
		}
	}

	if err := kernel.AddInbound(cfg); err != nil {
		// 旧的已经摘了、新的又没起来，这个 tag 眼下无人服务。
		// 清掉路由记录，让下一轮同步当作全新入站重来一次 ——
		// 留着错误的记录会让后续的取流量、加用户全都发往一个空壳。
		c.mu.Lock()
		delete(c.routes, cfg.Tag)
		c.mu.Unlock()
		return err
	}

	c.mu.Lock()
	c.routes[cfg.Tag] = kernel
	c.mu.Unlock()
	c.log.Info("入站已分派", "tag", cfg.Tag, "内核", kernel.Type())
	return nil
}

// pick 按面板下发的 kernel 字段挑内核。
//
// auto（也是默认值）走 sing-box：它支持的协议是 xray 的超集，
// Naive、AnyTLS、Hysteria 这些只有它有。选 xray 是为了那些
// sing-box 给不了的东西 —— XHTTP 传输，或者某个客户端只认 xray 的实现。
func (c *Core) pick(cfg *core.InboundConfig) core.Core {
	switch cfg.Kernel {
	case "xray-core":
		return c.xray
	case "sing-box":
		return c.sing
	default:
		return c.sing
	}
}

func (c *Core) putStandalone(tag string, in standalone) error {
	c.mu.Lock()
	if old, exists := c.alone[tag]; exists {
		_ = old.Close()
	}
	c.alone[tag] = in
	c.mu.Unlock()
	return in.Start()
}

func (c *Core) DelInbound(tag string) error {
	c.mu.Lock()
	in, isAlone := c.alone[tag]
	delete(c.alone, tag)
	kernel, hasRoute := c.routes[tag]
	delete(c.routes, tag)
	c.mu.Unlock()

	if isAlone {
		return in.Close()
	}
	if hasRoute {
		return kernel.DelInbound(tag)
	}
	return nil
}

func (c *Core) AddUsers(tag string, users []core.User) error {
	if in, ok := c.lookupAlone(tag); ok {
		return in.AddUsers(users)
	}
	if k, ok := c.lookupKernel(tag); ok {
		return k.AddUsers(tag, users)
	}
	return nil
}

func (c *Core) DelUsers(tag string, uuids []string) error {
	if in, ok := c.lookupAlone(tag); ok {
		return in.DelUsers(uuids)
	}
	if k, ok := c.lookupKernel(tag); ok {
		return k.DelUsers(tag, uuids)
	}
	return nil
}

func (c *Core) GetTraffic(tag string) ([]core.UserTraffic, error) {
	if in, ok := c.lookupAlone(tag); ok {
		return in.Traffic(), nil
	}
	if k, ok := c.lookupKernel(tag); ok {
		return k.GetTraffic(tag)
	}
	return nil, nil
}

func (c *Core) OnlineIPs(tag string) map[int64][]string {
	if in, ok := c.lookupAlone(tag); ok {
		return in.Online()
	}
	if k, ok := c.lookupKernel(tag); ok {
		return k.OnlineIPs(tag)
	}
	return nil
}

// SetRouting 把出站与分流同时下发给两个通用内核。
//
// 两边都设是必要的：节点随时可能被切到另一个内核，
// 只设当前那个的话，切换之后分流会静默消失。
func (c *Core) SetRouting(inboundTag string, r *core.Routing) error {
	if err := c.sing.SetRouting(inboundTag, r); err != nil {
		return err
	}
	return c.xray.SetRouting(inboundTag, r)
}

func (c *Core) lookupAlone(tag string) (standalone, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	in, ok := c.alone[tag]
	return in, ok
}

func (c *Core) lookupKernel(tag string) (core.Core, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	k, ok := c.routes[tag]
	return k, ok
}

func strFrom(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

var (
	_ core.Core  = (*Core)(nil)
	_ standalone = (*external.Juicity)(nil)
	_ standalone = (*mieru.Inbound)(nil)
)
