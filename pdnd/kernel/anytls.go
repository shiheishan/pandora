package kernel

import (
	"context"
	"crypto/tls"
	"errors"
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
	sessions  userSessions
	online    onlineDevices
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

// errAnyTLSStreamRefused 是子流打不开时经 cmdSYNACK 发给客户端的文字。拨号失败、
// 用户已删、会话已撤销、设备超限一律同一句：不带目标地址、内部原因与实现名，
// 免得成为指纹或泄露信息；具体原因只进本机的 OnConnError 观测链。
var errAnyTLSStreamRefused = errors.New("connection failed")

func newAnyTLSAdapter(spec InboundSpec) (Adapter, error) {
	return &anyTLSAdapter{
		spec: spec, users: make(map[string]int),
		active: make(map[net.Conn]struct{}),
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
	var removed []int64
	for _, id := range ids {
		password := strings.TrimSpace(id)
		if index, ok := a.users[password]; ok {
			a.slots[index].active = false
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			a.limiters.Remove(a.slots[index].user.ID)
			removed = append(removed, a.slots[index].user.ID)
			delete(a.users, password)
		}
	}
	a.mu.Unlock()
	err := a.syncUsers()
	// 先删表、再踢线（锁外关）：已有的 QUIC / AnyTLS 会话里属于他的流随之断开。
	a.sessions.revoke(removed)
	return err
}

func (a *anyTLSAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
}

func (a *anyTLSAdapter) OnlineIPs() map[int64][]string { return a.online.snapshot() }

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
		goGuardedConn(conn, func() { a.handleAccepted(conn) })
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
	ctx = context.WithValue(ctx, anyTLSOuterConnKey{}, conn)
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
		_ = N.ReportHandshakeFailure(conn, errAnyTLSStreamRefused)
		_ = conn.Close()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	goGuarded(conn, func() {
		defer a.wg.Done()
		defer conn.Close()
		if onClose != nil {
			defer onClose(nil)
		}
		// v2 客户端在复用会话上开流（sid>=2）后等 cmdSYNACK，3 秒收不到就关整条会话。
		// 拨号成功（或进入 UoT）时回成功；其余每个出口都由这里回中性的失败，
		// 客户端立刻关这条流、会话照常复用。fork 的 Stream 只报一次，已回过成功时
		// 这里是空操作；它排在 conn.Close 之前执行，SYNACK 先于 FIN 到达客户端。
		defer func() { _ = N.ReportHandshakeFailure(conn, errAnyTLSStreamRefused) }()
		remote := source.TCPAddr()
		epoch := a.sessions.epoch()
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
		// 登记子流与它所在的外层会话：用户被移出名单时整条 AnyTLS 会话断开。
		sess := a.sessions.open(user, epoch, conn, anyTLSOuterConn(ctx))
		if sess == nil {
			a.connErr.addr(StageSession, remote, errSessionRevoked)
			return
		}
		defer sess.close()
		if !a.online.enter(user, source.AddrString()) {
			a.connErr.addr(StageSession, remote, deviceLimitError("anytls"))
			return
		}
		defer a.online.leave(user, source.AddrString())
		if destination.Fqdn == uot.MagicAddress || destination.Fqdn == uot.LegacyMagicAddress {
			// UoT 不拨上游（出站 socket 按包懒建），进入前就回成功。
			if err := N.ReportConnHandshakeSuccess(conn, nil); err != nil {
				a.connErr.addr(StageSession, remote, err)
				return
			}
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
		if err := N.ReportConnHandshakeSuccess(conn, upstream); err != nil {
			a.connErr.addr(StageSession, remote, err)
			return
		}
		// 子流的 Close 语义与 TCP 半关闭不同：沿用「一侧结束即两端全关」。
		sess.relay(conn, upstream, core.RelayOptions{Limiter: a.limiters.For(user), NoHalfClose: true})
		_ = index
	})
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

func (a *anyTLSAdapter) addTraffic(index int, upload, download int64) {
	a.mu.RLock()
	var id int64
	ok := index >= 0 && index < len(a.slots)
	if ok {
		id = a.slots[index].user.ID
	}
	a.mu.RUnlock()
	if ok {
		a.sessions.add(id, upload, download)
	}
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

// anyTLSOuterConnKey 把外层会话连接挂进 ctx，子流登记时一并登记它：踢人要断整条
// 会话，否则客户端还能在同一会话上开新流（新流会被 lookupUser 拒，但已开的流
// 只关子流不够干净）。
type anyTLSOuterConnKey struct{}

func anyTLSOuterConn(ctx context.Context) io.Closer {
	if conn, ok := ctx.Value(anyTLSOuterConnKey{}).(net.Conn); ok {
		return conn
	}
	return nil
}
