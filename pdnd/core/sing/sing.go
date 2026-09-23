package sing

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"net/netip"
	"strings"

	"github.com/aegispanel/nodeagent/core"
)

// Core 是 sing-box 内核的封装。
type Core struct {
	logger *slog.Logger
	mu     sync.RWMutex
	box    *box.Box
	ctx    context.Context
	// 已启动的入站。sing-box 的实例是不可变的 —— 增删入站要重建整个 box，
	// 因此这里缓存配置，重建时原样重放。
	inbounds map[string]*core.InboundConfig
	managed  map[string]managedInbound

	// 每个入站的出站与分流。内核只有一张路由表，
	// 各入站的规则靠「入站限定」隔开（见 routing.go）。
	routing map[string]*core.Routing

	// 每个入站当前应有的用户。重建 box 会把内核里的用户表清空，
	// 必须由这里重放回去。
	//
	// 为什么不能指望上层重新下发：重建是「别人」引起的 —— A 节点改了配置，
	// 同实例的 B 节点跟着重建，而 B 的同步循环认为自己什么都没变，
	// 不会重新下发用户。结果 B 的用户全部失效且无人察觉，
	// 表现为一整个节点的人突然连不上。
	users map[string][]core.User
}

func New(log *slog.Logger) *Core {
	return &Core{
		logger:   log.With("kernel", "sing-box"),
		inbounds: make(map[string]*core.InboundConfig),
		managed:  make(map[string]managedInbound),
		users:    make(map[string][]core.User),
		routing:  make(map[string]*core.Routing),
	}
}

func (c *Core) Type() string { return "sing-box" }

func (c *Core) Start(ctx context.Context) error {
	c.ctx = ctx
	return c.rebuild()
}

func (c *Core) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.box != nil {
		err := c.box.Close()
		c.box = nil
		return err
	}
	return nil
}

// rebuild 用当前的入站集合重建 box 实例。
//
// 为什么增删入站要重建而增删用户不用：sing-box 的 Box 在 New 时就固化了
// 入站列表，没有运行时增删入站的接口；而用户在我们自己的 inbound 里管理，
// 完全不受此限制。节点的入站配置变更是低频操作（改协议、改端口），
// 用户增删才是高频的 —— 把高频路径做成无中断，低频路径接受重建，是合理的取舍。
func (c *Core) rebuild() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rebuildLocked()
}

func (c *Core) rebuildLocked() error {
	if len(c.inbounds) == 0 {
		if c.box != nil {
			_ = c.box.Close()
			c.box = nil
		}
		return nil
	}

	// 注册表：先取上游默认，再用我们的实现覆盖需要用户管理的协议。
	// 覆盖而非新建，是为了保留 direct/block/dns 等我们不关心的类型。
	registry := include.InboundRegistry()
	RegisterVLESS(registry)
	RegisterVMess(registry)
	RegisterTrojan(registry)
	RegisterShadowsocks(registry)
	RegisterHysteria2(registry)
	RegisterHysteria(registry)
	RegisterTUIC(registry)
	RegisterAnyTLS(registry)
	RegisterProxy(registry)
	RegisterNaive(registry)

	// ctx 必须先建好：翻译出站与路由要用到里面的注册表
	ctx := box.Context(c.ctx, registry,
		include.OutboundRegistry(),
		include.EndpointRegistry(),
		include.DNSTransportRegistry(),
		include.ServiceRegistry(),
	)

	// 分流配置有问题时只跳过那一条并记录，不中断整个构建 ——
	// 这张表是全机器共用的，中断意味着一个节点的错误配置
	// 会让同机器所有节点的同步一起失败。
	badRouting := func(err error) { c.logger.Error("分流配置有误", "err", err) }

	outbounds, err := buildOutbounds(ctx, c.routing, badRouting)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(outbounds))
	for _, o := range outbounds {
		known[o.Tag] = true
	}
	route, err := buildRoute(ctx, c.routing, known, badRouting)
	if err != nil {
		return err
	}

	opts := option.Options{
		Log:       &option.LogOptions{Disabled: false, Level: "warn", Timestamp: true},
		Inbounds:  make([]option.Inbound, 0, len(c.inbounds)),
		Outbounds: outbounds,
		Route:     route,
	}

	// 先把所有入站的配置翻译完。任何一个失败都在这里返回 ——
	// 此时旧 box 还完好无损地跑着。
	//
	// 顺序很要紧：早先的实现是「先关旧 box，再逐个构建」，结果一个节点
	// 配错（比如 hysteria2 忘了配证书）就会让整台机器上所有入站一起下线，
	// 而且此后每次同步都重蹈覆辙，永远起不来。一个坏配置的爆炸半径
	// 必须限制在它自己身上。
	// userTags 记录每个节点由哪个入站负责用户管理。
	// 普通协议是它自己；ShadowTLS 是它 detour 的内层入站。
	userTags := make(map[string]string, len(c.inbounds))
	for tag, cfg := range c.inbounds {
		list, userTag, err := buildInbound(tag, cfg)
		if err != nil {
			return fmt.Errorf("构建入站 %s: %w", tag, err)
		}
		opts.Inbounds = append(opts.Inbounds, list...)
		userTags[tag] = userTag
	}

	// 到这里配置都没问题了，才动真格：旧 box 必须先释放端口，
	// 否则新实例 bind 会失败
	if c.box != nil {
		_ = c.box.Close()
		c.box = nil
	}

	// 注册表通过 context 传递，而不是 box.Options 的字段 ——
	// 上面手动调 box.Context 而非 include.Context，正是为了塞进我们改过的
	// inbound 注册表；include.Context 会无条件用上游的默认表。
	b, err := box.New(box.Options{
		Context: ctx,
		Options: opts,
	})
	if err != nil {
		return fmt.Errorf("创建 sing-box 实例: %w", err)
	}
	if err := b.Start(); err != nil {
		_ = b.Close()
		return fmt.Errorf("启动 sing-box: %w", err)
	}
	c.box = b

	// 重新建立 tag → managedInbound 的索引，并把用户重新注入。
	// 重建会丢掉旧实例里的用户，必须由上层在 rebuild 后重新 AddUsers。
	c.managed = make(map[string]managedInbound, len(c.inbounds))
	for tag := range c.inbounds {
		// 按节点 tag 索引，但取的是负责用户的那个入站 ——
		// 上层只认识节点，不该知道 ShadowTLS 内部拆成了两层
		lookup := userTags[tag]
		if lookup == "" {
			lookup = tag
		}
		if ib, ok := b.Inbound().Get(lookup); ok {
			if mi, ok := ib.(managedInbound); ok {
				c.managed[tag] = mi
			}
		}
	}

	// 把用户重新注入。新建的 box 里每个入站的用户表都是空的，
	// 不重放的话这些人全都连不上。
	for tag, list := range c.users {
		mi, ok := c.managed[tag]
		if !ok || len(list) == 0 {
			continue
		}
		if err := mi.AddUsers(list); err != nil {
			// 重放失败只影响这一个入站，别的照常。
			// 但必须留下记录 —— 静默的话表现就是「这个节点的人都连不上」，
			// 而日志里一片祥和。
			c.logger.Error("重建后重放用户失败", "入站", tag,
				"用户数", len(list), "err", err)
		}
	}
	return nil
}

// buildInbound 把面板下发的协议配置翻译成 sing-box 的入站选项。
//
// 返回多个入站是为了 ShadowTLS：它是个外壳协议，自己不承载流量，
// 解开 TLS 伪装后要把连接交给内层协议。一个「ShadowTLS 节点」
// 在 sing-box 里因此是两个入站。userTag 指出其中哪一个负责用户管理
// 与流量计费 —— 对普通协议就是它自己，对 ShadowTLS 是内层那个。
func buildInbound(tag string, cfg *core.InboundConfig) ([]option.Inbound, string, error) {
	in, err := buildOneInbound(tag, cfg)
	if err != nil {
		return nil, "", err
	}
	if cfg.Protocol != "shadowtls" {
		return []option.Inbound{in}, tag, nil
	}
	inner, innerTag, err := buildShadowTLSInner(tag, cfg)
	if err != nil {
		return nil, "", err
	}
	return []option.Inbound{in, inner}, innerTag, nil
}

func buildOneInbound(tag string, cfg *core.InboundConfig) (option.Inbound, error) {
	listen := cfg.Listen
	if listen == "" {
		listen = "::"
	}
	base := option.ListenOptions{ListenPort: uint16(cfg.Port)}
	if a, err := netip.ParseAddr(listen); err == nil {
		addr := badoption.Addr(a)
		base.Listen = &addr
	}

	switch cfg.Protocol {
	case "vless":
		o := option.VLESSInboundOptions{ListenOptions: base}
		if t := buildTLS(cfg.Raw); t != nil {
			o.TLS = t
		}
		return option.Inbound{Type: C.TypeVLESS, Tag: tag, Options: &o}, nil
	case "vmess":
		o := option.VMessInboundOptions{ListenOptions: base}
		if t := buildTLS(cfg.Raw); t != nil {
			o.TLS = t
		}
		return option.Inbound{Type: C.TypeVMess, Tag: tag, Options: &o}, nil
	case "trojan":
		o := option.TrojanInboundOptions{ListenOptions: base}
		if t := buildTLS(cfg.Raw); t != nil {
			o.TLS = t
		}
		return option.Inbound{Type: C.TypeTrojan, Tag: tag, Options: &o}, nil
	case "shadowsocks":
		o := option.ShadowsocksInboundOptions{ListenOptions: base, Method: "2022-blake3-aes-128-gcm"}
		if m, ok := cfg.Raw["method"].(string); ok && m != "" {
			o.Method = m
		}
		// 2022 系列要求节点自身也有一把根密钥，用户 PSK 由它派生。
		// 面板必须下发，缺失时直接失败而不是用默认值 ——
		// 一个可预测的服务端密钥等于整个入站没有加密。
		p, _ := cfg.Raw["server_key"].(string)
		if p == "" {
			p, _ = cfg.Raw["password"].(string)
		}
		if p == "" && strings.HasPrefix(o.Method, "2022-") {
			return option.Inbound{}, fmt.Errorf("shadowsocks %s 需要 server_key", o.Method)
		}
		o.Password = p
		return option.Inbound{Type: C.TypeShadowsocks, Tag: tag, Options: &o}, nil
	case "hysteria2":
		o := option.Hysteria2InboundOptions{ListenOptions: base}
		o.TLS = buildTLS(cfg.Raw)
		if o.TLS == nil {
			return option.Inbound{}, fmt.Errorf("hysteria2 必须启用 TLS")
		}
		if v, ok := cfg.Raw["up_mbps"].(float64); ok {
			o.UpMbps = int(v)
		}
		if v, ok := cfg.Raw["down_mbps"].(float64); ok {
			o.DownMbps = int(v)
		}
		if s, ok := cfg.Raw["obfs_password"].(string); ok && s != "" {
			o.Obfs = &option.Hysteria2Obfs{Type: "salamander", Password: s}
		}
		return option.Inbound{Type: C.TypeHysteria2, Tag: tag, Options: &o}, nil
	case "hysteria":
		o := option.HysteriaInboundOptions{ListenOptions: base}
		o.TLS = buildTLS(cfg.Raw)
		if o.TLS == nil {
			return option.Inbound{}, fmt.Errorf("hysteria 必须启用 TLS")
		}
		if v, ok := cfg.Raw["up_mbps"].(float64); ok {
			o.UpMbps = int(v)
		}
		if v, ok := cfg.Raw["down_mbps"].(float64); ok {
			o.DownMbps = int(v)
		}
		if s, ok := cfg.Raw["obfs"].(string); ok {
			o.Obfs = s
		}
		return option.Inbound{Type: C.TypeHysteria, Tag: tag, Options: &o}, nil
	case "tuic":
		o := option.TUICInboundOptions{ListenOptions: base}
		o.TLS = buildTLS(cfg.Raw)
		if o.TLS == nil {
			return option.Inbound{}, fmt.Errorf("tuic 必须启用 TLS")
		}
		o.CongestionControl = "bbr"
		if s, ok := cfg.Raw["congestion_control"].(string); ok && s != "" {
			o.CongestionControl = s
		}
		return option.Inbound{Type: C.TypeTUIC, Tag: tag, Options: &o}, nil
	case "anytls":
		o := option.AnyTLSInboundOptions{ListenOptions: base}
		o.TLS = buildTLS(cfg.Raw)
		if o.TLS == nil {
			return option.Inbound{}, fmt.Errorf("anytls 必须启用 TLS")
		}
		if list, ok := cfg.Raw["padding_scheme"].([]any); ok {
			for _, v := range list {
				if s, ok := v.(string); ok {
					o.PaddingScheme = append(o.PaddingScheme, s)
				}
			}
		}
		return option.Inbound{Type: C.TypeAnyTLS, Tag: tag, Options: &o}, nil
	case "socks":
		o := option.SocksInboundOptions{ListenOptions: base}
		return option.Inbound{Type: C.TypeSOCKS, Tag: tag, Options: &o}, nil
	case "http":
		o := option.HTTPMixedInboundOptions{ListenOptions: base}
		o.TLS = buildTLS(cfg.Raw)
		return option.Inbound{Type: C.TypeHTTP, Tag: tag, Options: &o}, nil
	case "mixed":
		o := option.HTTPMixedInboundOptions{ListenOptions: base}
		o.TLS = buildTLS(cfg.Raw)
		return option.Inbound{Type: C.TypeMixed, Tag: tag, Options: &o}, nil
	case "shadowtls":
		o := option.ShadowTLSInboundOptions{ListenOptions: base}
		o.Version = 3
		if v, ok := cfg.Raw["version"].(float64); ok && v > 0 {
			o.Version = int(v)
		}
		// 外层密码是节点级的，不逐用户区分。
		//
		// 这不是偷懒：ShadowTLS 的服务端用户表在构造时就固定了，
		// 上游没有提供热更新入口。真要逐用户区分，每次有人到期都得重建入站，
		// 全节点断线。外壳只管「像不像在访问那个真网站」，
		// 谁是谁交给内层协议判断 —— 这也是主流部署的做法。
		pw, _ := cfg.Raw["password"].(string)
		if pw == "" {
			return option.Inbound{}, fmt.Errorf("shadowtls 需要 password")
		}
		// v3 走 Users 列表，裸 Password 字段只对 v2 有效 ——
		// 给 v3 设 Password 的话服务端会以 "missing users" 拒绝启动
		if o.Version >= 3 {
			o.Users = []option.ShadowTLSUser{{Name: "default", Password: pw}}
		} else {
			o.Password = pw
		}
		hs, _ := cfg.Raw["handshake_server"].(string)
		if hs == "" {
			// 伪装目标必须显式指定：随便挑一个默认值意味着
			// 全网所有用这个面板的节点都伪装成同一个站点，
			// 那本身就成了一个可以批量识别的特征
			return option.Inbound{}, fmt.Errorf("shadowtls 需要 handshake_server")
		}
		hsPort := uint16(443)
		if v, ok := cfg.Raw["handshake_port"].(float64); ok && v > 0 {
			hsPort = uint16(v)
		}
		o.Handshake = option.ShadowTLSHandshakeOptions{
			ServerOptions: option.ServerOptions{Server: hs, ServerPort: hsPort},
		}
		o.Detour = shadowTLSInnerTag(tag)
		return option.Inbound{Type: C.TypeShadowTLS, Tag: tag, Options: &o}, nil

	case "naive":
		o := NaiveOptions{Masquerade: parseMasquerade(cfg.Raw)}
		o.ListenOptions = base
		o.TLS = buildTLS(cfg.Raw)
		if o.TLS == nil {
			// 不带 TLS 的 Naive 就是明文 h2c，伪装成普通网站这一核心
			// 卖点直接消失，不如不开
			return option.Inbound{}, fmt.Errorf("naive 必须启用 TLS")
		}
		return option.Inbound{Type: C.TypeNaive, Tag: tag, Options: &o}, nil
	default:
		return option.Inbound{}, fmt.Errorf("暂不支持的协议 %q", cfg.Protocol)
	}
}

// buildTLS 从面板下发的原始配置里取出 TLS 设置。
// 字段名沿用 UniProxy 的约定，便于与现成节点端的配置互通。
func buildTLS(raw map[string]any) *option.InboundTLSOptions {
	if raw == nil {
		return nil
	}
	enabled := false
	switch v := raw["tls"].(type) {
	case bool:
		enabled = v
	case float64:
		enabled = v > 0
	}
	if !enabled {
		return nil
	}
	o := &option.InboundTLSOptions{Enabled: true}
	if s, ok := raw["server_name"].(string); ok {
		o.ServerName = s
	}
	if s, ok := raw["cert_path"].(string); ok {
		o.CertificatePath = s
	}
	if s, ok := raw["key_path"].(string); ok {
		o.KeyPath = s
	}
	return o
}

//------------------------------------------------------------------------------
// core.Core 实现
//------------------------------------------------------------------------------

func (c *Core) AddInbound(cfg *core.InboundConfig) error {
	// 先单独试一次翻译。不合法的配置连表都不该进 ——
	// 一旦进了表，后续每一次重建都会被它绊倒。
	if _, _, err := buildInbound(cfg.Tag, cfg); err != nil {
		return fmt.Errorf("入站 %s 配置无效: %w", cfg.Tag, err)
	}
	c.mu.Lock()
	prev, had := c.inbounds[cfg.Tag]
	c.inbounds[cfg.Tag] = cfg
	c.mu.Unlock()

	if err := c.rebuild(); err != nil {
		// 回滚。
		//
		// 上面的预校验只能查出配置「翻译不出来」，查不出「翻译出来了但内核
		// 拒绝启动」——ShadowTLS 少配用户就是这种，语法完全合法。
		// 不回滚的话这份坏配置会一直留在表里，此后每一次重建（别的节点改配置、
		// 改分流）都会被它绊倒，故障范围从一个节点扩散到整台机器。
		c.mu.Lock()
		if had {
			c.inbounds[cfg.Tag] = prev
		} else {
			delete(c.inbounds, cfg.Tag)
			delete(c.users, cfg.Tag)
			delete(c.routing, cfg.Tag)
		}
		c.mu.Unlock()
		// 用回滚后的配置恢复现场，否则内核里留着的是失败那一刻的中间状态
		if rerr := c.rebuild(); rerr != nil {
			c.logger.Error("入站加入失败后回滚也失败", "入站", cfg.Tag, "err", rerr)
		}
		return err
	}
	return nil
}

func (c *Core) DelInbound(tag string) error {
	c.mu.Lock()
	delete(c.inbounds, tag)
	delete(c.users, tag)
	delete(c.routing, tag)
	c.mu.Unlock()
	return c.rebuild()
}

func (c *Core) AddUsers(tag string, users []core.User) error {
	c.mu.Lock()
	mi, ok := c.managed[tag]
	if ok {
		c.users[tag] = mergeUsers(c.users[tag], users)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("入站 %s 不存在或不支持用户管理", tag)
	}
	return mi.AddUsers(users)
}

// mergeUsers 按 UUID 去重合并，保持既有顺序。
// 顺序要稳：重放时它决定用户索引，索引错位会让流量记到别人账上。
func mergeUsers(base, add []core.User) []core.User {
	seen := make(map[string]bool, len(base))
	for _, u := range base {
		seen[u.UUID] = true
	}
	for _, u := range add {
		if u.UUID == "" || seen[u.UUID] {
			continue
		}
		seen[u.UUID] = true
		base = append(base, u)
	}
	return base
}

// upsertUsers 按 UUID 覆盖或追加用户，保持既有顺序。
func upsertUsers(base, add []core.User) []core.User {
	seen := make(map[string]int, len(base))
	for i, u := range base {
		seen[u.UUID] = i
	}
	for _, u := range add {
		if u.UUID == "" {
			continue
		}
		if idx, exists := seen[u.UUID]; exists {
			base[idx] = u
		} else {
			seen[u.UUID] = len(base)
			base = append(base, u)
		}
	}
	return base
}

func (c *Core) UpsertUsers(tag string, users []core.User) error {
	c.mu.Lock()
	mi, ok := c.managed[tag]
	if ok {
		c.users[tag] = upsertUsers(c.users[tag], users)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("入站 %s 不存在或不支持用户管理", tag)
	}
	return mi.UpsertUsers(users)
}

func (c *Core) DelUsers(tag string, uuids []string) error {
	c.mu.Lock()
	mi, ok := c.managed[tag]
	if ok {
		drop := make(map[string]bool, len(uuids))
		for _, id := range uuids {
			drop[id] = true
		}
		kept := c.users[tag][:0]
		for _, u := range c.users[tag] {
			if !drop[u.UUID] {
				kept = append(kept, u)
			}
		}
		c.users[tag] = kept
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("入站 %s 不存在或不支持用户管理", tag)
	}
	return mi.DelUsers(uuids)
}

func (c *Core) GetTraffic(tag string) ([]core.UserTraffic, error) {
	c.mu.RLock()
	mi, ok := c.managed[tag]
	c.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return mi.Traffic(), nil
}

func (c *Core) OnlineIPs(tag string) map[int64][]string {
	c.mu.RLock()
	mi, ok := c.managed[tag]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	return mi.Online()
}

var _ core.Core = (*Core)(nil)
