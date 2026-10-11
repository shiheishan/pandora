package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/internal/confnum"
	hy2 "github.com/aegispanel/nodeagent/internal/nativewire/hysteria2"
	"github.com/aegispanel/nodeagent/route"
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

// hysteria2TLSConfig keeps the protocol adapter independent from sing-box's
// certificate loader while satisfying sing-quic's small TLS configuration
// contract. The QUIC service upgrades this config with HTTP/3 ALPN itself.
type hysteria2TLSConfig struct{ std *tls.Config }

func (c *hysteria2TLSConfig) ServerName() string     { return c.std.ServerName }
func (c *hysteria2TLSConfig) SetServerName(v string) { c.std.ServerName = v }
func (c *hysteria2TLSConfig) NextProtos() []string   { return append([]string(nil), c.std.NextProtos...) }
func (c *hysteria2TLSConfig) SetNextProtos(v []string) {
	c.std.NextProtos = append([]string(nil), v...)
}
func (c *hysteria2TLSConfig) STDConfig() (*tls.Config, error) {
	return c.std, nil
}
func (c *hysteria2TLSConfig) Client(conn net.Conn) (aTLS.Conn, error) {
	return &hysteria2TLSConn{Conn: tls.Client(conn, c.std)}, nil
}
func (c *hysteria2TLSConfig) Clone() aTLS.Config {
	return &hysteria2TLSConfig{std: c.std.Clone()}
}
func (c *hysteria2TLSConfig) Start() error { return nil }
func (c *hysteria2TLSConfig) Close() error { return nil }
func (c *hysteria2TLSConfig) Server(conn net.Conn) (aTLS.Conn, error) {
	return &hysteria2TLSConn{Conn: tls.Server(conn, c.std)}, nil
}

type hysteria2TLSConn struct{ *tls.Conn }

func (c *hysteria2TLSConn) NetConn() net.Conn { return c.Conn }

type hysteria2Slot struct {
	user   core.User
	active bool
}

type hysteria2Adapter struct {
	spec InboundSpec

	mu    sync.RWMutex
	users map[string]int
	slots []hysteria2Slot
	// sessions 登记 TCP 子流与 UDP 会话（删用户即断），流量也随搬随记在这里。
	sessions userSessions
	online   onlineDevices
	// udpQuota 限每用户在途 UDP 会话数（quic_udp_quota.go）。
	udpQuota  udpSessionQuota
	service   *hy2.Service[int]
	packet    net.PacketConn
	plane     DataPlane
	connErr   connErrorReporter
	limiters  core.SpeedLimiters
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	active    map[net.Conn]struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	updateMu  sync.Mutex

	salamander string
	upBPS      uint64
	downBPS    uint64
	udpTimeout time.Duration
}

var _ Adapter = (*hysteria2Adapter)(nil)
var _ N.TCPConnectionHandlerEx = (*hysteria2Adapter)(nil)
var _ N.UDPConnectionHandlerEx = (*hysteria2Adapter)(nil)

func newHysteria2Adapter(spec InboundSpec) (Adapter, error) {
	return &hysteria2Adapter{
		spec: spec, users: make(map[string]int),
		active: make(map[net.Conn]struct{}),
	}, nil
}

func (a *hysteria2Adapter) Protocol() string { return "hysteria2" }

func (a *hysteria2Adapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "hysteria2") {
		return fmt.Errorf("hysteria2 adapter received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("hysteria2 port is invalid")
	}
	if network := strings.TrimSpace(rawString(spec.Config.Raw, "network")); network != "" && !strings.EqualFold(network, "udp") {
		return fmt.Errorf("hysteria2 requires UDP transport, got %q", network)
	}
	if strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")) == "" || strings.TrimSpace(rawString(spec.Config.Raw, "key_path")) == "" {
		return fmt.Errorf("hysteria2 requires cert_path and key_path")
	}
	if raw, ok := spec.Config.Raw["obfs"]; ok && raw != nil {
		obj, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("hysteria2 obfs must be an object")
		}
		typ := strings.ToLower(strings.TrimSpace(rawString(obj, "type")))
		if typ != string(hy2.ObfsTypeSalamander) {
			return fmt.Errorf("hysteria2 only supports salamander obfs, got %q", typ)
		}
		if strings.TrimSpace(rawString(obj, "password")) == "" {
			return fmt.Errorf("hysteria2 salamander password is required")
		}
	}
	for _, key := range []string{"up_mbps", "down_mbps"} {
		if value, exists := spec.Config.Raw[key]; exists {
			n, ok := nonNegativeInt(value)
			if !ok {
				return fmt.Errorf("hysteria2 %s must be a non-negative integer", key)
			}
			_ = n
		}
	}
	if value, exists := spec.Config.Raw["udp_timeout"]; exists {
		if _, err := parseHysteriaDuration(value); err != nil {
			return fmt.Errorf("hysteria2 udp_timeout: %w", err)
		}
	}
	if _, err := hysteria2UDPQueueSize(spec.Config.Raw); err != nil {
		return err
	}
	return nil
}

// hysteria2UDPQueueSize 读可选的 udp_queue_size（每个 UDP 会话的接收队列长度，
// 16–65536，缺省 hy2.DefaultUDPQueueSize）。面板不下发它，只留给运维按机器调。
func hysteria2UDPQueueSize(raw map[string]any) (int, error) {
	value, exists := raw["udp_queue_size"]
	if !exists || value == nil {
		return hy2.DefaultUDPQueueSize, nil
	}
	n, ok := nonNegativeInt(value)
	if !ok || n < 16 || n > 65536 {
		return 0, fmt.Errorf("hysteria2 udp_queue_size must be an integer between 16 and 65536")
	}
	return n, nil
}

func (a *hysteria2Adapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("hysteria2 requires context and data plane")
	}
	if err := a.Validate(spec); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(rawString(spec.Config.Raw, "cert_path"), rawString(spec.Config.Raw, "key_path"))
	if err != nil {
		return fmt.Errorf("hysteria2 TLS certificate: %w", err)
	}
	stdTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	tlsConfig := &hysteria2TLSConfig{std: stdTLS}
	ctx, cancel := context.WithCancel(parent)
	connErr := newConnErrorReporter(hooks, spec, "hysteria2")

	var salamander string
	if obj, ok := spec.Config.Raw["obfs"].(map[string]any); ok {
		salamander = strings.TrimSpace(rawString(obj, "password"))
	}
	up, _ := nonNegativeInt(spec.Config.Raw["up_mbps"])
	down, _ := nonNegativeInt(spec.Config.Raw["down_mbps"])
	// A zero timeout would make sing-quic's canceler expire the first packet
	// read immediately. Keep the panel default explicit and allow a raw value
	// to override it.
	udpTimeout := 5 * time.Minute
	if value, exists := spec.Config.Raw["udp_timeout"]; exists {
		udpTimeout, err = parseHysteriaDuration(value)
		if err != nil {
			cancel()
			return fmt.Errorf("hysteria2 udp_timeout: %w", err)
		}
	}
	udpQueueSize, err := hysteria2UDPQueueSize(spec.Config.Raw)
	if err != nil {
		cancel()
		return err
	}
	service, err := hy2.NewService[int](hy2.ServiceOptions{
		Context: ctx, Logger: newSingConnErrorLogger(connErr, nil), SendBPS: uint64(up) * hysteria.MbpsToBps,
		ReceiveBPS: uint64(down) * hysteria.MbpsToBps, SalamanderPassword: salamander,
		TLSConfig: tlsConfig, UDPTimeout: udpTimeout, UDPQueueSize: udpQueueSize, Handler: a,
		MasqueradeHandler: hysteria2RejectHandler(connErr),
	})
	if err != nil {
		cancel()
		return fmt.Errorf("hysteria2 service: %w", err)
	}
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	packet, err := net.ListenPacket("udp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
	if err != nil {
		cancel()
		return err
	}
	a.mu.Lock()
	if a.closed || a.service != nil {
		a.mu.Unlock()
		cancel()
		_ = packet.Close()
		return fmt.Errorf("hysteria2 adapter already started or closed")
	}
	a.spec, a.plane, a.ctx, a.cancel, a.service, a.packet = spec, hooks.DataPlane, ctx, cancel, service, packet
	a.connErr = connErr
	a.salamander, a.upBPS, a.downBPS, a.udpTimeout = salamander, uint64(up)*hysteria.MbpsToBps, uint64(down)*hysteria.MbpsToBps, udpTimeout
	a.mu.Unlock()
	if err := a.syncUsers(); err != nil {
		_ = a.Close()
		return err
	}
	if err := service.Start(packet); err != nil {
		_ = a.Close()
		return fmt.Errorf("hysteria2 listen: %w", err)
	}
	warnSmallQUICSocketBuffers("hysteria2", spec.Config.Port, packet)
	return nil
}

// hysteria2RejectHandler 替换 nativewire 缺省的 404 伪装处理器：响应照旧是
// 404（对探测者不暴露任何差别），只是先把这次拒绝上报出去。
//
// Hysteria2 的口令校验失败不报错、不打日志，直接交给伪装处理器——这是协议
// 抗探测的设计，代价是节点端对「密码不对」完全无感。伪装处理器因此是唯一
// 能看到它的地方。POST https://hysteria/auth 是认证请求（常量在 nativewire
// 的 internal 包里，引用不到，按协议线格式写死），其余都是探测。
func hysteria2RejectHandler(connErr connErrorReporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Host == "hysteria" && r.URL != nil && r.URL.Path == "/auth" {
			connErr.request(StageSession, r.RemoteAddr, markConnError(connErrAuth, fmt.Errorf("hysteria2 auth rejected")))
		} else {
			connErr.request(StageSession, r.RemoteAddr, fmt.Errorf("hysteria2 non-auth HTTP/3 request"))
		}
		http.NotFound(w, r)
	})
}

// syncUsers 把整张槽位表同步进 nativewire 口令表，只在 Start 时用；之后的增减
// 走 PatchUsers 增量更新。停用的槽位不进口令表。
func (a *hysteria2Adapter) syncUsers() error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.RLock()
	service := a.service
	indices := make([]int, 0, len(a.users))
	passwords := make([]string, 0, len(a.users))
	for i, slot := range a.slots {
		if slot.active {
			indices = append(indices, i)
			passwords = append(passwords, slot.user.UUID)
		}
	}
	a.mu.RUnlock()
	if service != nil {
		service.UpdateUsers(indices, passwords)
	}
	return nil
}

func normalizeHysteria2Users(users []core.User) ([]core.User, error) {
	// Validate the entire batch before taking the state lock. A failed batch
	// must not partially publish users or leave the adapter locked.
	validated := make([]core.User, 0, len(users))
	for _, user := range users {
		password := strings.TrimSpace(user.UUID)
		if password == "" {
			return nil, fmt.Errorf("hysteria2 user password is empty")
		}
		user.UUID = password
		validated = append(validated, user)
	}
	return validated, nil
}

// putUsers 是 AddUsers / UpsertUsers 的共同实现。updateMu 在整个过程中持有
// （锁序 updateMu → mu，与 syncUsers 一致），保证口令表的增量与槽位表的变更
// 同序生效，并发的增删不会互相覆盖。
func (a *hysteria2Adapter) putUsers(users []core.User, upsert bool) error {
	validated, err := normalizeHysteria2Users(users)
	if err != nil {
		return err
	}
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("hysteria2 adapter is closed")
	}
	var indices []int
	var passwords []string
	for _, user := range validated {
		password := user.UUID
		if index, exists := a.users[password]; exists {
			if upsert {
				// 口令不变、槽位不变，口令表无需改动。
				a.slots[index].user = user
				a.slots[index].active = true
			}
			continue
		}
		a.users[password] = len(a.slots)
		indices = append(indices, len(a.slots))
		passwords = append(passwords, password)
		a.slots = append(a.slots, hysteria2Slot{user: user, active: true})
	}
	service := a.service
	a.mu.Unlock()
	if service != nil && len(indices) > 0 {
		service.PatchUsers(nil, indices, passwords)
	}
	return nil
}

func (a *hysteria2Adapter) AddUsers(users []core.User) error {
	return a.putUsers(users, false)
}

func (a *hysteria2Adapter) UpsertUsers(users []core.User) error {
	return a.putUsers(users, true)
}

func (a *hysteria2Adapter) DelUsers(ids []string) error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.Lock()
	removed := make([]string, 0, len(ids))
	var removedIDs []int64
	for _, id := range ids {
		password := strings.TrimSpace(id)
		if index, ok := a.users[password]; ok {
			a.slots[index].active = false
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			a.limiters.Remove(a.slots[index].user.ID)
			removedIDs = append(removedIDs, a.slots[index].user.ID)
			delete(a.users, password)
			removed = append(removed, password)
		}
	}
	service := a.service
	a.mu.Unlock()
	if service != nil && len(removed) > 0 {
		service.PatchUsers(removed, nil, nil)
	}
	// 先删表、再踢线（锁外关）：已有 QUIC 会话里属于他的 TCP 子流随之断开。
	a.sessions.revoke(removedIDs)
	return nil
}

func (a *hysteria2Adapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
}

func (a *hysteria2Adapter) OnlineIPs() map[int64][]string { return a.online.snapshot() }

func (a *hysteria2Adapter) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	a.wg.Add(1)
	a.active[conn] = struct{}{}
	a.mu.Unlock()
	goGuarded(conn, func() {
		defer a.wg.Done()
		defer a.removeActive(conn)
		defer conn.Close()
		if onClose != nil {
			defer onClose(nil)
		}
		epoch := a.sessions.epoch()
		index, user, ok := a.userFromContext(ctx)
		if admitErr := admissionError("hysteria2", ok, ok && a.online.enter(user, source.AddrString())); admitErr != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), admitErr)
			if hs, ok := conn.(N.HandshakeFailure); ok {
				_ = hs.HandshakeFailure(fmt.Errorf("hysteria2 user is not authorized"))
			}
			return
		}
		defer a.online.leave(user, source.AddrString())
		sess := a.sessions.open(user, epoch, conn)
		if sess == nil {
			a.connErr.addr(StageSession, source.UDPAddr(), errSessionRevoked)
			return
		}
		defer sess.close()
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "tcp", Protocol: "hysteria2", SourceIP: source.Addr, SourcePort: source.Port}
		upstream, err := a.plane.DialTCP(ctx, meta, destination)
		if err != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), err)
			if hs, ok := conn.(N.HandshakeFailure); ok {
				_ = hs.HandshakeFailure(err)
			}
			return
		}
		defer upstream.Close()
		if hs, ok := conn.(N.HandshakeSuccess); ok {
			if err := hs.HandshakeSuccess(); err != nil {
				return
			}
		}
		// 子流的 Close 语义与 TCP 半关闭不同：沿用「一侧结束即两端全关」。
		sess.relay(conn, upstream, core.RelayOptions{Limiter: a.limiters.For(user), NoHalfClose: true})
		_ = index
	})
}

func (a *hysteria2Adapter) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer conn.Close()
		if onClose != nil {
			defer onClose(nil)
		}
		// epoch 要在查用户之前取，见 userSessions 的竞态说明。
		epoch := a.sessions.epoch()
		_, user, ok := a.userFromContext(ctx)
		if admitErr := admissionError("hysteria2", ok, ok && a.online.enter(user, source.AddrString())); admitErr != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), admitErr)
			return
		}
		defer a.online.leave(user, source.AddrString())
		if !a.udpQuota.acquire(user.ID) {
			a.connErr.addr(StageSession, source.UDPAddr(), udpSessionLimitError("hysteria2"))
			return
		}
		defer a.udpQuota.release(user.ID)
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "udp", Protocol: "hysteria2", SourceIP: source.Addr, SourcePort: source.Port}
		upstream, err := a.plane.ListenUDP(ctx, meta, destination)
		if err != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), err)
			return
		}
		defer upstream.Close()
		// UDP 会话同样登记进用户连接表：删用户时关掉会话与上游 socket，两个方向
		// 的阻塞读立即返回，不必等空闲超时。
		sess := a.sessions.open(user, epoch, conn, upstream)
		if sess == nil {
			a.connErr.addr(StageSession, source.UDPAddr(), errSessionRevoked)
			return
		}
		defer sess.close()
		relayHy2UDP(ctx, conn, upstream, destination, user.ID, sess.up(), sess.down())
	}()
}

func (a *hysteria2Adapter) userFromContext(ctx context.Context) (int, core.User, bool) {
	index, ok := auth.UserFromContext[int](ctx)
	if !ok {
		return 0, core.User{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if index < 0 || index >= len(a.slots) || !a.slots[index].active {
		return 0, core.User{}, false
	}
	return index, a.slots[index].user, true
}

func (a *hysteria2Adapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}

func (a *hysteria2Adapter) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		cancel, service, packet := a.cancel, a.service, a.packet
		active := make([]net.Conn, 0, len(a.active))
		for conn := range a.active {
			active = append(active, conn)
		}
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if service != nil {
			_ = service.Close()
		}
		if packet != nil {
			_ = packet.Close()
		}
		for _, conn := range active {
			_ = conn.Close()
		}
	})
	a.wg.Wait()
	return nil
}

// nonNegativeInt 读一个非负整数字段；缺省（nil）按 0。数值形态的归一见 confnum。
func nonNegativeInt(value any) (int, bool) {
	if value == nil {
		return 0, true
	}
	n, ok := confnum.Int(value)
	return n, ok && n >= 0
}

// parseHysteriaDuration 读一个时长字段：数字按整秒，字符串按 Go 时长语法。
func parseHysteriaDuration(value any) (time.Duration, error) {
	d, ok := confnum.Duration(value)
	if !ok {
		return 0, fmt.Errorf("must be a duration or seconds")
	}
	return d, nil
}

func resolveUDPAddr(ctx context.Context, destination M.Socksaddr) (*net.UDPAddr, error) {
	if destination.IsIP() {
		return destination.UDPAddr(), nil
	}
	if strings.TrimSpace(destination.Fqdn) == "" {
		return nil, fmt.Errorf("invalid UDP destination")
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", destination.Fqdn)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = fmt.Errorf("no address")
		}
		return nil, err
	}
	return &net.UDPAddr{IP: net.IP(addrs[0].AsSlice()), Port: int(destination.Port), Zone: addrs[0].Zone()}, nil
}
