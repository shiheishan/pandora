package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// connErrors 是全部入站共用的连接失败日志出口，见 connerror_log.go。
	connErrors *connErrorLogSink
	// ports 是进程级端口登记表：(端口, L4) → 占着它的入站 tag。tagPorts 是每个
	// 入站当前名下的那一个键。都由 mu 保护，见 port_claims.go。
	ports    map[portKey]string
	tagPorts map[string]portKey
	// owners 记每个入站属于哪个面板的哪个节点，只用于冲突文案（SetInboundOwner）。
	owners map[string]inboundOwner
	// retiredTraffic 是已退场入站（换代、删除、关停）还没交出去的流量，按 tag 记，
	// 下一次 GetTraffic 一并取走。由 mu 保护。
	retiredTraffic map[string][]core.UserTraffic
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
	conn, err := r.DialTCP(ctx, meta, destination)
	return conn, markConnError(connErrUpstream, err)
}

func (p *routedDataPlane) ListenUDP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.mu.RLock()
	r := p.current
	p.mu.RUnlock()
	if r == nil {
		return nil, fmt.Errorf("入站路由 runtime 尚未就绪")
	}
	conn, err := r.ListenUDP(ctx, meta, destination)
	return conn, markConnError(connErrUpstream, err)
}

// NewNativeCore 用 slog.Default() 记连接失败，给测试与不关心日志的调用方。
func NewNativeCore(registry *AdapterRegistry) *NativeCore {
	return NewNativeCoreWithLogger(registry, nil)
}

// NewNativeCoreWithLogger 是生产入口：main 的 newLogger 经 newRuntime 传进来，
// 入站连接失败与进程其余日志同级别、同格式、同一个出口。log 为 nil 时用 slog.Default()。
func NewNativeCoreWithLogger(registry *AdapterRegistry, log *slog.Logger) *NativeCore {
	if registry == nil {
		registry = NewDefaultAdapterRegistry()
	}
	if log == nil {
		log = slog.Default()
	}
	return &NativeCore{
		registry:   registry,
		inbounds:   make(map[string]*nativeInbound),
		desired:    make(map[string]uint64),
		applyTag:   make(map[string]*sync.Mutex),
		connErrors: newConnErrorLogSink(log, connErrorLogBurst, connErrorLogWindow),
		ports:      make(map[portKey]string),
		tagPorts:   make(map[string]portKey),
		owners:     make(map[string]inboundOwner),

		retiredTraffic: make(map[string][]core.UserTraffic),
	}
}

// adapterHooks 是两处 adapter.Start（新起与回滚恢复）共用的钩子装配，
// 保证恢复出来的旧入站同样有失败观测，不会因为走了回滚分支就重新变哑。
func (c *NativeCore) adapterHooks(plane DataPlane) AdapterHooks {
	return AdapterHooks{DataPlane: plane, OnConnError: c.connErrors.Report}
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

// Close 关停全部入站。需要最后一轮流量的调用方用 CloseAndDrainTraffic。
// 入站全部关完再收日志出口，关闭过程中的最后几条失败也能进摘要。
func (c *NativeCore) Close() error {
	_, err := c.CloseAndDrainTraffic()
	return err
}

func (c *NativeCore) AddInbound(cfg *core.InboundConfig) error {
	return c.applyInbound(cfg, nil, nil)
}

func (c *NativeCore) ApplyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	return c.applyInbound(cfg, routing, nil)
}

// ApplyInboundWithUsers 同 ApplyInbound，但先把 users 装进新适配器、再开始 accept
// （startup_users.go）：冷启动与入站重建时不再有「监听开了、名单还是空的」窗口。
func (c *NativeCore) ApplyInboundWithUsers(cfg *core.InboundConfig, routing *core.Routing, users []core.User) error {
	return c.applyInbound(cfg, routing, users)
}

func (c *NativeCore) applyInbound(cfg *core.InboundConfig, routing *core.Routing, users []core.User) error {
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

	// 端口先到先得：别的入站占着这个 (端口, L4) 就不启动，也不碰占着的那个。
	// 放在构造适配器之前，节点按退避重试时每次的代价只是一次查表。
	key := inboundPortKey(cfg)
	c.mu.RLock()
	_, previousExists := c.inbounds[cfg.Tag]
	var conflict *PortInUseError
	if owner := c.portOwnerLocked(key); owner != "" && owner != cfg.Tag {
		conflict = c.portConflictLocked(key, cfg.Tag, owner)
	}
	c.mu.RUnlock()
	if conflict != nil {
		return &core.ConfigApplyError{Err: conflict, PreviousPreserved: previousExists}
	}

	// Validate the entire candidate before advancing desired. Otherwise a
	// malformed later update can supersede a valid generation still starting.
	spec := InboundSpec{Config: *cfg}
	adapter, err := c.registry.New(spec)
	if err != nil {
		return &core.ConfigApplyError{Err: err, PreviousPreserved: previousExists}
	}
	// 名单装不上（某个用户凭据格式不对之类）不挡入站本身：与原先「装完入站再
	// 同步用户」失败时一样，入站照起、名单留给下一轮同步，错误单独交回。
	usersErr := preloadUsers(adapter, users)
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
	// 再查一次并登记：上面查表到这里之间，别的入站可能抢先登记了同一个键。
	if owner := c.portOwnerLocked(key); owner != "" && owner != cfg.Tag {
		conflict := c.portConflictLocked(key, cfg.Tag, owner)
		c.mu.Unlock()
		_ = runtime.Close()
		_ = adapter.Close()
		return &core.ConfigApplyError{Err: conflict, PreviousPreserved: previousExists}
	}
	// 先登记新端口，起来之后才释放旧端口（换端口时旧端口在新入站就绪前仍归本入站）。
	oldKey, hadOldKey := c.tagPorts[cfg.Tag]
	c.ports[key] = cfg.Tag
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
		c.stashRetiredTraffic(cfg.Tag, previous)
	}

	plane := &routedDataPlane{current: runtime}
	if err := adapter.Start(ctx, spec, c.adapterHooks(plane)); err != nil {
		_ = runtime.Close()
		_ = adapter.Close()
		startErr := fmt.Errorf("启动原生协议 %s: %w", cfg.Protocol, asExternalPortInUse(key, err))
		if previous == nil {
			// 没有旧入站可回退：这个 tag 名下不该再留任何登记。
			c.mu.Lock()
			c.releasePortLocked(cfg.Tag, key)
			if hadOldKey {
				c.releasePortLocked(cfg.Tag, oldKey)
			}
			delete(c.tagPorts, cfg.Tag)
			c.mu.Unlock()
			return startErr
		}
		restored, restoreErr := c.restoreInbound(ctx, previous, spec.Generation)
		if restoreErr != nil {
			// 新旧都没起来：这个入站已不在服务，名下的端口一并让出。
			c.mu.Lock()
			c.releasePortLocked(cfg.Tag, key)
			if hadOldKey {
				c.releasePortLocked(cfg.Tag, oldKey)
			}
			delete(c.tagPorts, cfg.Tag)
			c.mu.Unlock()
			return errors.Join(startErr, fmt.Errorf("restore previous inbound: %w", restoreErr))
		}
		c.mu.Lock()
		if !c.closed && c.desired[cfg.Tag] == spec.Generation {
			c.inbounds[cfg.Tag] = restored
			// 旧入站回来了：保留旧端口，让出刚登记的新端口。
			if !hadOldKey || key != oldKey {
				c.releasePortLocked(cfg.Tag, key)
			}
			c.mu.Unlock()
			return &core.ConfigApplyError{Err: startErr, PreviousPreserved: true}
		}
		c.mu.Unlock()
		_ = closeNativeInbound(restored)
		return startErr
	}

	if usersErr == nil {
		usersErr = postloadUsers(adapter, users)
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
	if hadOldKey && oldKey != key {
		c.releasePortLocked(cfg.Tag, oldKey)
	}
	c.tagPorts[cfg.Tag] = key
	c.mu.Unlock()
	if stale != nil {
		_ = closeNativeInbound(stale)
		c.stashRetiredTraffic(cfg.Tag, stale)
	}
	if usersErr != nil {
		return &UsersPreloadError{Err: usersErr}
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
	if err := adapter.Start(ctx, spec, c.adapterHooks(plane)); err != nil {
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
	if key, ok := c.tagPorts[tag]; ok {
		c.releasePortLocked(tag, key)
		delete(c.tagPorts, tag)
	}
	c.mu.Unlock()
	if in == nil {
		return nil
	}
	err := closeNativeInbound(in)
	c.stashRetiredTraffic(tag, in)
	return err
}

func (c *NativeCore) AddUsers(tag string, users []core.User) error {
	in, err := c.getInbound(tag)
	if err != nil {
		return err
	}
	in.mu.RLock()
	if in.retired {
		in.mu.RUnlock()
		return fmt.Errorf("入站 %q 已退役", tag)
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
		return fmt.Errorf("入站 %q 已退役", tag)
	}
	err = in.adapter.DelUsers(uuids)
	in.mu.RUnlock()
	if err != nil {
		return err
	}
	return nil
}

// GetTraffic 取出并清零 tag 的流量增量，连同这个 tag 已退场入站（换代、删除）
// 还没交出去的那部分。入站不在、但有退场流量时照样交出，不报错。
func (c *NativeCore) GetTraffic(tag string) ([]core.UserTraffic, error) {
	c.mu.Lock()
	retired := c.takeRetiredTrafficLocked(tag)
	in := c.inbounds[tag]
	c.mu.Unlock()
	if in == nil {
		if len(retired) > 0 {
			return retired, nil
		}
		return nil, fmt.Errorf("入站 %q 不存在", tag)
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	if in.retired {
		if len(retired) > 0 {
			return retired, nil
		}
		return nil, fmt.Errorf("入站 %q 已退役", tag)
	}
	live, err := in.adapter.SnapshotTraffic()
	if err != nil {
		// 退场流量放回去，别因为这一次读失败把它丢了。
		if len(retired) > 0 {
			c.mu.Lock()
			c.retiredTraffic[tag] = append(retired, c.retiredTraffic[tag]...)
			c.mu.Unlock()
		}
		return nil, err
	}
	return append(retired, live...), nil
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
		return fmt.Errorf("入站 %q 已退役", tag)
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
	// 限时：转发挂住的连接不能让换配置、停机卡死在旧适配器的 WaitGroup 上。
	if err := closeAdapterBounded(in.adapter); err != nil {
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
