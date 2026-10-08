package kernel

import (
	"context"
	"crypto/tls"
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

	"github.com/gorilla/websocket"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

const (
	vlessVersion byte = 0
	vlessTCP     byte = 1
	vlessUDP     byte = 2
	vlessMux     byte = 3
)

type vlessAdapter struct {
	spec     InboundSpec
	mu       sync.RWMutex
	users    map[string]core.User
	limiters core.SpeedLimiters
	// sessions 是按用户的在途连接表与原子流量计数（user_sessions.go）。
	sessions      userSessions
	online        map[int64]map[string]struct{}
	listener      net.Listener
	packet        net.PacketConn
	httpServer    *http.Server
	h3Server      interface{ Close() error }
	plane         DataPlane
	connErr       connErrorReporter
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	active        map[net.Conn]struct{}
	reality       bool
	tlsConfig     *tls.Config
	xhttpConfig   XHTTPConfig
	xhttpBroker   *XHTTPPacketBroker
	xhttpSessions map[string]*xhttpSession
	// fallback 是 TCP 直连承载认证失败时的回落（raw `fallback`）；空即中性 404。
	fallback *probeFallback
	wg       sync.WaitGroup
}

func NewDefaultAdapterRegistry() *AdapterRegistry {
	r := NewAdapterRegistry()
	_ = r.Register("vless", newVLESSAdapter)
	_ = r.Register("trojan", newTrojanAdapter)
	_ = r.Register("vmess", newVMessAdapter)
	_ = r.Register("shadowsocks", newShadowsocksAdapter)
	_ = r.Register("ss", newShadowsocksAdapter)
	_ = r.Register("hysteria2", newHysteria2Adapter)
	_ = r.Register("tuic", newTUICAdapter)
	_ = r.Register("anytls", newAnyTLSAdapter)
	_ = r.Register("socks", newSOCKSAdapter)
	_ = r.Register("http", newHTTPProxyAdapter)
	_ = r.Register("naive", newNaiveAdapter)
	_ = r.Register("shadowtls", newShadowTLSAdapter)
	_ = r.Register("mieru", newMieruAdapter)
	_ = r.Register("juicity", newJuicityAdapter)
	return r
}
func newVLESSAdapter(spec InboundSpec) (Adapter, error) {
	return &vlessAdapter{spec: spec, users: make(map[string]core.User), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}), xhttpSessions: make(map[string]*xhttpSession)}, nil
}
func (a *vlessAdapter) Protocol() string { return "vless" }
func (a *vlessAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "vless") {
		return fmt.Errorf("vless 适配器收到协议 %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("vless 端口无效")
	}
	network, _ := spec.Config.Raw["network"].(string)
	if network != "" && !strings.EqualFold(network, "tcp") && !strings.EqualFold(network, "ws") && !strings.EqualFold(network, "httpupgrade") && !strings.EqualFold(network, "grpc") && !strings.EqualFold(network, "xhttp") && !strings.EqualFold(network, "xhttp-h3") && !isMKCPNetwork(network) {
		return fmt.Errorf("原生 vless 当前只接受 tcp/ws/httpupgrade/grpc/xhttp/xhttp-h3")
	}
	if strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") {
		if _, _, err := parseWebSocketTransport(spec.Config.Raw); err != nil {
			return err
		}
	}
	if strings.EqualFold(network, "grpc") {
		if _, _, err := parseGRPCPath(spec.Config.Raw); err != nil {
			return err
		}
	}
	if network, _ := spec.Config.Raw["network"].(string); strings.EqualFold(network, "xhttp") || strings.EqualFold(network, "xhttp-h3") {
		if _, err := ParseXHTTPConfig(spec.Config.Raw); err != nil {
			return err
		}
		if strings.EqualFold(network, "xhttp-h3") && !strings.EqualFold(stringValue(spec.Config.Raw["security"]), "reality") && (strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")) == "" || strings.TrimSpace(rawString(spec.Config.Raw, "key_path")) == "") {
			return fmt.Errorf("vless xhttp-h3 requires cert_path and key_path")
		}
	}
	if _, err := parseProbeFallback(spec.Config.Raw); err != nil {
		return fmt.Errorf("vless %w", err)
	}
	security, _ := spec.Config.Raw["security"].(string)
	if strings.EqualFold(security, "reality") {
		if _, err := ParseRealityServerConfig(spec.Config.Raw); err != nil {
			return err
		}
	} else if security != "" && !strings.EqualFold(security, "none") {
		return fmt.Errorf("原生 vless 暂不接受安全层 %q；避免静默降级", security)
	}
	if enabled, ok := spec.Config.Raw["tls"].(bool); ok && enabled {
		if strings.EqualFold(network, "xhttp") || strings.EqualFold(network, "xhttp-h3") {
			return fmt.Errorf("native vless TLS is currently supported on TCP only")
		}
		if !strings.EqualFold(security, "reality") {
			if _, _, err := loadInboundTLSConfig(spec.Config.Raw); err != nil {
				return err
			}
		}
	}
	return nil
}
func (a *vlessAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("vless 启动缺少 context 或 data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.packet != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("vless 适配器已启动或已关闭")
	}
	a.spec, a.plane, a.connErr = spec, hooks.DataPlane, newConnErrorReporter(hooks, spec, "vless")
	fallbackAddr, fallbackErr := parseProbeFallback(spec.Config.Raw)
	if fallbackErr != nil {
		a.mu.Unlock()
		return fmt.Errorf("vless %w", fallbackErr)
	}
	a.fallback = newProbeFallback(fallbackAddr)
	security, _ := spec.Config.Raw["security"].(string)
	a.reality = strings.EqualFold(security, "reality")
	var tlsEnabled bool
	if !a.reality {
		var err error
		a.tlsConfig, tlsEnabled, err = loadInboundTLSConfig(spec.Config.Raw)
		if err != nil {
			a.mu.Unlock()
			return err
		}
	}
	if a.active == nil {
		a.active = make(map[net.Conn]struct{})
	}
	if a.xhttpSessions == nil {
		a.xhttpSessions = make(map[string]*xhttpSession)
	}
	a.ctx, a.cancel = context.WithCancel(parent)
	network, _ := spec.Config.Raw["network"].(string)
	listenAddress := spec.Config.Listen
	if listenAddress == "" {
		listenAddress = "0.0.0.0"
	}
	address := net.JoinHostPort(listenAddress, fmt.Sprintf("%d", spec.Config.Port))
	if strings.EqualFold(network, "xhttp-h3") {
		xhttpConfig, parseErr := ParseXHTTPConfig(spec.Config.Raw)
		if parseErr != nil {
			a.cancel()
			a.mu.Unlock()
			return parseErr
		}
		var cert tls.Certificate
		var realitySpec RealityServerConfig
		if a.reality {
			var realityErr error
			realitySpec, realityErr = ParseRealityServerConfig(spec.Config.Raw)
			if realityErr != nil {
				a.cancel()
				a.mu.Unlock()
				return realityErr
			}
		} else {
			var certErr error
			cert, certErr = tls.LoadX509KeyPair(rawString(spec.Config.Raw, "cert_path"), rawString(spec.Config.Raw, "key_path"))
			if certErr != nil {
				a.cancel()
				a.mu.Unlock()
				return fmt.Errorf("vless xhttp-h3 TLS: %w", certErr)
			}
		}
		packet, listenErr := net.ListenPacket("udp", address)
		if listenErr != nil {
			a.cancel()
			a.mu.Unlock()
			return listenErr
		}
		a.xhttpConfig = xhttpConfig
		if xhttpUsesSessions(xhttpConfig.Mode) {
			a.xhttpBroker, parseErr = NewXHTTPPacketBroker(xhttpConfig.MaxBufferedPosts, 5*time.Minute)
			if parseErr != nil {
				_ = packet.Close()
				a.cancel()
				a.mu.Unlock()
				return parseErr
			}
		}
		var h3Server interface{ Close() error }
		var serveErr error
		if a.reality {
			h3Server, serveErr = (XHTTPServer{Config: xhttpConfig, Handler: a.xhttpHandler()}).ServeH3Reality(packet, realitySpec, nil)
		} else {
			h3Server, serveErr = (XHTTPServer{Config: xhttpConfig, Handler: a.xhttpHandler()}).ServeH3(packet, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
		}
		if serveErr != nil {
			_ = packet.Close()
			a.cancel()
			a.mu.Unlock()
			return serveErr
		}
		a.packet, a.h3Server = packet, h3Server
		a.mu.Unlock()
		return nil
	}
	_ = tlsEnabled
	var listener net.Listener
	var err error
	if strings.EqualFold(security, "reality") {
		realitySpec, parseErr := ParseRealityServerConfig(spec.Config.Raw)
		if parseErr != nil {
			a.mu.Unlock()
			return parseErr
		}
		realityListener, realityErr := ListenReality("tcp", address, realitySpec, nil)
		if realityListener != nil {
			// REALITY 握手在 listener 内部完成，失败的连接根本到不了
			// acceptLoop。不在这里接一道，adapter 上那个 OnConnError
			// 永远看不到握手层的任何东西。
			realityListener.SetHandshakeErrorHandler(a.connErr.realityHandshake)
		}
		listener, err = realityListener, realityErr
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
			a.cancel()
			return pathErr
		}
		upgrader := websocket.Upgrader{
			ReadBufferSize:  32 * 1024,
			WriteBufferSize: 32 * 1024,
			CheckOrigin: func(req *http.Request) bool {
				return requestHostMatches(req.Host, host)
			},
		}
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
			goGuardedConn(conn, func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConnSession(connCtx, conn, nil)
			})
		})
		server := newInboundHTTPServer(handler, 64<<10)
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = server.Close()
			return fmt.Errorf("vless adapter is closed")
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
			a.cancel()
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
			goGuardedConn(conn, func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConnSession(connCtx, conn, nil)
			})
		})
		server := newInboundHTTPServer(handler, 64<<10)
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = server.Close()
			return fmt.Errorf("vless adapter is closed")
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
			a.cancel()
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
			_ = a.handleConnSession(connCtx, conn, nil)
		}
		hosts := []string{host}
		if a.reality && strings.TrimSpace(host) != "" {
			// REALITY 下客户端的 :authority 是 REALITY server name（见 serveNativeGRPCHosts）
			if realitySpec, err := ParseRealityServerConfig(spec.Config.Raw); err == nil {
				for name := range realitySpec.ServerNames {
					hosts = append(hosts, name)
				}
			}
		}
		server, serveErr := serveNativeGRPCHosts(listener, path, hosts, 16<<20, h2cMode, onConn)
		if serveErr != nil {
			_ = listener.Close()
			a.cancel()
			return serveErr
		}
		a.mu.Lock()
		a.httpServer = server
		a.mu.Unlock()
		return nil
	}
	if strings.EqualFold(network, "xhttp") {
		xhttpConfig, parseErr := ParseXHTTPConfig(spec.Config.Raw)
		if parseErr != nil {
			_ = listener.Close()
			a.cancel()
			return parseErr
		}
		a.mu.Lock()
		a.xhttpConfig = xhttpConfig
		if xhttpUsesSessions(xhttpConfig.Mode) {
			a.xhttpBroker, parseErr = NewXHTTPPacketBroker(xhttpConfig.MaxBufferedPosts, 5*time.Minute)
			if parseErr != nil {
				a.mu.Unlock()
				_ = listener.Close()
				a.cancel()
				return parseErr
			}
		}
		a.mu.Unlock()
		xhttpServer := XHTTPServer{Config: xhttpConfig, Handler: a.xhttpHandler()}
		server, serveErr := xhttpServer.Serve(listener)
		if serveErr != nil {
			_ = listener.Close()
			a.cancel()
			return serveErr
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = server.Close()
			return fmt.Errorf("vless 适配器已关闭")
		}
		a.httpServer = server
		a.mu.Unlock()
		return nil
	}
	a.wg.Add(1)
	go a.acceptLoop()
	return nil
}
func (a *vlessAdapter) xhttpHandler() XHTTPHandler {
	return func(ctx context.Context, session XHTTPSession) error {
		if session.Kind != XHTTPRequestDuplex {
			return a.xhttpPacketHandler(ctx, session)
		}
		conn := newXHTTPDuplexConn(ctx, session.Body, session.Writer)
		defer conn.Close()
		var realitySession *RealitySession
		if captured, ok := RealitySessionFromContext(ctx); ok {
			realitySession = &captured
		}
		return a.handleConnSession(ctx, conn, realitySession)
	}
}
func (a *vlessAdapter) acceptLoop() {
	defer a.wg.Done()
	runAcceptLoop(a.ctx.Done(), a.listener.Accept, a.serveAccepted)
}

// serveAccepted 只登记连接、起 goroutine，立刻返回；TLS 握手与 REALITY 会话
// 检查都在连接自己的 goroutine 里做（REALITY 握手更早，在 RealityListener 的
// 握手 worker 里）。登记的是原始连接，握手中的连接 Close 也关得到。
func (a *vlessAdapter) serveAccepted(conn net.Conn) {
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
	goGuardedConn(conn, func() {
		defer a.wg.Done()
		defer a.removeActive(conn)
		session := conn
		if tlsConfig != nil {
			tlsConn, err := serverTLSHandshake(ctx, conn, tlsConfig, inboundHandshakeTimeout)
			if err != nil {
				a.reportConnError(StageTLSHandshake, conn, err)
				_ = conn.Close()
				return
			}
			session = tlsConn
		}
		var realitySession *RealitySession
		if a.reality {
			captured, ok := InspectRealityConn(session)
			if !ok {
				a.reportConnError(StageRealityInspect, session,
					errors.New("连接上取不到 REALITY 会话信息"))
				_ = session.Close()
				return
			}
			realitySession = &captured
		}
		_ = a.handleConnSessionProbe(ctx, session, realitySession, true)
	})
}
func (a *vlessAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	return a.handleConnSession(ctx, conn, nil)
}

// reportConnError 把一条连接的失败原因交给观测出口。
//
// 正常关闭不算失败：io.EOF 和 net.ErrClosed 在每条连接结束时都会出现，
// 报上去只会把真正的错误淹掉。
func (a *vlessAdapter) reportConnError(stage string, conn net.Conn, err error) {
	a.connErr.conn(stage, conn, err)
}

// handleConnSession 是所有承载（TCP / REALITY / WS / HTTP Upgrade / gRPC /
// XHTTP）共同的会话入口，会话层失败在这里统一上报一次。
//
// 以前只有 TCP 的 acceptLoop 报，其余承载都是 `_ =` 丢掉——换个 network
// 配置，同一个「用户未授权」就从日志里消失了。
func (a *vlessAdapter) handleConnSession(ctx context.Context, conn net.Conn, realitySession *RealitySession) error {
	return a.handleConnSessionProbe(ctx, conn, realitySession, false)
}

// handleConnSessionProbe 的 probe 为真（TCP 直连类承载）时，认证判定失败的连接
// 交给回落或中性页面；WS / HTTP Upgrade / gRPC / XHTTP 在 HTTP 层已对错路径回 404。
func (a *vlessAdapter) handleConnSessionProbe(ctx context.Context, conn net.Conn, realitySession *RealitySession, probe bool) error {
	err := a.serveConnSession(ctx, conn, realitySession, probe)
	a.reportConnError(StageSession, conn, err)
	return err
}

func (a *vlessAdapter) serveConnSession(ctx context.Context, conn net.Conn, realitySession *RealitySession, probe bool) error {
	defer conn.Close()
	// epoch 要在读请求（查用户）之前取，见 userSessions 的竞态说明。
	epoch := a.sessions.epoch()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 请求头是裸的，Vision 只包它之后的数据。
	//
	// 这一点是拿真实 Mihomo 抓出来的：REALITY 之上第一个应用数据记录的
	// 首字节是 0x00（VLESS version），后面才是 UUID、addons。上游那句
	// 「we do a long padding to hide vless header」说的是客户端把首帧的
	// 填充做长，用长度掩盖头部特征，不是把头部本身塞进帧里。
	var reader io.Reader = conn
	var recorder *vlessPreAuthRecorder
	if probe {
		recorder = &vlessPreAuthRecorder{r: conn}
		reader = recorder
	}
	user, destination, err := readVLESSRequest(reader, a.lookupUser)
	if err != nil {
		if recorder != nil && recorder.rejected(err) {
			// 先报失败：回落会话可能持续到对端断开或空闲超时，观测不能等它。
			a.reportConnError(StageSession, conn, err)
			a.mu.RLock()
			fallback := a.fallback
			a.mu.RUnlock()
			fallback.serveConn(ctx, conn, recorder.buf, negotiatedH2(conn))
			return nil
		}
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	// 登记到用户连接表：用户被移出名单时这条连接（含其上的 mux / UDP）被关掉。
	sess := a.sessions.open(user, epoch, conn)
	if sess == nil {
		return errSessionRevoked
	}
	defer sess.close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return deviceLimitError("vless")
	}
	defer a.leaveDevice(user, ip)
	if _, err := conn.Write([]byte{vlessVersion, 0}); err != nil {
		return err
	}
	if destination.Vision {
		// Plain UDP over Vision is intentionally rejected by upstream clients.
		// XUDP/mux is the supported Vision UDP path and is handled below.
		if destination.Command == vlessUDP {
			return fmt.Errorf("vless flow %s 不支持裸 UDP 命令，请使用 XUDP/mux", destination.Flow)
		}
		// VLESS 响应头（上面那两个字节）必须裸发，Vision 从它之后开始。
		visionConn, visionErr := NewVisionConn(conn, destination.RawUUID[:])
		if visionErr != nil {
			return visionErr
		}
		conn = visionConn
	}
	if destination.Command == vlessUDP {
		return a.handleVLESSUDP(ctx, conn, user, destination, realitySession, ip)
	}
	if destination.Command == vlessMux {
		return a.handleVLESSMux(ctx, conn, user, realitySession, ip)
	}
	sourceIP, _ := netip.ParseAddr(ip)
	var sourcePort uint16
	if _, port, parseErr := net.SplitHostPort(conn.RemoteAddr().String()); parseErr == nil {
		if parsed, parseErr := strconv.ParseUint(port, 10, 16); parseErr == nil {
			sourcePort = uint16(parsed)
		}
	}
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "vless", SourceIP: sourceIP, SourcePort: sourcePort}
	if realitySession != nil {
		// Keep the authenticated transport boundary explicit for future policy
		// hooks; the route contract remains protocol-agnostic.
		meta.Protocol = "vless-reality"
	}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	// 上下行共用一个令牌桶：限的是这个用户的带宽，不是单条连接的。半关闭的
	// 双向传递、单向收尾与空闲回收、按块计数都在 core.Relay 里。
	sess.relay(conn, upstream, core.RelayOptions{Limiter: a.limiters.For(user)})
	return nil
}

func (a *vlessAdapter) Close() error {
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
	packet := a.packet
	httpServer := a.httpServer
	h3Server := a.h3Server
	active := make([]net.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	packetSessions := make([]*xhttpSession, 0, len(a.xhttpSessions))
	for _, session := range a.xhttpSessions {
		packetSessions = append(packetSessions, session)
	}
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if packet != nil {
		_ = packet.Close()
	}
	if httpServer != nil {
		_ = httpServer.Close()
	}
	if h3Server != nil {
		_ = h3Server.Close()
	}
	for _, conn := range active {
		_ = conn.Close()
	}
	for _, session := range packetSessions {
		session.cancel()
		_ = session.duplex.Uplink.Close(net.ErrClosed)
		_ = session.duplex.Downlink.Close(net.ErrClosed)
	}
	a.wg.Wait()
	return nil
}
func (a *vlessAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}
func remoteIP(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
