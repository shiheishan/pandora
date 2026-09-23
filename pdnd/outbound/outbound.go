// Package outbound 是 pdnd 的出站层。
//
// 所有出站——直连、拒绝、以及各种中转协议——都实现同一个接口，由分流引擎
// 按标签选中。以前这层同样是两份：sing-box 那边靠它自己的 outbound 体系，
// xray 那边逐个翻译成 protobuf，同一条「中转到某台机器」的配置在两个内核上
// 走的是完全不同的代码。
//
// # 接口为什么长这样
//
// 不用 `DialContext(network, addr string) (net.Conn, error)` 那种通用形状，
// 是因为要容纳 Hysteria2 / TUIC / AnyTLS 这类基于 QUIC 的中转：
//
//   - 它们持有一条长期存在的 QUIC 会话，每个请求在会话上开一条多路复用流。
//     通用 Dialer 接口没有「会话」的位置，每次拨号重建会话会让延迟和握手
//     开销高到不可用。所以出站是有生命周期的对象，不是一个函数。
//   - 它们原生支持 UDP 中继，且 UDP 不是「拨一条连接」而是「拿一个包通道」。
//     硬塞进 net.Conn 会丢掉目标地址这一维，多目标 UDP（游戏、DNS）就废了。
//   - 目标地址用 sing 的 M.Socksaddr 而不是字符串：协议库全都说这个类型，
//     用字符串就得在每个出站里解析一遍，而「域名还是 IP」这个区分
//     恰恰是中转要保留的——把域名提前解析掉，等于让本地 DNS 决定
//     出口看到的目标，中转的意义就没了。
package outbound

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	M "github.com/sagernet/sing/common/metadata"
)

// Outbound 是一个出站目的地。
//
// 实现必须并发安全：同一个出站会被大量连接同时使用。
type Outbound interface {
	Tag() string
	Type() string
	outboundInstanceID() uint64

	// DialTCP 建立一条到 destination 的流。
	DialTCP(ctx context.Context, destination M.Socksaddr) (net.Conn, error)

	// ListenUDP 返回一个可以向 destination 收发数据报的通道。
	//
	// destination 是「主要目标」，用于中转协议建立会话；实际发包时
	// 每个数据报仍然带自己的地址，所以一个通道能服务多个目标。
	// 不支持 UDP 的出站返回 ErrUDPNotSupported，调用方据此拒绝连接
	// 而不是静默丢包——静默丢包的表现是「游戏进不去但网页正常」，
	// 极难定位。
	ListenUDP(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error)

	// Close 释放长期资源（QUIC 会话、连接池）。
	Close() error
}

var nextOutboundInstanceID atomic.Uint64

type instanceIdentity struct{ id atomic.Uint64 }

func (i *instanceIdentity) outboundInstanceID() uint64 {
	if id := i.id.Load(); id != 0 {
		return id
	}
	candidate := nextOutboundInstanceID.Add(1)
	if i.id.CompareAndSwap(0, candidate) {
		return candidate
	}
	return i.id.Load()
}

// ErrUDPNotSupported 表示这个出站不能承载 UDP。
var ErrUDPNotSupported = fmt.Errorf("该出站不支持 UDP")

// Options 是构造一个出站需要的全部输入。
//
// Settings 沿用面板下发的键名（server / server_port / uuid / password /
// tls / sni …）。这里不替各协议解释它们——协议差异留在各自的构造函数里，
// 加一个协议不必改这一层。
type Options struct {
	Tag      string
	Type     string
	Settings map[string]any

	// Dialer 是这个出站用来连接它上游服务器的拨号器。
	//
	// 中转出站自己也要先连上游，那一跳同样要遵守「优先 IPv4」这类策略，
	// 也可能需要再经过另一个出站（链式代理）。把它作为参数传进来，
	// 而不是让每个出站自己 net.Dial —— 后者会让链式代理无法实现。
	Dialer Dialer
}

// Dialer 是底层拨号能力。direct 出站实现它，中转出站消费它。
type Dialer interface {
	DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error)
	ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error)
}

//------------------------------------------------------------------------------
// 注册表
//------------------------------------------------------------------------------

type constructor func(Options) (Outbound, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]constructor{}
)

// Register 登记一种出站类型。
//
// 各协议在自己的文件里 init() 注册，这一层不认识任何具体协议 ——
// 以前那两套实现最麻烦的地方就是「加个协议要改三处 switch」，
// 而漏改一处的表现是配置能存下来、节点却当它不存在。
func Register(typeName string, ctor constructor) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[typeName]; dup {
		panic("出站类型重复注册: " + typeName)
	}
	registry[typeName] = ctor
}

// Types 返回已注册的类型，排序后返回，供日志和自检使用。
func Types() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// New 按类型构造一个出站。
func New(opts Options) (Outbound, error) {
	t := strings.ToLower(strings.TrimSpace(opts.Type))
	if t == "" {
		return nil, fmt.Errorf("出站 %q 没有指定类型", opts.Tag)
	}
	registryMu.RLock()
	ctor, ok := registry[t]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("不支持的出站类型 %q（已支持：%s）",
			t, strings.Join(Types(), "、"))
	}
	opts.Type = t
	return ctor(opts)
}

//------------------------------------------------------------------------------
// 出站集合
//------------------------------------------------------------------------------

// Set 是一个节点当前生效的全部出站。
//
// 换配置时整体替换而不是逐个增删：出站之间可能有依赖（链式代理），
// 逐个替换会经过一个「A 已换、B 还是旧的」的中间态，那期间的连接
// 走向是不确定的。
type Set struct {
	mu       sync.RWMutex
	updateMu sync.Mutex
	current  *generation
	// Instance IDs are monotonic, so a high-water mark is enough to reject
	// every previously owned or closed object without retaining that object.
	highWater uint64
}

type generation struct {
	mu      sync.Mutex
	byTag   map[string]Outbound
	refs    int
	retired bool
	done    chan struct{}
}

func newGeneration(src map[string]Outbound) *generation {
	owned := make(map[string]Outbound, len(src))
	for tag, current := range src {
		owned[tag] = current
	}
	return &generation{byTag: owned, done: make(chan struct{})}
}

func (g *generation) acquire(tag string) (Outbound, func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current, ok := g.byTag[tag]
	if !ok || g.retired {
		return nil, nil, false
	}
	g.refs++
	var once sync.Once
	return current, func() {
		once.Do(func() {
			g.mu.Lock()
			g.refs--
			if g.retired && g.refs == 0 {
				close(g.done)
			}
			g.mu.Unlock()
		})
	}, true
}

func (g *generation) retire() {
	g.mu.Lock()
	g.retired = true
	if g.refs == 0 {
		close(g.done)
	}
	g.mu.Unlock()
}

func NewSet() *Set { return &Set{current: newGeneration(nil)} }

// Replace 用新的一组出站替换旧的，并关闭被替换掉的。
//
// 返回值是关闭旧出站时遇到的错误，仅用于记日志：这时新的已经生效，
// 旧的关不掉最多是泄漏一条 QUIC 会话，不该让整次配置更新失败。
func (s *Set) Replace(next map[string]Outbound) []error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	nextGeneration := newGeneration(next)
	previousHighWater := s.highWater
	maxID, nextTag, reused := inspectGenerationIDs(nextGeneration.byTag, previousHighWater)
	if reused {
		if maxID > s.highWater {
			s.highWater = maxID
		}
		errs := []error{fmt.Errorf("出站实例不能跨 generation 复用（新标签 %q）；请为新配置构造新实例", nextTag)}
		errs = append(errs, closeRejectedGeneration(nextGeneration, previousHighWater)...)
		return errs
	}
	s.mu.RLock()
	old := s.current
	s.mu.RUnlock()
	if maxID > s.highWater {
		s.highWater = maxID
	}
	s.mu.Lock()
	s.current = nextGeneration
	s.mu.Unlock()
	old.retire()
	select {
	case <-old.done:
		return closeGeneration(old)
	default:
		go func() {
			<-old.done
			_ = closeGeneration(old)
		}()
		return nil
	}
}

// Acquire returns an outbound from one immutable generation. The caller must
// invoke release after the returned connection/packet connection no longer
// depends on that outbound. Replace waits for these leases before closing old
// session-bearing outbounds.
func (s *Set) Acquire(tag string) (Outbound, func(), bool) {
	s.mu.RLock()
	g := s.current
	o, release, ok := g.acquire(tag)
	s.mu.RUnlock()
	return o, release, ok
}

// DialTCP acquires the selected generation and releases it when the returned
// connection is closed. Runtime callers should prefer this over manual leases.
func (s *Set) DialTCP(ctx context.Context, tag string, destination M.Socksaddr) (net.Conn, error) {
	o, release, ok := s.Acquire(tag)
	if !ok {
		return nil, fmt.Errorf("出站 %q 不存在", tag)
	}
	conn, err := o.DialTCP(ctx, destination)
	if err != nil {
		release()
		return nil, err
	}
	return &leasedConn{Conn: conn, release: release}, nil
}

func (s *Set) ListenUDP(ctx context.Context, tag string, destination M.Socksaddr) (net.PacketConn, error) {
	o, release, ok := s.Acquire(tag)
	if !ok {
		return nil, fmt.Errorf("出站 %q 不存在", tag)
	}
	conn, err := o.ListenUDP(ctx, destination)
	if err != nil {
		release()
		return nil, err
	}
	return &leasedPacketConn{PacketConn: conn, release: release}, nil
}

type leasedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *leasedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

type leasedPacketConn struct {
	net.PacketConn
	once    sync.Once
	release func()
}

func (c *leasedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.release)
	return err
}

// Tags 返回当前所有出站标签，供分流引擎校验规则引用。
func (s *Set) Tags() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.current.byTag))
	for k := range s.current.byTag {
		out[k] = true
	}
	return out
}

func (s *Set) Close() error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	s.mu.Lock()
	old := s.current
	s.current = newGeneration(nil)
	s.mu.Unlock()
	old.retire()
	select {
	case <-old.done:
		errs := closeGeneration(old)
		if len(errs) > 0 {
			return errs[0]
		}
	default:
		go func() {
			<-old.done
			_ = closeGeneration(old)
		}()
	}
	return nil
}

func closeGeneration(g *generation) []error {
	unique := make([]Outbound, 0, len(g.byTag))
	var errs []error
	for tag, current := range g.byTag {
		if outboundInSlice(unique, current) {
			continue
		}
		unique = append(unique, current)
		if err := current.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭出站 %s: %w", tag, err))
		}
	}
	return errs
}

func outboundInSlice(all []Outbound, target Outbound) bool {
	for _, current := range all {
		if sameOutbound(current, target) {
			return true
		}
	}
	return false
}

func inspectGenerationIDs(next map[string]Outbound, highWater uint64) (uint64, string, bool) {
	maxID := highWater
	var reusedTag string
	for tag, current := range next {
		id := current.outboundInstanceID()
		if id > maxID {
			maxID = id
		}
		if id <= highWater && reusedTag == "" {
			reusedTag = tag
		}
	}
	return maxID, reusedTag, reusedTag != ""
}

func closeRejectedGeneration(g *generation, previousHighWater uint64) []error {
	unique := make([]Outbound, 0, len(g.byTag))
	var errs []error
	for tag, current := range g.byTag {
		if current.outboundInstanceID() <= previousHighWater || outboundInSlice(unique, current) {
			continue
		}
		unique = append(unique, current)
		if err := current.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关闭未采用的出站 %s: %w", tag, err))
		}
	}
	return errs
}

func sameOutbound(a, b Outbound) bool {
	return a != nil && b != nil && a.outboundInstanceID() == b.outboundInstanceID()
}

//------------------------------------------------------------------------------
// 配置读取的小工具
//
// 面板下发的是 JSON 解出来的 map[string]any，数字一律是 float64。
// 每个出站都要做同样的取值与类型归一，放在这里一次写好。
//------------------------------------------------------------------------------

func Str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func Int(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	}
	return 0
}

func Bool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case float64:
		return v > 0
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func Strings(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []string:
		return v
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// Server 取出上游服务器地址。中转类出站都需要它，校验也一样。
func Server(m map[string]any) (M.Socksaddr, error) {
	host := Str(m, "server")
	port := Int(m, "server_port")
	if host == "" {
		return M.Socksaddr{}, fmt.Errorf("缺少 server")
	}
	if port < 1 || port > 65535 {
		return M.Socksaddr{}, fmt.Errorf("server_port %d 不合法", port)
	}
	return M.ParseSocksaddrHostPort(host, uint16(port)), nil
}
