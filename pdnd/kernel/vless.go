// [INPUT]: 依赖 adapter.go 的 Adapter 契约与 DataPlane，依赖 reality_listener.go、vision.go、xhttp_server.go、websocket_netconn.go 等承载，依赖 core 的用户与 route 的路由
// [OUTPUT]: 对外提供 NewDefaultAdapterRegistry（全部原生协议的注册表）与 vlessAdapter 的 Protocol、Validate、Start、Close；包内 acceptLoop、handleConn、handleConnSession、remoteIP
// [POS]: kernel 的 VLESS 入站主体：TCP / REALITY / WebSocket / HTTP Upgrade / XHTTP 的监听与分派、TCP 转发；请求头解析在 vless_request.go，flow 在 vless_flow.go，mux 在 vless_mux.go，UDP 在 vless_udp.go，用户表在 vless_users.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
	spec          InboundSpec
	mu            sync.RWMutex
	users         map[string]core.User
	limiters      core.SpeedLimiters
	traffic       map[int64]core.UserTraffic
	online        map[int64]map[string]struct{}
	listener      net.Listener
	packet        net.PacketConn
	httpServer    *http.Server
	h3Server      interface{ Close() error }
	plane         DataPlane
	onConnError   func(ConnError)
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	active        map[net.Conn]struct{}
	reality       bool
	tlsConfig     *tls.Config
	xhttpConfig   XHTTPConfig
	xhttpBroker   *XHTTPPacketBroker
	xhttpSessions map[string]*vlessXHTTPPacketSession
	wg            sync.WaitGroup
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
	return &vlessAdapter{spec: spec, users: make(map[string]core.User), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}), xhttpSessions: make(map[string]*vlessXHTTPPacketSession)}, nil
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
	a.spec, a.plane, a.onConnError = spec, hooks.DataPlane, hooks.OnConnError
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
		a.xhttpSessions = make(map[string]*vlessXHTTPPacketSession)
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
		if isXHTTPPacketMode(xhttpConfig.Mode) {
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
			realityListener.SetHandshakeErrorHandler(func(remote net.Addr, hsErr error) {
				if a.onConnError == nil {
					return
				}
				a.onConnError(ConnError{
					Tag: a.spec.Config.Tag, Protocol: "vless",
					Stage: StageTLSHandshake, Remote: remote, Err: hsErr,
				})
			})
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
				return host == "" || strings.EqualFold(strings.TrimSpace(req.Host), host)
			},
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL == nil || req.URL.Path != path || (host != "" && !strings.EqualFold(strings.TrimSpace(req.Host), host)) {
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
			go func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConnSession(connCtx, conn, nil)
			}()
		})
		server := &http.Server{Handler: handler, MaxHeaderBytes: 64 << 10}
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
			go func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConnSession(connCtx, conn, nil)
			}()
		})
		server := &http.Server{Handler: handler, MaxHeaderBytes: 64 << 10}
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
		server, serveErr := serveNativeGRPC(listener, path, host, 16<<20, h2cMode, onConn)
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
		if isXHTTPPacketMode(xhttpConfig.Mode) {
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
		if isXHTTPPacketMode(a.xhttpConfig.Mode) {
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
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			a.mu.RLock()
			closed := a.closed
			a.mu.RUnlock()
			if closed || a.ctx.Err() != nil {
				return
			}
			continue
		}
		a.wg.Add(1)
		a.mu.Lock()
		a.active[conn] = struct{}{}
		a.mu.Unlock()
		go func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			if a.tlsConfig != nil {
				tlsConn := tls.Server(conn, a.tlsConfig.Clone())
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				if err := tlsConn.HandshakeContext(a.ctx); err != nil {
					a.reportConnError(StageTLSHandshake, conn, err)
					_ = conn.Close()
					return
				}
				_ = conn.SetReadDeadline(time.Time{})
				conn = tlsConn
			}
			var realitySession *RealitySession
			if a.reality {
				captured, ok := InspectRealityConn(conn)
				if !ok {
					a.reportConnError(StageRealityInspect, conn,
						errors.New("连接上取不到 REALITY 会话信息"))
					_ = conn.Close()
					return
				}
				realitySession = &captured
			}
			if err := a.handleConnSession(a.ctx, conn, realitySession); err != nil {
				a.reportConnError(StageSession, conn, err)
			}
		}()
	}
}
func (a *vlessAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	return a.handleConnSession(ctx, conn, nil)
}

// reportConnError 把一条连接的失败原因交给观测出口。
//
// 正常关闭不算失败：io.EOF 和 net.ErrClosed 在每条连接结束时都会出现，
// 报上去只会把真正的错误淹掉。
func (a *vlessAdapter) reportConnError(stage string, conn net.Conn, err error) {
	reportAdapterConnError(a.onConnError, a.spec.Config.Tag, "vless", stage, conn, err)
}
func (a *vlessAdapter) handleConnSession(ctx context.Context, conn net.Conn, realitySession *RealitySession) error {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 请求头是裸的，Vision 只包它之后的数据。
	//
	// 这一点是拿真实 Mihomo 抓出来的：REALITY 之上第一个应用数据记录的
	// 首字节是 0x00（VLESS version），后面才是 UUID、addons。上游那句
	// 「we do a long padding to hide vless header」说的是客户端把首帧的
	// 填充做长，用长度掩盖头部特征，不是把头部本身塞进帧里。
	user, destination, err := readVLESSRequest(conn, a.lookupUser)
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("vless device limit")
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
	// 上下行共用一个令牌桶：限的是这个用户的带宽，不是单条连接的。
	limiter := a.limiters.For(user)
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		n, _ := core.SpeedLimitedCopy(upstream, conn, limiter)
		a.addTraffic(user, n, 0)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		copyWG.Done()
	}()
	go func() {
		n, _ := core.SpeedLimitedCopy(conn, upstream, limiter)
		a.addTraffic(user, 0, n)
		// 上游收完关掉了写端，这个关闭要传给客户端，否则客户端不知道
		// 响应已经结束——HTTP/1.1 的 Connection: close 正是靠 EOF 判断
		// 收尾的，收不到就一直挂着，直到自己超时。连接也就一直不释放。
		//
		// 反方向（客户端 → 上游）本来就有这一步，缺的只是回程。
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		copyWG.Done()
	}()
	copyWG.Wait()
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
	packetSessions := make([]*vlessXHTTPPacketSession, 0, len(a.xhttpSessions))
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
