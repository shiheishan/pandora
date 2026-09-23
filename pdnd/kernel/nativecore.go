package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// NativeCore is the control/data-plane owner for Pandora's native protocol
// adapters. It implements the existing node Core contract so the panel client
// can migrate without knowing which protocol adapter is active.
type NativeCore struct {
	mu       sync.RWMutex
	ctx      context.Context
	started  bool
	closed   bool
	registry *AdapterRegistry
	inbounds map[string]*nativeInbound
	// desired records the newest AddInbound/DelInbound intent for each tag.
	// An adapter can take a while to bind its listener; checking this epoch
	// before publishing prevents an older start from resurrecting after a
	// newer replacement or deletion.
	desired  map[string]uint64
	seq      uint64
	applyMu  sync.Mutex
	applyTag map[string]*sync.Mutex
}

type nativeInbound struct {
	mu      sync.RWMutex
	spec    InboundSpec
	routing *core.Routing
	adapter Adapter
	plane   *routedDataPlane
	runtime *Runtime
	retired bool
}

type routedDataPlane struct {
	mu      sync.RWMutex
	current *Runtime
}

func (p *routedDataPlane) swap(next *Runtime) *Runtime {
	p.mu.Lock()
	old := p.current
	p.current = next
	p.mu.Unlock()
	return old
}

func (p *routedDataPlane) DialTCP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.Conn, error) {
	p.mu.RLock()
	r := p.current
	p.mu.RUnlock()
	if r == nil {
		return nil, fmt.Errorf("入站路由 runtime 尚未就绪")
	}
	return r.DialTCP(ctx, meta, destination)
}

func (p *routedDataPlane) ListenUDP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.mu.RLock()
	r := p.current
	p.mu.RUnlock()
	if r == nil {
		return nil, fmt.Errorf("入站路由 runtime 尚未就绪")
	}
	return r.ListenUDP(ctx, meta, destination)
}

func NewNativeCore(registry *AdapterRegistry) *NativeCore {
	if registry == nil {
		registry = NewDefaultAdapterRegistry()
	}
	return &NativeCore{
		registry: registry,
		inbounds: make(map[string]*nativeInbound),
		desired:  make(map[string]uint64),
		applyTag: make(map[string]*sync.Mutex),
	}
}

var _ core.Core = (*NativeCore)(nil)
var _ core.InboundReadiness = (*NativeCore)(nil)

func (c *NativeCore) Type() string { return "pandora-native" }

// InboundReady verifies that tag still points at a published, non-retired
// native generation with all runtime resources attached. It intentionally
// does not perform external loopback traffic: that belongs to the separate
// protocol interoperability gate and must not turn the periodic health path
// into synthetic user traffic.
func (c *NativeCore) InboundReady(tag string) error {
	c.mu.RLock()
	started, closed := c.started, c.closed
	in := c.inbounds[tag]
	c.mu.RUnlock()
	if !started || closed {
		return fmt.Errorf("native core is not serving")
	}
	if in == nil {
		return fmt.Errorf("inbound %q is not published", tag)
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	if in.retired {
		return fmt.Errorf("inbound %q is retired", tag)
	}
	if in.adapter == nil || in.runtime == nil || in.plane == nil {
		return fmt.Errorf("inbound %q runtime is incomplete", tag)
	}
	return nil
}

// CapabilityReport exposes the immutable protocol contract without exposing
// adapter instances or compatibility-core state. It is safe to call before
// Start and is intended for panel/node negotiation and diagnostics.
func (c *NativeCore) CapabilityReport() NativeCapabilityReport {
	return NativeCapabilityReportFor(true)
}

func (c *NativeCore) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("启动 context 不能为空")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("原生内核已关闭")
	}
	if c.started {
		return nil
	}
	c.ctx = ctx
	c.started = true
	return nil
}

func (c *NativeCore) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	inbounds := make([]*nativeInbound, 0, len(c.inbounds))
	for tag, in := range c.inbounds {
		inbounds = append(inbounds, in)
		delete(c.inbounds, tag)
	}
	c.mu.Unlock()

	var first error
	for _, in := range inbounds {
		if err := closeNativeInbound(in); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (c *NativeCore) AddInbound(cfg *core.InboundConfig) error {
	return c.applyInbound(cfg, nil)
}

func (c *NativeCore) ApplyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	return c.applyInbound(cfg, routing)
}

func (c *NativeCore) applyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	if cfg == nil {
		return fmt.Errorf("入站配置不能为空")
	}
	if cfg.Tag == "" {
		return fmt.Errorf("入站缺少 tag")
	}
	tagLock := c.inboundApplyLock(cfg.Tag)
	tagLock.Lock()
	defer tagLock.Unlock()
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("入站 %s 端口 %d 无效", cfg.Tag, cfg.Port)
	}
	c.mu.RLock()
	ctx, started, closed := c.ctx, c.started, c.closed
	c.mu.RUnlock()
	if closed || !started {
		return fmt.Errorf("原生内核尚未启动或已关闭")
	}

	c.mu.RLock()
	_, previousExists := c.inbounds[cfg.Tag]
	c.mu.RUnlock()

	// Validate the entire candidate before advancing desired. Otherwise a
	// malformed later update can supersede a valid generation still starting.
	spec := InboundSpec{Config: *cfg}
	adapter, err := c.registry.New(spec)
	if err != nil {
		return &core.ConfigApplyError{Err: err, PreviousPreserved: previousExists}
	}
	// Compile the complete routing generation before touching the old listener.
	// A malformed route must not turn a configuration update into an outage.
	runtime, err := Build(routing)
	if err != nil {
		_ = adapter.Close()
		return &core.ConfigApplyError{Err: err, PreviousPreserved: previousExists}
	}
	c.mu.Lock()
	if c.closed || !c.started {
		c.mu.Unlock()
		_ = runtime.Close()
		_ = adapter.Close()
		return fmt.Errorf("native core is not started or is closed")
	}
	c.seq++
	spec.Generation = c.seq
	c.desired[cfg.Tag] = spec.Generation
	ctx = c.ctx
	c.mu.Unlock()

	// 替换同一个 tag 之前必须先把旧入站关掉。
	//
	// 这里原本是「先起新的，成功了再关旧的」——听上去更稳，实际上根本
	// 起不来：两个 socket 不能 bind 同一个地址，而我们没有开
	// SO_REUSEPORT（对 UDP 也不该开，内核会在两个 socket 之间分发包，
	// 同一条会话的报文可能被丢给正在退场的那个实例）。
	//
	// 症状是管理员在面板改一次节点配置，节点端拉到新配置、bind 失败、
	// 整个入站就没了，直到有人手动重启服务。改配置变成一次事故。
	//
	// 代价是中间有个毫秒级的空窗：旧的已关、新的没起。这个代价换的是
	// 「改配置能生效」，值得。空窗期内新连接会被拒，已建立的连接由旧
	// 入站的 Close 负责收尾。
	c.mu.Lock()
	previous := c.inbounds[cfg.Tag]
	delete(c.inbounds, cfg.Tag)
	c.mu.Unlock()
	if previous != nil {
		_ = closeNativeInbound(previous)
	}

	plane := &routedDataPlane{current: runtime}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		_ = runtime.Close()
		_ = adapter.Close()
		startErr := fmt.Errorf("启动原生协议 %s: %w", cfg.Protocol, err)
		if previous == nil {
			return startErr
		}
		restored, restoreErr := c.restoreInbound(ctx, previous, spec.Generation)
		if restoreErr != nil {
			return errors.Join(startErr, fmt.Errorf("restore previous inbound: %w", restoreErr))
		}
		c.mu.Lock()
		if !c.closed && c.desired[cfg.Tag] == spec.Generation {
			c.inbounds[cfg.Tag] = restored
			c.mu.Unlock()
			return &core.ConfigApplyError{Err: startErr, PreviousPreserved: true}
		}
		c.mu.Unlock()
		_ = closeNativeInbound(restored)
		return startErr
	}

	newInbound := &nativeInbound{
		spec: spec, routing: routing, adapter: adapter, plane: plane, runtime: runtime,
	}
	c.mu.Lock()
	if c.closed || c.desired[cfg.Tag] != spec.Generation {
		c.mu.Unlock()
		_ = closeNativeInbound(newInbound)
		return fmt.Errorf("inbound %q start was superseded or core closed", cfg.Tag)
	}
	// 并发替换时，晚到的那次可能在我们 Start 期间又塞了一个进来。
	// 上面的 generation 检查已经挡住了「我们是旧的」这种情况，这里
	// 兜住剩下的：真有残留就关掉，不能泄漏一个还在监听的 socket。
	stale := c.inbounds[cfg.Tag]
	c.inbounds[cfg.Tag] = newInbound
	c.mu.Unlock()
	if stale != nil {
		_ = closeNativeInbound(stale)
	}
	return nil
}

func (c *NativeCore) inboundApplyLock(tag string) *sync.Mutex {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	lock := c.applyTag[tag]
	if lock == nil {
		lock = &sync.Mutex{}
		c.applyTag[tag] = lock
	}
	return lock
}

func (c *NativeCore) restoreInbound(ctx context.Context, previous *nativeInbound, generation uint64) (*nativeInbound, error) {
	spec := previous.spec
	spec.Generation = generation
	adapter, err := c.registry.New(spec)
	if err != nil {
		return nil, err
	}
	runtime, err := Build(previous.routing)
	if err != nil {
		_ = adapter.Close()
		return nil, err
	}
	plane := &routedDataPlane{current: runtime}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		_ = runtime.Close()
		_ = adapter.Close()
		return nil, err
	}
	return &nativeInbound{
		spec: spec, routing: previous.routing, adapter: adapter, plane: plane, runtime: runtime,
	}, nil
}

func (c *NativeCore) DelInbound(tag string) error {
	tagLock := c.inboundApplyLock(tag)
	tagLock.Lock()
	defer tagLock.Unlock()
	c.mu.Lock()
	// Invalidate an adapter that is still starting before removing the
	// currently published generation.
	c.seq++
	c.desired[tag] = c.seq
	in := c.inbounds[tag]
	delete(c.inbounds, tag)
	c.mu.Unlock()
	if in == nil {
		return nil
	}
	return closeNativeInbound(in)
}

func (c *NativeCore) AddUsers(tag string, users []core.User) error {
	in, err := c.getInbound(tag)
	if err != nil {
		return err
	}
	in.mu.RLock()
	if in.retired {
		in.mu.RUnlock()
		return fmt.Errorf("鍏ョ珯 %q 宸插�€閫€", tag)
	}
	err = in.adapter.AddUsers(users)
	in.mu.RUnlock()
	if err != nil {
		return err
	}
	return nil
}

func (c *NativeCore) UpsertUsers(tag string, users []core.User) error {
	in, err := c.getInbound(tag)
	if err != nil {
		return err
	}
	in.mu.RLock()
	if in.retired {
		in.mu.RUnlock()
		return fmt.Errorf("入站 %q 已退役", tag)
	}
	err = in.adapter.UpsertUsers(users)
	in.mu.RUnlock()
	return err
}

func (c *NativeCore) DelUsers(tag string, uuids []string) error {
	in, err := c.getInbound(tag)
	if err != nil {
		return err
	}
	in.mu.RLock()
	if in.retired {
		in.mu.RUnlock()
		return fmt.Errorf("鍏ョ珯 %q 宸插�€閫€", tag)
	}
	err = in.adapter.DelUsers(uuids)
	in.mu.RUnlock()
	if err != nil {
		return err
	}
	return nil
}

func (c *NativeCore) GetTraffic(tag string) ([]core.UserTraffic, error) {
	in, err := c.getInbound(tag)
	if err != nil {
		return nil, err
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	if in.retired {
		return nil, fmt.Errorf("鍏ョ珯 %q 宸插�€閫€", tag)
	}
	return in.adapter.SnapshotTraffic()
}

func (c *NativeCore) OnlineIPs(tag string) map[int64][]string {
	in, err := c.getInbound(tag)
	if err != nil {
		return nil
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	if in.retired {
		return nil
	}
	return in.adapter.OnlineIPs()
}

func (c *NativeCore) SetRouting(tag string, cfg *core.Routing) error {
	in, err := c.getInbound(tag)
	if err != nil {
		return err
	}
	next, err := Build(cfg)
	if err != nil {
		return err
	}
	in.mu.Lock()
	if in.retired {
		in.mu.Unlock()
		_ = next.Close()
		return fmt.Errorf("鍏ョ珯 %q 宸插�€閫€", tag)
	}
	old := in.plane.swap(next)
	in.mu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

func closeNativeInbound(in *nativeInbound) error {
	if in == nil {
		return nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.retired {
		return nil
	}
	in.retired = true
	var first error
	if err := in.adapter.Close(); err != nil {
		first = err
	}
	if err := in.runtime.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

func (c *NativeCore) getInbound(tag string) (*nativeInbound, error) {
	c.mu.RLock()
	in := c.inbounds[tag]
	c.mu.RUnlock()
	if in == nil {
		return nil, fmt.Errorf("入站 %q 不存在", tag)
	}
	return in, nil
}
