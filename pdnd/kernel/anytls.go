package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	anytls "github.com/aegispanel/nodeagent/internal/nativewire/anytls"
	"github.com/aegispanel/nodeagent/internal/nativewire/anytls/padding"
	"github.com/aegispanel/nodeagent/route"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

type anyTLSSlot struct {
	user   core.User
	active bool
}

type anyTLSAdapter struct {
	spec InboundSpec

	mu        sync.RWMutex
	users     map[string]int
	slots     []anyTLSSlot
	traffic   map[int64]core.UserTraffic
	online    map[int64]map[string]struct{}
	service   *anytls.Service
	listener  net.Listener
	tlsConfig *hysteria2TLSConfig
	plane     DataPlane
	connErr   connErrorReporter
	limiters  core.SpeedLimiters
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	// fallback 是认证失败时的回落目标（raw `fallback`）；空串时由中性页面接住。
	fallback  *probeFallback
	active    map[net.Conn]struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	updateMu  sync.Mutex
}

var _ Adapter = (*anyTLSAdapter)(nil)
var _ N.TCPConnectionHandlerEx = (*anyTLSAdapter)(nil)

func newAnyTLSAdapter(spec InboundSpec) (Adapter, error) {
	return &anyTLSAdapter{
		spec: spec, users: make(map[string]int), traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}),
	}, nil
}

func (a *anyTLSAdapter) Protocol() string { return "anytls" }

func (a *anyTLSAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "anytls") {
		return fmt.Errorf("anytls adapter received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("anytls port is invalid")
	}
	if network := strings.TrimSpace(rawString(spec.Config.Raw, "network")); network != "" && !strings.EqualFold(network, "tcp") {
		return fmt.Errorf("anytls requires TCP transport, got %q", network)
	}
	certPath, keyPath := strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")), strings.TrimSpace(rawString(spec.Config.Raw, "key_path"))
	if (certPath == "") != (keyPath == "") {
		return fmt.Errorf("anytls cert_path and key_path must be provided together")
	}
	if value, exists := spec.Config.Raw["tls"]; exists {
		enabled, ok := value.(bool)
		if !ok {
			return fmt.Errorf("anytls tls must be boolean")
		}
		if enabled && (certPath == "" || keyPath == "") {
			return fmt.Errorf("anytls enabled TLS requires cert_path and key_path")
		}
	}
	if raw, exists := spec.Config.Raw["padding_scheme"]; exists {
		if _, err := parseAnyTLSPadding(raw); err != nil {
			return err
		}
	}
	if _, err := parseProbeFallback(spec.Config.Raw); err != nil {
		return fmt.Errorf("anytls %w", err)
	}
	return nil
}

func (a *anyTLSAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("anytls requires context and data plane")
	}
	if err := a.Validate(spec); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	var tlsConfig *hysteria2TLSConfig
	certPath, keyPath := strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")), strings.TrimSpace(rawString(spec.Config.Raw, "key_path"))
	if certPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			cancel()
			return fmt.Errorf("anytls TLS certificate: %w", err)
		}
		tlsConfig = &hysteria2TLSConfig{std: withInboundWebALPN(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})}
	}
	paddingScheme := padding.DefaultPaddingScheme
	if raw, exists := spec.Config.Raw["padding_scheme"]; exists {
		var err error
		paddingScheme, err = parseAnyTLSPadding(raw)
		if err != nil {
			cancel()
			return err
		}
	}
	fallback, err := parseProbeFallback(spec.Config.Raw)
	if err != nil {
		cancel()
		return fmt.Errorf("anytls %w", err)
	}
	// 口令不对、首包不足 32 字节等认证失败交给 anyTLSFallback：回落或中性页面，
	// 不再 0 秒断开。
	service, err := anytls.NewService(anytls.ServiceConfig{PaddingScheme: paddingScheme, Handler: a, FallbackHandler: anyTLSFallback{adapter: a}, Logger: logger.NOP()})
	if err != nil {
		cancel()
		return fmt.Errorf("anytls service: %w", err)
	}
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(listen, fmt.Sprint(spec.Config.Port)))
	if err != nil {
		cancel()
		return err
	}
	a.mu.Lock()
	if a.closed || a.listener != nil {
		a.mu.Unlock()
		cancel()
		_ = listener.Close()
		return fmt.Errorf("anytls adapter already started or closed")
	}
	a.spec, a.plane, a.ctx, a.cancel, a.service, a.listener, a.tlsConfig = spec, hooks.DataPlane, ctx, cancel, service, listener, tlsConfig
	a.fallback = newProbeFallback(fallback)
	a.connErr = newConnErrorReporter(hooks, spec, "anytls")
	a.mu.Unlock()
	if err := a.syncUsers(); err != nil {
		_ = a.Close()
		return err
	}
	a.mu.Lock()
	a.wg.Add(1)
	a.mu.Unlock()
	go a.acceptLoop()
	return nil
}

func parseAnyTLSPadding(value any) ([]byte, error) {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("anytls padding_scheme is empty")
		}
		return []byte(v), nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("anytls padding_scheme is empty")
		}
		return []byte(strings.Join(v, "\n")), nil
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			part, ok := item.(string)
			if !ok || strings.TrimSpace(part) == "" {
				return nil, fmt.Errorf("anytls padding_scheme[%d] must be a non-empty string", i)
			}
			parts[i] = part
		}
		return []byte(strings.Join(parts, "\n")), nil
	default:
		return nil, fmt.Errorf("anytls padding_scheme must be string or string array")
	}
}

func (a *anyTLSAdapter) syncUsers() error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.RLock()
	service := a.service
	users := make([]anytls.User, 0, len(a.slots))
	for _, slot := range a.slots {
		if slot.active {
			users = append(users, anytls.User{Name: slot.user.UUID, Password: slot.user.UUID})
		}
	}
	a.mu.RUnlock()
	if service != nil {
		service.UpdateUsers(users)
	}
	return nil
}

func (a *anyTLSAdapter) AddUsers(users []core.User) error {
	validated := make([]anyTLSSlot, 0, len(users))
	for _, user := range users {
		password := strings.TrimSpace(user.UUID)
		if password == "" {
			return fmt.Errorf("anytls user password is empty")
		}
		user.UUID = password
		validated = append(validated, anyTLSSlot{user: user, active: true})
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("anytls adapter is closed")
	}
	for _, slot := range validated {
		password := slot.user.UUID
		if _, exists := a.users[password]; exists {
			continue
		}
		a.users[password] = len(a.slots)
		a.slots = append(a.slots, slot)
	}
	a.mu.Unlock()
	return a.syncUsers()
}

func (a *anyTLSAdapter) UpsertUsers(users []core.User) error {
	validated := make([]anyTLSSlot, 0, len(users))
	for _, user := range users {
		password := strings.TrimSpace(user.UUID)
		if password == "" {
			return fmt.Errorf("anytls user password is empty")
		}
		user.UUID = password
		validated = append(validated, anyTLSSlot{user: user, active: true})
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("anytls adapter is closed")
	}
	changed := false
	for _, slot := range validated {
		password := slot.user.UUID
		if index, exists := a.users[password]; exists {
			a.slots[index].user = slot.user
			a.slots[index].active = true
			changed = true
			continue
		}
		a.users[password] = len(a.slots)
		a.slots = append(a.slots, slot)
		changed = true
	}
	a.mu.Unlock()
	if !changed {
		return nil
	}
	return a.syncUsers()
}

func (a *anyTLSAdapter) DelUsers(ids []string) error {
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

func (a *anyTLSAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, traffic := range a.traffic {
		if traffic.Upload != 0 || traffic.Download != 0 {
			out = append(out, traffic)
		}
		delete(a.traffic, id)
	}
	return out, nil
}

func (a *anyTLSAdapter) OnlineIPs() map[int64][]string {
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

func (a *anyTLSAdapter) acceptLoop() {
	defer a.wg.Done()
	a.mu.RLock()
	ctx, listener := a.ctx, a.listener
	a.mu.RUnlock()
	runAcceptLoop(ctx.Done(), listener.Accept, func(conn net.Conn) {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = conn.Close()
			return
		}
		a.wg.Add(1)
		a.active[conn] = struct{}{}
		a.mu.Unlock()
		go a.handleAccepted(conn)
	})
}

func (a *anyTLSAdapter) handleAccepted(conn net.Conn) {
	defer a.wg.Done()
	defer a.removeActive(conn)
	defer conn.Close()
	a.mu.RLock()
	ctx, service, tlsConfig := a.ctx, a.service, a.tlsConfig
	a.mu.RUnlock()
	if tlsConfig != nil {
		wrapped, err := tlsConfig.Server(conn)
		if err == nil {
			// 握手限时，与 vless / vmess / trojan 的 serverTLSHandshake 同一口径。
			// 握手之后的口令认证仍由 sing-anytls 自己读、不限时：它把会话循环
			// 跑在同一次 NewConnection 里，这里没有清截止时间的落点，而客户端
			// 预建的空闲会话本来就会长时间不发数据。
			err = withHandshakeDeadline(conn, inboundHandshakeTimeout, func() error { return wrapped.HandshakeContext(ctx) })
		}
		if err != nil {
			a.connErr.conn(StageTLSHandshake, conn, err)
			return
		}
		conn = wrapped
		ctx = context.WithValue(ctx, anyTLSNegotiatedH2{}, wrapped.ConnectionState().NegotiatedProtocol == "h2")
	}
	source := M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()
	// NewConnection 的错误是 AnyTLS 会话层的：口令不对（"unknown user
	// password"）、padding 帧读不全。通过认证的子流失败在 NewConnectionEx 里报。
	if err := service.NewConnection(ctx, conn, source, nil); err != nil {
		a.connErr.conn(StageSession, conn, err)
	}
}

func (a *anyTLSAdapter) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
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
		remote := source.TCPAddr()
		name, ok := auth.UserFromContext[string](ctx)
		if !ok {
			a.connErr.addr(StageSession, remote, markConnError(connErrAuth, fmt.Errorf("anytls stream has no authenticated user")))
			return
		}
		index, user, ok := a.lookupUser(name)
		if !ok {
			a.connErr.addr(StageSession, remote, markConnError(connErrAuth, fmt.Errorf("anytls user is no longer active")))
			return
		}
		if !a.enterDevice(user, source.AddrString()) {
			a.connErr.addr(StageSession, remote, deviceLimitError("anytls"))
			return
		}
		defer a.leaveDevice(user, source.AddrString())
		if destination.Fqdn == uot.MagicAddress || destination.Fqdn == uot.LegacyMagicAddress {
			if err := a.handleUOT(ctx, conn, source, destination.Fqdn == uot.MagicAddress, index); err != nil {
				a.connErr.addr(StageSession, remote, err)
			}
			return
		}
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "tcp", Protocol: "anytls", SourceIP: source.Addr, SourcePort: source.Port}
		upstream, err := a.plane.DialTCP(ctx, meta, destination)
		if err != nil {
			a.connErr.addr(StageSession, remote, err)
			return
		}
		defer upstream.Close()
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

func (a *anyTLSAdapter) handleUOT(ctx context.Context, conn net.Conn, source M.Socksaddr, version2 bool, index int) error {
	meta := route.Meta{Network: "udp", Protocol: "anytls", SourceIP: source.Addr, SourcePort: source.Port}
	packetConn := newUOTRoutedPacketConn(ctx, a.plane, meta)
	defer packetConn.Close()
	version := 1
	var request *uot.Request
	if version2 {
		version = uot.Version
		var err error
		request, err = uot.ReadRequest(conn)
		if err != nil || request == nil || !request.Destination.IsValid() || request.Destination.Port == 0 {
			return fmt.Errorf("invalid AnyTLS UoT request")
		}
	}
	uotConn := uot.NewServerConn(packetConn, version)
	defer uotConn.Close()
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		if request != nil {
			encoded, err := uot.EncodeRequest(*request)
			if err != nil {
				return
			}
			_, _ = uotConn.Write(encoded.Bytes())
			encoded.Release()
		}
		_, _ = io.Copy(uotConn, conn)
	}()
	_, _ = io.Copy(conn, uotConn)
	_ = conn.Close()
	_ = uotConn.Close()
	<-feedDone
	_ = index
	return nil
}

func (a *anyTLSAdapter) lookupUser(name string) (int, core.User, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	index, ok := a.users[name]
	if !ok || index < 0 || index >= len(a.slots) || !a.slots[index].active {
		return 0, core.User{}, false
	}
	return index, a.slots[index].user, true
}

func (a *anyTLSAdapter) enterDevice(user core.User, ip string) bool {
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

func (a *anyTLSAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *anyTLSAdapter) addTraffic(index int, upload, download int64) {
	a.mu.Lock()
	if index >= 0 && index < len(a.slots) {
		id := a.slots[index].user.ID
		current := a.traffic[id]
		current.ID = id
		current.Upload += upload
		current.Download += download
		a.traffic[id] = current
	}
	a.mu.Unlock()
}

func (a *anyTLSAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}

func (a *anyTLSAdapter) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		cancel, listener := a.cancel, a.listener
		active := make([]net.Conn, 0, len(a.active))
		for conn := range a.active {
			active = append(active, conn)
		}
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if listener != nil {
			_ = listener.Close()
		}
		for _, conn := range active {
			_ = conn.Close()
		}
	})
	a.wg.Wait()
	return nil
}
