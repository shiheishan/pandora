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

	mu      sync.RWMutex
	users   map[string]int
	slots   []hysteria2Slot
	traffic map[int64]core.UserTraffic
	// udpTraffic 是活跃 UDP 会话的流量计数，SnapshotTraffic 时并入 traffic。
	udpTraffic map[*hy2Traffic]struct{}
	online     map[int64]map[string]struct{}
	service    *hy2.Service[int]
	packet     net.PacketConn
	plane      DataPlane
	connErr    connErrorReporter
	limiters   core.SpeedLimiters
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	active     map[net.Conn]struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
	updateMu   sync.Mutex

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
		spec: spec, users: make(map[string]int), traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}),
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

func (a *hysteria2Adapter) syncUsers() error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.RLock()
	service := a.service
	indices := make([]int, len(a.slots))
	passwords := make([]string, len(a.slots))
	for i, slot := range a.slots {
		indices[i] = i
		if slot.active {
			passwords[i] = slot.user.UUID
		} else {
			passwords[i] = fmt.Sprintf("__pandora_disabled_%d__", i)
		}
	}
	a.mu.RUnlock()
	if service != nil {
		service.UpdateUsers(indices, passwords)
	}
	return nil
}

func (a *hysteria2Adapter) AddUsers(users []core.User) error {
	// Validate the entire batch before taking the state lock. A failed batch
	// must not partially publish users or leave the adapter locked.
	validated := make([]core.User, 0, len(users))
	for _, user := range users {
		password := strings.TrimSpace(user.UUID)
		if password == "" {
			return fmt.Errorf("hysteria2 user password is empty")
		}
		user.UUID = password
		validated = append(validated, user)
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("hysteria2 adapter is closed")
	}
	for _, user := range validated {
		password := user.UUID
		if _, exists := a.users[password]; exists {
			continue
		}
		a.users[password] = len(a.slots)
		a.slots = append(a.slots, hysteria2Slot{user: user, active: true})
	}
	a.mu.Unlock()
	return a.syncUsers()
}

func (a *hysteria2Adapter) UpsertUsers(users []core.User) error {
	validated := make([]core.User, 0, len(users))
	for _, user := range users {
		password := strings.TrimSpace(user.UUID)
		if password == "" {
			return fmt.Errorf("hysteria2 user password is empty")
		}
		user.UUID = password
		validated = append(validated, user)
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("hysteria2 adapter is closed")
	}
	changed := false
	for _, user := range validated {
		password := user.UUID
		if index, exists := a.users[password]; exists {
			a.slots[index].user = user
			a.slots[index].active = true
			changed = true
			continue
		}
		a.users[password] = len(a.slots)
		a.slots = append(a.slots, hysteria2Slot{user: user, active: true})
		changed = true
	}
	a.mu.Unlock()
	if !changed {
		return nil
	}
	return a.syncUsers()
}

func (a *hysteria2Adapter) DelUsers(ids []string) error {
	a.mu.Lock()
	for _, id := range ids {
		password := strings.TrimSpace(id)
		if index, ok := a.users[password]; ok {
			a.slots[index].active = false
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			a.limiters.Remove(a.slots[index].user.ID)
			delete(a.users, password)
		}
	}
	a.mu.Unlock()
	return a.syncUsers()
}

func (a *hysteria2Adapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for traffic := range a.udpTraffic {
		a.collectUDPTrafficLocked(traffic)
	}
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, traffic := range a.traffic {
		if traffic.Upload != 0 || traffic.Download != 0 {
			out = append(out, traffic)
		}
		delete(a.traffic, id)
	}
	return out, nil
}

func (a *hysteria2Adapter) OnlineIPs() map[int64][]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[int64][]string, len(a.online))
	for id, ips := range a.online {
		for ip := range ips {
			out[id] = append(out[id], ip)
		}
	}
	return out
}

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
	go func() {
		defer a.wg.Done()
		defer a.removeActive(conn)
		defer conn.Close()
		if onClose != nil {
			defer onClose(nil)
		}
		index, user, ok := a.userFromContext(ctx)
		if admitErr := admissionError("hysteria2", ok, ok && a.enterDevice(user, source.AddrString())); admitErr != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), admitErr)
			if hs, ok := conn.(N.HandshakeFailure); ok {
				_ = hs.HandshakeFailure(fmt.Errorf("hysteria2 user is not authorized"))
			}
			return
		}
		defer a.leaveDevice(user, source.AddrString())
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
		var copyWG sync.WaitGroup
		copyWG.Add(2)
		copyDone := make(chan struct{}, 2)
		go func() {
			n, _ := core.SpeedLimitedCopy(upstream, conn, a.limiters.For(user))
			a.addTraffic(index, n, 0)
			copyDone <- struct{}{}
			copyWG.Done()
		}()
		go func() {
			n, _ := core.SpeedLimitedCopy(conn, upstream, a.limiters.For(user))
			a.addTraffic(index, 0, n)
			copyDone <- struct{}{}
			copyWG.Done()
		}()
		<-copyDone
		_ = conn.Close()
		_ = upstream.Close()
		copyWG.Wait()
	}()
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
		index, user, ok := a.userFromContext(ctx)
		if admitErr := admissionError("hysteria2", ok, ok && a.enterDevice(user, source.AddrString())); admitErr != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), admitErr)
			return
		}
		defer a.leaveDevice(user, source.AddrString())
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "udp", Protocol: "hysteria2", SourceIP: source.Addr, SourcePort: source.Port}
		upstream, err := a.plane.ListenUDP(ctx, meta, destination)
		if err != nil {
			a.connErr.addr(StageSession, source.UDPAddr(), err)
			return
		}
		defer upstream.Close()
		a.relayHy2UDP(ctx, conn, upstream, destination, index)
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

func (a *hysteria2Adapter) enterDevice(user core.User, ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.online[user.ID]
	if set == nil {
		set = make(map[string]struct{})
		a.online[user.ID] = set
	}
	if _, exists := set[ip]; !exists && user.DeviceLimit > 0 && len(set) >= user.DeviceLimit {
		return false
	}
	set[ip] = struct{}{}
	return true
}

func (a *hysteria2Adapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *hysteria2Adapter) addTraffic(index int, upload, download int64) {
	a.mu.Lock()
	a.addTrafficLocked(index, upload, download)
	a.mu.Unlock()
}

func (a *hysteria2Adapter) addTrafficLocked(index int, upload, download int64) {
	if index >= 0 && index < len(a.slots) {
		id := a.slots[index].user.ID
		current := a.traffic[id]
		current.ID = id
		current.Upload += upload
		current.Download += download
		a.traffic[id] = current
	}
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

func nonNegativeInt(value any) (int, bool) {
	switch v := value.(type) {
	case nil:
		return 0, true
	case int:
		return v, v >= 0
	case int64:
		return int(v), v >= 0 && int64(int(v)) == v
	case float64:
		return int(v), v >= 0 && v == float64(int(v))
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil && n >= 0
	default:
		return 0, false
	}
}

func parseHysteriaDuration(value any) (time.Duration, error) {
	switch v := value.(type) {
	case int:
		if v < 0 {
			return 0, fmt.Errorf("must be non-negative")
		}
		return time.Duration(v) * time.Second, nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("must be non-negative")
		}
		return time.Duration(v) * time.Second, nil
	case float64:
		if v < 0 || v != float64(int64(v)) {
			return 0, fmt.Errorf("must be a non-negative integer")
		}
		return time.Duration(int64(v)) * time.Second, nil
	case string:
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d >= 0 {
			return d, nil
		}
		return 0, fmt.Errorf("must be a duration or seconds")
	default:
		return 0, fmt.Errorf("must be a duration or seconds")
	}
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
