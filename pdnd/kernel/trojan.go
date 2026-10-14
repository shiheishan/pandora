package kernel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	"github.com/gorilla/websocket"
	M "github.com/sagernet/sing/common/metadata"
)

// trojanAdapter is Pandora's native TCP Trojan server. A panel User.UUID is
// the Trojan password; only the SHA-224 password proof is kept in the lookup
// table so plaintext credentials do not remain in the adapter state.
type trojanAdapter struct {
	spec       InboundSpec
	mu         sync.RWMutex
	users      map[string]trojanUser
	sessions   userSessions
	online     onlineDevices
	listener   net.Listener
	httpServer *http.Server
	plane      DataPlane
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	reality    bool
	connErr    connErrorReporter
	limiters   core.SpeedLimiters
	tlsConfig  *tls.Config
	// fallback 是认证失败时的回落目标（raw `fallback`，"host:port"）；空串时
	// 由中性页面接住。只作用于 TCP / TLS / REALITY / mKCP 直连承载。
	fallback *probeFallback
	active   map[net.Conn]struct{}
	wg       sync.WaitGroup
}

type trojanUser struct {
	ID          int64
	DeviceLimit int
	// SpeedLimit 之前没存，面板下发的限速到这里就丢了，套餐里写的
	// 速率对用户毫无约束。
	SpeedLimit int
}

const trojanCommandUDP byte = 3

func newTrojanAdapter(spec InboundSpec) (Adapter, error) {
	return &trojanAdapter{
		spec: spec, users: make(map[string]trojanUser),
		active: make(map[net.Conn]struct{}),
	}, nil
}

func (a *trojanAdapter) Protocol() string { return "trojan" }

func (a *trojanAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "trojan") {
		return fmt.Errorf("trojan adapter received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("trojan port is invalid")
	}
	network, _ := spec.Config.Raw["network"].(string)
	if network != "" && !strings.EqualFold(network, "tcp") && !strings.EqualFold(network, "ws") && !strings.EqualFold(network, "httpupgrade") && !strings.EqualFold(network, "grpc") && !isMKCPNetwork(network) {
		return fmt.Errorf("native trojan currently accepts tcp/ws/httpupgrade/grpc only")
	}
	if strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") {
		if _, _, err := parseWebSocketTransport(spec.Config.Raw); err != nil {
			return err
		}
		if strings.EqualFold(rawString(spec.Config.Raw, "security"), "reality") {
			return fmt.Errorf("native trojan websocket cannot stack REALITY")
		}
	}
	if strings.EqualFold(network, "grpc") {
		if _, _, err := parseGRPCPath(spec.Config.Raw); err != nil {
			return err
		}
	}
	security, _ := spec.Config.Raw["security"].(string)
	if strings.EqualFold(security, "reality") {
		if _, err := ParseRealityServerConfig(spec.Config.Raw); err != nil {
			return err
		}
	} else if security != "" && !strings.EqualFold(security, "none") {
		return fmt.Errorf("native trojan rejects unsupported security %q", security)
	}
	if enabled, ok := spec.Config.Raw["tls"].(bool); ok && enabled && !strings.EqualFold(security, "reality") {
		if _, _, err := loadInboundTLSConfig(spec.Config.Raw); err != nil {
			return err
		}
	}
	if _, err := parseProbeFallback(spec.Config.Raw); err != nil {
		return fmt.Errorf("trojan %w", err)
	}
	return nil
}

func (a *trojanAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("trojan start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("trojan adapter already started or closed")
	}
	security, _ := spec.Config.Raw["security"].(string)
	network, _ := spec.Config.Raw["network"].(string)
	a.spec, a.plane, a.reality = spec, hooks.DataPlane, strings.EqualFold(security, "reality")
	a.connErr = newConnErrorReporter(hooks, spec, "trojan")
	var err error
	fallbackAddr, err := parseProbeFallback(spec.Config.Raw)
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("trojan %w", err)
	}
	a.fallback = newProbeFallback(fallbackAddr)
	if !a.reality {
		var tlsEnabled bool
		a.tlsConfig, tlsEnabled, err = loadInboundTLSConfig(spec.Config.Raw)
		_ = tlsEnabled
		if err != nil {
			a.mu.Unlock()
			return err
		}
	}
	a.ctx, a.cancel = context.WithCancel(parent)
	listenAddress := spec.Config.Listen
	if listenAddress == "" {
		listenAddress = "0.0.0.0"
	}
	address := net.JoinHostPort(listenAddress, strconv.Itoa(spec.Config.Port))
	var listener net.Listener
	if a.reality {
		realitySpec, parseErr := ParseRealityServerConfig(spec.Config.Raw)
		if parseErr != nil {
			a.mu.Unlock()
			return parseErr
		}
		var realityListener *RealityListener
		realityListener, err = ListenReality("tcp", address, realitySpec, nil)
		if realityListener != nil {
			realityListener.SetHandshakeErrorHandler(a.connErr.realityHandshake)
			listener = realityListener
		}
	} else if isMKCPNetwork(network) {
		// mKCP 跑在 UDP 上，但对上层就是个 net.Listener——下面 TLS、
		// 分片读写那些代码一行都不用改。
		listener, err = ListenMKCP(address, spec.Config.Raw)
	} else {
		listener, err = net.Listen("tcp", address)
	}
	if err != nil {
		a.cancel()
		a.mu.Unlock()
		return err
	}
	a.listener = listener
	a.mu.Unlock()
	if strings.EqualFold(network, "ws") {
		if a.tlsConfig != nil {
			listener = tls.NewListener(listener, a.tlsConfig.Clone())
			a.mu.Lock()
			a.listener = listener
			a.mu.Unlock()
		}
		path, host, pathErr := parseWebSocketTransport(spec.Config.Raw)
		if pathErr != nil {
			_ = listener.Close()
			if a.cancel != nil {
				a.cancel()
			}
			return pathErr
		}
		upgrader := websocket.Upgrader{ReadBufferSize: 32 * 1024, WriteBufferSize: 32 * 1024, CheckOrigin: func(req *http.Request) bool {
			return requestHostMatches(req.Host, host)
		}}
		handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL == nil || req.URL.Path != path || !requestHostMatches(req.Host, host) {
				http.NotFound(w, req)
				return
			}
			wsConn, upgradeErr := upgrader.Upgrade(w, req, nil)
			if upgradeErr != nil {
				return
			}
			conn := newWebSocketNetConn(wsConn)
			a.mu.Lock()
			if a.closed {
				a.mu.Unlock()
				_ = conn.Close()
				return
			}
			a.active[conn] = struct{}{}
			a.wg.Add(1)
			connCtx := a.ctx
			a.mu.Unlock()
			goGuarded(conn, func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConn(connCtx, conn, nil)
			})
		})
		server := newInboundHTTPServer(handler, 64<<10)
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = server.Close()
			return fmt.Errorf("trojan adapter is closed")
		}
		a.httpServer = server
		a.mu.Unlock()
		go func() { _ = server.Serve(listener) }()
		return nil
	}
	if strings.EqualFold(network, "httpupgrade") {
		if a.tlsConfig != nil {
			listener = tls.NewListener(listener, a.tlsConfig.Clone())
			a.mu.Lock()
			a.listener = listener
			a.mu.Unlock()
		}
		path, host, pathErr := parseWebSocketTransport(spec.Config.Raw)
		if pathErr != nil {
			_ = listener.Close()
			if a.cancel != nil {
				a.cancel()
			}
			return pathErr
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !validHTTPUpgradeRequest(path, host, req) {
				http.NotFound(w, req)
				return
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "httpupgrade unavailable", http.StatusHTTPVersionNotSupported)
				return
			}
			rawConn, rw, hijackErr := hijacker.Hijack()
			if hijackErr != nil {
				return
			}
			if responseErr := writeHTTPUpgradeResponse(rw); responseErr != nil {
				_ = rawConn.Close()
				return
			}
			conn := newHijackedNetConn(rawConn, rw.Reader)
			a.mu.Lock()
			if a.closed {
				a.mu.Unlock()
				_ = conn.Close()
				return
			}
			a.active[conn] = struct{}{}
			a.wg.Add(1)
			connCtx := a.ctx
			a.mu.Unlock()
			goGuarded(conn, func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConn(connCtx, conn, nil)
			})
		})
		server := newInboundHTTPServer(handler, 64<<10)
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = server.Close()
			return fmt.Errorf("trojan adapter is closed")
		}
		a.httpServer = server
		a.mu.Unlock()
		go func() { _ = server.Serve(listener) }()
		return nil
	}
	if strings.EqualFold(network, "grpc") {
		path, host, pathErr := parseGRPCPath(spec.Config.Raw)
		if pathErr != nil {
			_ = listener.Close()
			if a.cancel != nil {
				a.cancel()
			}
			return pathErr
		}
		h2cMode := a.tlsConfig == nil
		if a.tlsConfig != nil {
			grpcTLS := a.tlsConfig.Clone()
			grpcTLS.NextProtos = []string{"h2"}
			listener = tls.NewListener(listener, grpcTLS)
			a.mu.Lock()
			a.listener = listener
			a.mu.Unlock()
		}
		onConn := func(connCtx context.Context, conn net.Conn) {
			a.mu.Lock()
			if a.closed {
				a.mu.Unlock()
				_ = conn.Close()
				return
			}
			a.active[conn] = struct{}{}
			a.wg.Add(1)
			a.mu.Unlock()
			defer a.wg.Done()
			defer a.removeActive(conn)
			_ = a.handleConn(connCtx, conn, nil)
		}
		server, serveErr := serveNativeGRPC(listener, path, host, 16<<20, h2cMode, onConn)
		if serveErr != nil {
			_ = listener.Close()
			if a.cancel != nil {
				a.cancel()
			}
			return serveErr
		}
		a.mu.Lock()
		a.httpServer = server
		a.mu.Unlock()
		return nil
	}
	a.wg.Add(1)
	go a.acceptLoop()
	return nil
}

func (a *trojanAdapter) acceptLoop() {
	defer a.wg.Done()
	runAcceptLoop(a.ctx.Done(), a.listener.Accept, a.serveAccepted)
}

// serveAccepted 只登记连接、起 goroutine，立刻返回；TLS 握手与 REALITY 会话
// 检查都在连接自己的 goroutine 里做。登记的是原始连接，握手中的连接 Close
// 也关得到。
func (a *trojanAdapter) serveAccepted(conn net.Conn) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	a.active[conn] = struct{}{}
	a.wg.Add(1)
	ctx, tlsConfig := a.ctx, a.tlsConfig
	a.mu.Unlock()
	goGuarded(conn, func() {
		defer a.wg.Done()
		defer a.removeActive(conn)
		session := conn
		if tlsConfig != nil {
			tlsConn, err := serverTLSHandshake(ctx, conn, tlsConfig, inboundHandshakeTimeout)
			if err != nil {
				a.connErr.conn(StageTLSHandshake, conn, err)
				_ = conn.Close()
				return
			}
			session = tlsConn
		}
		var realitySession *RealitySession
		if a.reality {
			captured, ok := InspectRealityConn(session)
			if !ok {
				a.connErr.conn(StageRealityInspect, session, errors.New("连接上取不到 REALITY 会话信息"))
				_ = session.Close()
				return
			}
			realitySession = &captured
		}
		err := a.serveConn(ctx, session, realitySession, true)
		a.connErr.conn(StageSession, session, err)
	})
}

// handleConn 是 TCP / REALITY / WS / HTTP Upgrade / gRPC 共同的会话入口，
// 会话层失败在这里统一上报一次，换承载不会让失败从日志里消失。
// WS / HTTP Upgrade / gRPC 承载在 HTTP 层已经对错路径回 404，这里不再回落。
func (a *trojanAdapter) handleConn(ctx context.Context, conn net.Conn, realitySession *RealitySession) error {
	err := a.serveConn(ctx, conn, realitySession, false)
	a.connErr.conn(StageSession, conn, err)
	return err
}

// serveConn 跑一条 Trojan 会话。probe 为真（TCP 直连类承载）时，认证判定
// 之前读到的字节被记下，认证失败就交给回落或中性页面，而不是立刻断开。
func (a *trojanAdapter) serveConn(ctx context.Context, conn net.Conn, realitySession *RealitySession, probe bool) error {
	defer conn.Close()
	epoch := a.sessions.epoch()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reader io.Reader = conn
	var recorder *trojanPreAuthRecorder
	if probe {
		recorder = &trojanPreAuthRecorder{r: conn}
		reader = recorder
	}
	user, destination, err := readTrojanRequest(reader, a.lookupUser)
	if err != nil {
		if recorder != nil && recorder.rejected(err) {
			a.mu.RLock()
			fallback := a.fallback
			a.mu.RUnlock()
			fallback.serveConn(ctx, conn, recorder.buf, negotiatedH2(conn))
		}
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	sess := a.sessions.open(user, epoch, conn)
	if sess == nil {
		return errSessionRevoked
	}
	defer sess.close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.online.enter(user, ip) {
		return deviceLimitError("trojan")
	}
	defer a.online.leave(user, ip)
	if destination.Command == trojanCommandUDP {
		return a.handleTrojanUDP(ctx, conn, user, destination, realitySession, ip)
	}
	sourceIP, _ := netip.ParseAddr(ip)
	var sourcePort uint16
	if _, port, splitErr := net.SplitHostPort(conn.RemoteAddr().String()); splitErr == nil {
		if parsed, parseErr := strconv.ParseUint(port, 10, 16); parseErr == nil {
			sourcePort = uint16(parsed)
		}
	}
	protocol := "trojan"
	if realitySession != nil {
		protocol = "trojan-reality"
	}
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: protocol, SourceIP: sourceIP, SourcePort: sourcePort}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	// 上下行共用一个令牌桶：限的是这个用户的带宽，不是单条连接的。
	sess.relay(conn, upstream, core.RelayOptions{Limiter: a.limiters.For(user)})
	return nil
}

func readTrojanRequest(conn io.Reader, lookup func(string) (core.User, bool)) (core.User, vlessDestination, error) {
	var destination vlessDestination
	var proof [trojanProofLen]byte
	if err := readTrojanProof(conn, &proof); err != nil {
		return core.User{}, destination, err
	}
	user, ok := lookup(string(proof[:56]))
	if !ok {
		return core.User{}, destination, markConnError(connErrAuth, errTrojanUserRejected)
	}
	if recorder, ok := conn.(*trojanPreAuthRecorder); ok {
		recorder.authenticated()
	}
	// Trojan 请求头是 CMD | ATYP | DST.ADDR | DST.PORT，没有 SOCKS5 那个
	// VER 字节，也没有 RSV。这里一度按 SOCKS5 的四字节头解析，把 CMD 当
	// 成版本字节比对，还多吃两个字节——真实客户端一连就被拒。之所以能
	// 一直没被发现，是因为单元测试按同一套错误格式编码请求，自己和自己
	// 对得上。
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return core.User{}, destination, err
	}
	if header[0] != 1 && header[0] != trojanCommandUDP {
		return core.User{}, destination, fmt.Errorf("trojan supports TCP CONNECT and UDP ASSOCIATE")
	}
	destination.Command = header[0]
	if err := readTrojanAddress(conn, header[1], &destination); err != nil {
		return core.User{}, destination, err
	}
	var trailer [2]byte
	if _, err := io.ReadFull(conn, trailer[:]); err != nil {
		return core.User{}, destination, err
	}
	if trailer != [2]byte{'\r', '\n'} {
		return core.User{}, destination, fmt.Errorf("trojan request terminator invalid")
	}
	return user, destination, nil
}

func readTrojanAddress(conn io.Reader, addressType byte, destination *vlessDestination) error {
	switch addressType {
	case 1:
		var buf [4]byte
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return err
		}
		destination.IP = netip.AddrFrom4(buf)
		destination.Host = destination.IP.String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return err
		}
		if length[0] == 0 || length[0] > 253 {
			return fmt.Errorf("trojan domain length invalid")
		}
		buf := make([]byte, length[0])
		if _, err := io.ReadFull(conn, buf); err != nil {
			return err
		}
		destination.Domain, destination.Host = string(buf), string(buf)
	case 4:
		var buf [16]byte
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return err
		}
		destination.IP = netip.AddrFrom16(buf)
		destination.Host = destination.IP.String()
	default:
		return fmt.Errorf("trojan address type %d unsupported", addressType)
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return err
	}
	destination.Port = uint16(port[0])<<8 | uint16(port[1])
	if destination.Port == 0 {
		return fmt.Errorf("trojan destination port invalid")
	}
	return nil
}

// lookupUser 按口令哈希直接查表。
//
// 原先对全部用户逐个做常量时间比较（压测时握手 CPU 的约 22%）。表的键是口令的
// SHA-224，查表时间最多泄露「某个已有键的前缀」，那是摘要不是口令，凭它造不出
// 能通过的口令（要找原像）；Xray / trojan-go 同样按哈希查表。
func (a *trojanAdapter) lookupUser(proof string) (core.User, bool) {
	a.mu.RLock()
	metadata, ok := a.users[proof]
	a.mu.RUnlock()
	if !ok {
		return core.User{}, false
	}
	return core.User{ID: metadata.ID, DeviceLimit: metadata.DeviceLimit, SpeedLimit: metadata.SpeedLimit}, true
}

func (a *trojanAdapter) AddUsers(users []core.User) error {
	validated := make([]trojanUserEntry, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" || len(user.UUID) > 256 {
			return fmt.Errorf("trojan user %d password invalid", user.ID)
		}
		validated = append(validated, trojanUserEntry{proof: trojanPasswordProof(user.UUID), user: user})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("trojan adapter closed")
	}
	for _, entry := range validated {
		if _, exists := a.users[entry.proof]; exists {
			continue
		}
		a.users[entry.proof] = trojanUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit}
	}
	return nil
}

type trojanUserEntry struct {
	proof string
	user  core.User
}

func (a *trojanAdapter) UpsertUsers(users []core.User) error {
	validated := make([]trojanUserEntry, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" || len(user.UUID) > 256 {
			return fmt.Errorf("trojan user %d password invalid", user.ID)
		}
		validated = append(validated, trojanUserEntry{proof: trojanPasswordProof(user.UUID), user: user})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("trojan adapter closed")
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.proof]; exists {
			a.limiters.Remove(previous.ID)
		}
		a.users[entry.proof] = trojanUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit}
	}
	return nil
}

func (a *trojanAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	var removed []int64
	for _, password := range ids {
		proof := trojanPasswordProof(password)
		// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着，map 只增不减。
		if metadata, ok := a.users[proof]; ok {
			a.limiters.Remove(metadata.ID)
			removed = append(removed, metadata.ID)
		}
		delete(a.users, proof)
	}
	a.mu.Unlock()
	// 先删表、再踢线（锁外关）：已有的长连接、mux / QUIC 会话随之断开。
	a.sessions.revoke(removed)
	return nil
}

func (a *trojanAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
}

func (a *trojanAdapter) OnlineIPs() map[int64][]string { return a.online.snapshot() }

func (a *trojanAdapter) addTraffic(user core.User, upload, download int64) {
	a.sessions.add(user.ID, upload, download)
}

func (a *trojanAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	listener := a.listener
	active := make([]net.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range active {
		_ = conn.Close()
	}
	a.wg.Wait()
	return nil
}

func (a *trojanAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}

func trojanPasswordProof(password string) string {
	sum := sha256.Sum224([]byte(password))
	return strings.ToLower(hex.EncodeToString(sum[:]))
}

func trojanProofEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
