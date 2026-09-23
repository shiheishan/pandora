// Package multi preserves the transitional core.Core dispatcher. Production
// uses Pandora NativeCore directly; this package is retained only for an
// explicit compatibility build while protocol coverage is migrated.
package multi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/external"
	"github.com/aegispanel/nodeagent/core/mieru"
	"github.com/aegispanel/nodeagent/core/sing"
	"github.com/aegispanel/nodeagent/core/xray"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
)

// standalone 是不依赖通用内核的独立入站需要满足的接口。
type standalone interface {
	Start() error
	Close() error
	AddUsers(users []core.User) error
	UpsertUsers(users []core.User) error
	DelUsers(uuids []string) error
	Traffic() []core.UserTraffic
	Online() map[int64][]string
}

type Core struct {
	sing   *sing.Core
	xray   *xray.Core
	native *nativekernel.NativeCore
	log    *slog.Logger

	mu      sync.RWMutex
	startMu sync.Mutex
	alone   map[string]standalone // tag -> 独立入站
	// routes 记录每个 tag 归属哪个通用内核。
	// 没有它的话，取流量时就得挨个内核问一遍，
	// 而「问错内核」和「这个用户没流量」在返回值上无法区分。
	routes        map[string]core.Core
	ctx           context.Context
	started       bool
	singStarted   bool
	xrayStarted   bool
	nativeStarted bool
	nativeOnly    bool
}

func New(log *slog.Logger) *Core {
	// NativeCore is the production default. Compatibility cores remain an
	// explicit migration opt-in through NewWithOptions(Options{NativeOnly:false})
	// and are never started merely because the process was constructed.
	return NewWithOptions(log, Options{NativeOnly: true})
}

// Options controls the compatibility boundary. NativeOnly is intended for
// production deployments that would rather reject an unverified combination
// than put its data plane on a third-party core.
type Options struct {
	NativeOnly bool
}

func NewWithOptions(log *slog.Logger, options Options) *Core {
	return &Core{
		sing:       sing.New(log),
		xray:       xray.New(log),
		native:     nativekernel.NewNativeCore(nil),
		log:        log,
		alone:      make(map[string]standalone),
		routes:     make(map[string]core.Core),
		nativeOnly: options.NativeOnly,
	}
}

func (c *Core) Type() string {
	if c.nativeOnly {
		return "pandora-native"
	}
	return "pandora-native+compat"
}

func (c *Core) NativeOnly() bool { return c.nativeOnly }

// InboundReady delegates readiness to the runtime that currently owns tag.
// Standalone compatibility adapters do not expose a generation-level probe,
// so they fail closed instead of being reported healthy from process state.
func (c *Core) InboundReady(tag string) error {
	c.mu.RLock()
	kernel := c.routes[tag]
	_, standalone := c.alone[tag]
	c.mu.RUnlock()
	if standalone {
		return fmt.Errorf("standalone inbound %q has no readiness probe", tag)
	}
	if kernel == nil {
		return fmt.Errorf("inbound %q has no runtime owner", tag)
	}
	probe, ok := kernel.(core.InboundReadiness)
	if !ok {
		return fmt.Errorf("runtime %q has no readiness probe", kernel.Type())
	}
	return probe.InboundReady(tag)
}

func (c *Core) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("multi core start context cannot be nil")
	}
	c.startMu.Lock()
	defer c.startMu.Unlock()
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	c.ctx = ctx
	c.started = true
	c.mu.Unlock()
	// NativeCore is the default and is cheap to start. Compatibility cores
	// remain stopped until an explicitly unsupported inbound needs them.
	if err := c.native.Start(ctx); err != nil {
		c.mu.Lock()
		c.started = false
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.nativeStarted = true
	c.mu.Unlock()
	return nil
}

func (c *Core) ensureStarted(kernel core.Core) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	c.mu.RLock()
	ctx, started := c.ctx, c.started
	singStarted, xrayStarted := c.singStarted, c.xrayStarted
	c.mu.RUnlock()
	if !started || ctx == nil {
		return fmt.Errorf("multi core has not been started")
	}
	switch kernel {
	case c.sing:
		if singStarted {
			return nil
		}
		if err := c.sing.Start(ctx); err != nil {
			return fmt.Errorf("start sing-box compatibility core: %w", err)
		}
		c.mu.Lock()
		c.singStarted = true
		c.mu.Unlock()
	case c.xray:
		if xrayStarted {
			return nil
		}
		if err := c.xray.Start(ctx); err != nil {
			return fmt.Errorf("start xray compatibility core: %w", err)
		}
		c.mu.Lock()
		c.xrayStarted = true
		c.mu.Unlock()
	}
	return nil
}

func (c *Core) Close() error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	c.mu.Lock()
	for _, in := range c.alone {
		_ = in.Close()
	}
	c.alone = make(map[string]standalone)
	c.routes = make(map[string]core.Core)
	c.started = false
	c.ctx = nil
	c.singStarted = false
	c.xrayStarted = false
	c.nativeStarted = false
	c.mu.Unlock()

	err := c.sing.Close()
	if xerr := c.xray.Close(); err == nil {
		err = xerr
	}
	if nerr := c.native.Close(); err == nil {
		err = nerr
	}
	return err
}

// AddInbound 决定由谁承载这个入站。
func (c *Core) AddInbound(cfg *core.InboundConfig) error {
	if cfg == nil {
		return fmt.Errorf("inbound config cannot be nil")
	}
	if reason := nativeBoundaryReason(cfg); reason != "" && (cfg.Kernel == "" || cfg.Kernel == "native" || cfg.Kernel == "pandora-native") {
		return fmt.Errorf("native kernel refuses inbound %q: %s", cfg.Tag, reason)
	}
	if c.nativeOnly {
		switch strings.ToLower(strings.TrimSpace(cfg.Kernel)) {
		case "", "native", "pandora-native":
			if !nativeCanServe(cfg) {
				return fmt.Errorf("native-only mode refuses inbound %q: configuration is outside the verified NativeCore matrix", cfg.Tag)
			}
		default:
			return fmt.Errorf("native-only mode refuses explicit compatibility kernel %q for inbound %q", cfg.Kernel, cfg.Tag)
		}
	}
	switch cfg.Protocol {
	case "mieru":
		if nativeCanServe(cfg) {
			break
		}
		if c.nativeOnly && cfg.Kernel == "" {
			return fmt.Errorf("native-only mode refuses mieru inbound %q", cfg.Tag)
		}
		transport := "TCP"
		if s, ok := cfg.Raw["transport"].(string); ok && s != "" {
			transport = s
		}
		return c.putStandalone(cfg.Tag, mieru.New(cfg.Tag, cfg.Port, transport, c.log))

	case "juicity":
		if nativeCanServe(cfg) {
			break
		}
		if c.nativeOnly && cfg.Kernel == "" {
			return fmt.Errorf("native-only mode refuses juicity inbound %q", cfg.Tag)
		}
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
	if err := c.ensureStarted(kernel); err != nil {
		return err
	}

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
			return err
		}
	}

	if err := kernel.AddInbound(cfg); err != nil {
		if hasOld && old != kernel {
			if restoreErr := old.AddInbound(cfg); restoreErr != nil {
				c.log.Error("新内核启动失败且旧内核恢复失败", "tag", cfg.Tag, "新内核", kernel.Type(), "err", err, "恢复错误", restoreErr)
			} else {
				c.mu.Lock()
				c.routes[cfg.Tag] = old
				c.mu.Unlock()
				c.log.Warn("新内核启动失败，已恢复旧内核入站", "tag", cfg.Tag, "err", err)
				return err
			}
		}
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

// ApplyInbound uses a runtime's generation-level apply contract when the
// selected kernel supports it. NativeCore therefore validates routing before
// replacing the old listener; compatibility kernels retain their existing
// best-effort sequence.
func (c *Core) ApplyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	if cfg == nil {
		return fmt.Errorf("inbound config cannot be nil")
	}
	kernel := c.pick(cfg)
	if err := c.ensureStarted(kernel); err != nil {
		return err
	}
	c.mu.RLock()
	old, hasOld := c.routes[cfg.Tag]
	c.mu.RUnlock()
	if !hasOld || old == kernel {
		if applier, ok := kernel.(core.ConfigApplier); ok {
			if err := applier.ApplyInbound(cfg, routing); err != nil {
				return err
			}
			c.mu.Lock()
			c.routes[cfg.Tag] = kernel
			c.mu.Unlock()
			return nil
		}
	}
	if err := c.AddInbound(cfg); err != nil {
		return err
	}
	return c.SetRouting(cfg.Tag, routing)
}

// xrayProtocols 是 xray 内核在本项目里真正实现了入站的协议。
//
// 这张表要和 core/xray/protocols.go 的 buildInbound 保持一致。
// 不一致的后果是分派器把入站交给一个会当场报「暂不支持协议」的内核，
// 而节点表现为「端口不监听」，日志里只有一行看不出因果的错误。
var xrayProtocols = map[string]bool{
	"vless": true, "vmess": true, "trojan": true,
}

var nativeProtocols = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true, "ss": true, "hysteria2": true, "tuic": true, "anytls": true, "socks": true, "http": true, "naive": true, "shadowtls": true, "mieru": true, "juicity": true,
}

// pick selects the runtime for an inbound. NativeCore is authoritative for
// every verified protocol/transport combination. Compatibility cores are only
// reachable from an explicit migration build and never serve as a silent
// fallback for a NativeCore boundary or malformed configuration.
func (c *Core) pick(cfg *core.InboundConfig) core.Core {
	// 管理员显式指定时照办。他可能正是为了绕开某个内核的毛病，
	// 这里替他改主意只会让现场排查更难。
	switch cfg.Kernel {
	case "pandora-native", "native":
		return c.native
	case "xray-core":
		return c.xray
	case "sing-box":
		return c.sing
	}
	if nativeCanServe(cfg) {
		return c.native
	}
	if nativeBoundaryReason(cfg) != "" {
		// A known protocol boundary must fail in NativeCore rather than
		// silently moving the data plane to xray/sing-box.
		return c.native
	}
	if c.nativeOnly && cfg.Kernel == "" {
		return c.native
	}

	if !xrayProtocols[cfg.Protocol] {
		return c.sing
	}
	if reason := xrayCannotServe(cfg); reason != "" {
		c.log.Info("按能力回落到 sing-box", "tag", cfg.Tag, "原因", reason)
		return c.sing
	}
	return c.xray
}

// nativeBoundaryReason identifies configurations that must not silently fall
// back to a third-party data plane.
func nativeBoundaryReason(cfg *core.InboundConfig) string {
	if cfg == nil {
		return ""
	}
	protocol := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	network, _ := cfg.Raw["network"].(string)
	if protocol == "vless" && strings.EqualFold(network, "xhttp-h3") {
		security, _ := cfg.Raw["security"].(string)
		allowExperimental, _ := cfg.Raw["allow_experimental"].(bool)
		if strings.EqualFold(security, "reality") && !allowExperimental {
			return "REALITY+XHTTP/H3 is experimental and requires allow_experimental=true"
		}
	}
	if protocol == "mieru" {
		transport, _ := cfg.Raw["transport"].(string)
		if transport != "" && !strings.EqualFold(transport, "tcp") && !strings.EqualFold(transport, "udp") {
			return "mieru native transport must be tcp or udp"
		}
	}
	if protocol == "juicity" {
		if network != "" && !strings.EqualFold(network, "udp") {
			return "juicity native transport must be udp"
		}
	}
	return ""
}

func nativeCanServe(cfg *core.InboundConfig) bool {
	if cfg == nil || !nativeProtocols[strings.ToLower(strings.TrimSpace(cfg.Protocol))] {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Protocol)) {
	case "vless":
		network, _ := cfg.Raw["network"].(string)
		return network == "" || strings.EqualFold(network, "tcp") || strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") || strings.EqualFold(network, "grpc") || strings.EqualFold(network, "xhttp") || strings.EqualFold(network, "xhttp-h3")
	case "trojan":
		network, _ := cfg.Raw["network"].(string)
		return network == "" || strings.EqualFold(network, "tcp") || strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") || strings.EqualFold(network, "grpc")
	case "vmess":
		network, _ := cfg.Raw["network"].(string)
		security, _ := cfg.Raw["security"].(string)
		return (network == "" || strings.EqualFold(network, "tcp") || strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") || strings.EqualFold(network, "grpc") || strings.EqualFold(network, "xhttp") || strings.EqualFold(network, "xhttp-h3")) && (security == "" || strings.EqualFold(security, "none") || strings.EqualFold(security, "zero") || strings.EqualFold(security, "aes-128-gcm") || strings.EqualFold(security, "chacha20-poly1305") || strings.EqualFold(security, "auto"))
	case "shadowsocks", "ss":
		network, _ := cfg.Raw["network"].(string)
		method, _ := cfg.Raw["method"].(string)
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-") {
			password, _ := cfg.Raw["password"].(string)
			if strings.EqualFold(network, "udp") && strings.Contains(password, ":") {
				return false
			}
			return network == "" || strings.EqualFold(network, "tcp") || strings.EqualFold(network, "udp")
		}
		return network == "" || strings.EqualFold(network, "tcp") || strings.EqualFold(network, "udp")
	case "hysteria2":
		if network, _ := cfg.Raw["network"].(string); network != "" && !strings.EqualFold(network, "udp") {
			return false
		}
		// Hysteria2 is a native-only candidate in auto mode. Missing TLS or
		// malformed obfs must reach the native validator and fail closed rather
		// than silently falling back to another wire implementation.
		return true
	case "tuic":
		if network, _ := cfg.Raw["network"].(string); network != "" && !strings.EqualFold(network, "udp") {
			return false
		}
		// TUIC authentication and TLS requirements are validated by the native
		// adapter so malformed configs fail closed in auto mode.
		return true
	case "anytls":
		if network, _ := cfg.Raw["network"].(string); network != "" && !strings.EqualFold(network, "tcp") {
			return false
		}
		// AnyTLS may carry UoT/UDP inside its authenticated TCP session. The
		// native adapter validates the outer TLS and UoT framing strictly.
		return true
	case "socks", "http":
		network, _ := cfg.Raw["network"].(string)
		return network == "" || strings.EqualFold(network, "tcp") || (strings.EqualFold(cfg.Protocol, "socks") && strings.EqualFold(network, "udp"))
	case "naive":
		network, _ := cfg.Raw["network"].(string)
		return network == "" || strings.EqualFold(network, "tcp")
	case "shadowtls":
		network, _ := cfg.Raw["network"].(string)
		return network == "" || strings.EqualFold(network, "tcp")
	case "mieru":
		transport, _ := cfg.Raw["transport"].(string)
		return transport == "" || strings.EqualFold(transport, "tcp") || strings.EqualFold(transport, "udp")
	case "juicity":
		network, _ := cfg.Raw["network"].(string)
		return network == "" || strings.EqualFold(network, "udp")
	default:
		return false
	}
}

// xrayCannotServe 返回非空字符串表示这份配置 xray 承载不了。
//
// 只列我们确知的差异。拿不准的一律交给 xray —— 它是这三个协议的
// 第一顺位，真跑不起来会在 AddInbound 明确报错，比静默降级好查。
func xrayCannotServe(cfg *core.InboundConfig) string {
	// REALITY 反过来只有 xray 有，这里不必判：它落在 xray 分支里。
	if s, _ := cfg.Raw["security"].(string); strings.EqualFold(s, "reality") {
		return ""
	}
	// 普通 TLS 在 xray 侧要 cert_path + key_path 两个文件；面板目前
	// 不下发证书路径，配了 tls=true 却没有文件的话 xray 会直接失败，
	// 而 sing-box 那边有自己的证书处理路径。
	switch v := cfg.Raw["tls"].(type) {
	case bool:
		if v && strFrom(cfg.Raw, "cert_path") == "" {
			return "启用了 TLS 但没有证书文件路径"
		}
	case float64:
		if v > 0 && strFrom(cfg.Raw, "cert_path") == "" {
			return "启用了 TLS 但没有证书文件路径"
		}
	}
	return ""
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

func (c *Core) UpsertUsers(tag string, users []core.User) error {
	if in, ok := c.lookupAlone(tag); ok {
		return in.UpsertUsers(users)
	}
	if k, ok := c.lookupKernel(tag); ok {
		return k.UpsertUsers(tag, users)
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
	kernel, ok := c.lookupKernel(inboundTag)
	if !ok {
		return nil
	}
	return kernel.SetRouting(inboundTag, r)
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
