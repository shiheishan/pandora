package kernel

import (
	"bufio"
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

const (
	vmessVersion   byte = 1
	vmessTCP       byte = 1
	vmessUDP       byte = 2
	vmessMux       byte = 3
	vmessSecLegacy byte = 1
	vmessSecAuto   byte = 2
	vmessSecAES128 byte = 3
	vmessSecChaCha byte = 4
	vmessSecNone   byte = 5
	vmessSecZero   byte = 6
	vmessOptChunk  byte = 1
	vmessOptMask   byte = 4
)

var vmessKDFRoot = []byte("VMess AEAD KDF")

var vmessAddressSerializer = M.NewSerializer(
	M.AddressFamilyByte(0x01, M.AddressFamilyIPv4),
	M.AddressFamilyByte(0x03, M.AddressFamilyIPv6),
	M.AddressFamilyByte(0x02, M.AddressFamilyFqdn),
	M.PortThenAddress(),
)

type vmessAdapter struct {
	spec  InboundSpec
	mu    sync.RWMutex
	users map[string]vmessUser
	// authCandidates 是认证用的只读快照（vmess_auth.go），每次用户变更后重建；
	// 握手直接读它，不加锁、不复制、不再逐用户重做 KDF 与 aes.NewCipher。
	authCandidates atomic.Pointer[[]vmessUserCandidate]
	sessions       userSessions
	online         map[int64]map[string]struct{}
	listener       net.Listener
	packet         net.PacketConn
	httpServer     *http.Server
	h3Server       interface{ Close() error }
	xhttpConfig    XHTTPConfig
	tlsConfig      *tls.Config
	plane          DataPlane
	connErr        connErrorReporter
	limiters       core.SpeedLimiters
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	lastErr        error
	active         map[net.Conn]struct{}
	replay         *replayFilter
	replayOnce     sync.Once
	// headerTimeout 只给测试缩短读请求头的截止时间，零值为 10 秒。
	headerTimeout time.Duration
	xhttpBroker   *XHTTPPacketBroker
	xhttpSessions map[string]*vmessXHTTPPacketSession
	wg            sync.WaitGroup
}

type vmessUser struct {
	ID          int64
	DeviceLimit int
	// SpeedLimit 之前没存，面板下发的限速到这里就丢了。
	SpeedLimit int
	key        [16]byte
	// authBlock 是 AuthID 的 AES 解密块，加用户时算好（KDF + aes.NewCipher）。
	authBlock cipher.Block
}

func newVMessAdapter(spec InboundSpec) (Adapter, error) {
	return &vmessAdapter{spec: spec, users: make(map[string]vmessUser), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}), xhttpSessions: make(map[string]*vmessXHTTPPacketSession)}, nil
}

func (a *vmessAdapter) Protocol() string { return "vmess" }

func (a *vmessAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "vmess") {
		return fmt.Errorf("vmess adapter received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("vmess port is invalid")
	}
	network, _ := spec.Config.Raw["network"].(string)
	if network != "" && !strings.EqualFold(network, "tcp") && !strings.EqualFold(network, "ws") && !strings.EqualFold(network, "httpupgrade") && !strings.EqualFold(network, "grpc") && !strings.EqualFold(network, "xhttp") && !strings.EqualFold(network, "xhttp-h3") && !isMKCPNetwork(network) {
		return fmt.Errorf("native vmess currently accepts tcp/ws/httpupgrade/grpc/xhttp/xhttp-h3 only")
	}
	if strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") {
		if _, _, err := parseWebSocketTransport(spec.Config.Raw); err != nil {
			return err
		}
	}
	if strings.EqualFold(network, "xhttp") || strings.EqualFold(network, "xhttp-h3") {
		_, err := ParseXHTTPConfig(spec.Config.Raw)
		if err != nil {
			return err
		}
		if strings.EqualFold(network, "xhttp-h3") && (strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")) == "" || strings.TrimSpace(rawString(spec.Config.Raw, "key_path")) == "") {
			return fmt.Errorf("vmess xhttp-h3 requires cert_path and key_path")
		}
	}
	if enabled, ok := spec.Config.Raw["tls"].(bool); ok && enabled {
		if _, _, err := loadInboundTLSConfig(spec.Config.Raw); err != nil {
			return err
		}
	}
	security, _ := spec.Config.Raw["security"].(string)
	if security != "" && !strings.EqualFold(security, "none") && !strings.EqualFold(security, "zero") && !strings.EqualFold(security, "aes-128-gcm") && !strings.EqualFold(security, "chacha20-poly1305") && !strings.EqualFold(security, "auto") {
		return fmt.Errorf("native vmess currently accepts none/zero/aes-128-gcm/chacha20-poly1305/auto; unsupported %q", security)
	}
	if alter, ok := spec.Config.Raw["alterId"]; ok && fmt.Sprint(alter) != "" && fmt.Sprint(alter) != "0" {
		return fmt.Errorf("native vmess does not accept legacy alterId")
	}
	return nil
}

func (a *vmessAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("vmess start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.packet != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("vmess adapter already started or closed")
	}
	a.spec, a.plane, a.connErr = spec, hooks.DataPlane, newConnErrorReporter(hooks, spec, "vmess")
	a.ctx, a.cancel = context.WithCancel(parent)
	if a.active == nil {
		a.active = make(map[net.Conn]struct{})
	}
	if a.xhttpSessions == nil {
		a.xhttpSessions = make(map[string]*vmessXHTTPPacketSession)
	}
	var tlsErr error
	a.tlsConfig, _, tlsErr = loadInboundTLSConfig(spec.Config.Raw)
	if tlsErr != nil {
		a.cancel()
		a.mu.Unlock()
		return tlsErr
	}
	network, _ := spec.Config.Raw["network"].(string)
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	var ln net.Listener
	var err error
	if isMKCPNetwork(network) {
		// mKCP 跑在 UDP 上，但对上层就是个 net.Listener——下面 TLS、
		// 分片读写那些代码一行都不用改。
		ln, err = ListenMKCP(net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)), spec.Config.Raw)
		if err != nil {
			a.cancel()
			a.mu.Unlock()
			return err
		}
		// 以前这里漏了这一句：acceptLoop 拿着 nil 的 a.listener 直接 panic，
		// Close 也关不到这个 mKCP 监听器。vless / trojan 一直是统一赋值。
		a.listener = ln
	} else if !strings.EqualFold(network, "xhttp-h3") {
		ln, err = net.Listen("tcp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
		if err != nil {
			a.cancel()
			a.mu.Unlock()
			return err
		}
		a.listener = ln
	}
	a.mu.Unlock()
	if strings.EqualFold(network, "xhttp-h3") {
		xhttpConfig, parseErr := ParseXHTTPConfig(spec.Config.Raw)
		if parseErr != nil {
			a.cancel()
			return parseErr
		}
		cert, certErr := tls.LoadX509KeyPair(rawString(spec.Config.Raw, "cert_path"), rawString(spec.Config.Raw, "key_path"))
		if certErr != nil {
			a.cancel()
			return fmt.Errorf("vmess xhttp-h3 TLS: %w", certErr)
		}
		packet, listenErr := net.ListenPacket("udp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
		if listenErr != nil {
			a.cancel()
			return listenErr
		}
		a.mu.Lock()
		a.packet = packet
		a.xhttpConfig = xhttpConfig
		if isXHTTPPacketMode(xhttpConfig.Mode) {
			a.xhttpBroker, parseErr = NewXHTTPPacketBroker(xhttpConfig.MaxBufferedPosts, 5*time.Minute)
			if parseErr != nil {
				a.mu.Unlock()
				_ = packet.Close()
				a.cancel()
				return parseErr
			}
		}
		a.mu.Unlock()
		handler := XHTTPServer{Config: xhttpConfig, Handler: func(ctx context.Context, session XHTTPSession) error {
			if isXHTTPPacketMode(a.xhttpConfig.Mode) {
				return a.xhttpPacketHandler(ctx, session)
			}
			conn := newXHTTPDuplexConn(ctx, session.Body, session.Writer)
			defer conn.Close()
			return a.handleConn(ctx, conn)
		}}
		h3Server, serveErr := handler.ServeH3(packet, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
		if serveErr != nil {
			_ = packet.Close()
			a.cancel()
			return serveErr
		}
		a.mu.Lock()
		a.h3Server = h3Server
		a.mu.Unlock()
		return nil
	}
	if strings.EqualFold(network, "xhttp") {
		xhttpConfig, parseErr := ParseXHTTPConfig(spec.Config.Raw)
		if parseErr != nil {
			_ = ln.Close()
			a.cancel()
			return parseErr
		}
		if a.tlsConfig != nil {
			ln = tls.NewListener(ln, a.tlsConfig.Clone())
			a.mu.Lock()
			a.listener = ln
			a.mu.Unlock()
		}
		a.mu.Lock()
		a.xhttpConfig = xhttpConfig
		if isXHTTPPacketMode(xhttpConfig.Mode) {
			a.xhttpBroker, parseErr = NewXHTTPPacketBroker(xhttpConfig.MaxBufferedPosts, 5*time.Minute)
			if parseErr != nil {
				a.mu.Unlock()
				_ = ln.Close()
				a.cancel()
				return parseErr
			}
		}
		a.mu.Unlock()
		handler := XHTTPServer{Config: xhttpConfig, Handler: func(ctx context.Context, session XHTTPSession) error {
			if isXHTTPPacketMode(a.xhttpConfig.Mode) {
				return a.xhttpPacketHandler(ctx, session)
			}
			conn := newXHTTPDuplexConn(ctx, session.Body, session.Writer)
			defer conn.Close()
			return a.handleConn(ctx, conn)
		}}
		server, serveErr := handler.Serve(ln)
		if serveErr != nil {
			_ = ln.Close()
			a.cancel()
			return serveErr
		}
		a.mu.Lock()
		a.httpServer = server
		a.mu.Unlock()
		return nil
	}
	if strings.EqualFold(network, "ws") || strings.EqualFold(network, "httpupgrade") {
		if a.tlsConfig != nil {
			ln = tls.NewListener(ln, a.tlsConfig.Clone())
			a.mu.Lock()
			a.listener = ln
			a.mu.Unlock()
		}
		path, host, pathErr := parseWebSocketTransport(spec.Config.Raw)
		if pathErr != nil {
			_ = ln.Close()
			a.cancel()
			return pathErr
		}
		var server *http.Server
		var serveErr error
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
			goGuarded(conn, func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConn(connCtx, conn)
			})
		}
		if strings.EqualFold(network, "ws") {
			server, serveErr = serveNativeWebSocket(ln, path, host, func() context.Context { return a.ctx }, onConn)
		} else {
			server, serveErr = serveNativeHTTPUpgrade(ln, path, host, func() context.Context { return a.ctx }, onConn)
		}
		if serveErr != nil {
			_ = ln.Close()
			a.cancel()
			return serveErr
		}
		a.mu.Lock()
		a.httpServer = server
		a.mu.Unlock()
		return nil
	}
	if strings.EqualFold(network, "grpc") {
		path, host, pathErr := parseGRPCPath(spec.Config.Raw)
		if pathErr != nil {
			_ = ln.Close()
			a.cancel()
			return pathErr
		}
		h2cMode := a.tlsConfig == nil
		if a.tlsConfig != nil {
			grpcTLS := a.tlsConfig.Clone()
			grpcTLS.NextProtos = []string{"h2"}
			ln = tls.NewListener(ln, grpcTLS)
			a.mu.Lock()
			a.listener = ln
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
			if err := a.handleConn(connCtx, conn); err != nil {
				a.mu.Lock()
				if a.lastErr == nil {
					a.lastErr = err
				}
				a.mu.Unlock()
			}
		}
		server, serveErr := serveNativeGRPC(ln, path, host, 16<<20, h2cMode, onConn)
		if serveErr != nil {
			_ = ln.Close()
			a.cancel()
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

func (a *vmessAdapter) acceptLoop() {
	defer a.wg.Done()
	runAcceptLoop(a.ctx.Done(), a.listener.Accept, a.serveAccepted)
}

// serveAccepted 只登记连接、起 goroutine，立刻返回；TLS 握手在连接自己的
// goroutine 里做。以前握手在 acceptLoop 里同步跑、读超时 10 秒，一条只建
// TCP 不发 ClientHello 的连接就能让整个入站 10 秒接不进新连接。
//
// 登记的是原始连接：握手中的连接也在 active 里，Close 关得到它，wg 也等得到它。
func (a *vmessAdapter) serveAccepted(conn net.Conn) {
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
		if err := a.handleConn(ctx, session); err != nil {
			a.mu.Lock()
			if a.lastErr == nil {
				a.lastErr = err
			}
			a.mu.Unlock()
		}
	})
}

// handleConn 是 TCP / mKCP / WS / HTTP Upgrade / gRPC / XHTTP 共同的会话入口，
// 会话层失败在这里统一上报一次。lastErr 只留第一条给单测断言，不是观测出口。
func (a *vmessAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	err := a.serveConn(ctx, conn)
	a.connErr.conn(StageSession, conn, err)
	return err
}

func (a *vmessAdapter) serveConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	epoch := a.sessions.epoch()
	_ = conn.SetReadDeadline(time.Now().Add(requestHeaderTimeout(a.headerTimeout)))
	reader := bufio.NewReaderSize(conn, ssHeaderReadBuffer)
	user, destination, body, security, err := a.readRequest(reader)
	if err != nil {
		// authID 对不上、头部解不开：读到超时再关，不在读完 16 字节后立刻断
		// （读错误本身立即返回，读空不会多等）。
		drainUntilDeadline(conn)
		return fmt.Errorf("vmess request: %w", err)
	}
	if security != vmessSecNone && security != vmessSecZero && security != vmessSecAES128 && security != vmessSecChaCha {
		return fmt.Errorf("vmess security %d is not enabled in native slice", security)
	}
	bodyState, ok := body.(*vmessBodyReader)
	if !ok {
		return fmt.Errorf("vmess internal body state missing")
	}
	// 重放检查放在清读截止时间之前：重放的请求头与认证失败一样读到超时再关。
	if !a.acceptAuthID(bodyState.authID) {
		drainUntilDeadline(conn)
		return markConnError(connErrAuth, fmt.Errorf("vmess replayed request"))
	}
	_ = conn.SetReadDeadline(time.Time{})
	sess := a.sessions.open(user, epoch, conn)
	if sess == nil {
		return errSessionRevoked
	}
	defer sess.close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return deviceLimitError("vmess")
	}
	defer a.leaveDevice(user, ip)
	if bodyState.command == vmessUDP {
		return a.handleUDP(ctx, conn, user, destination, bodyState, security)
	}
	if bodyState.command == vmessMux {
		return a.handleMux(ctx, conn, user, bodyState, security)
	}
	sourceIP, _ := netip.ParseAddr(ip)
	var sourcePort uint16
	if _, p, e := net.SplitHostPort(conn.RemoteAddr().String()); e == nil {
		if n, e := strconv.ParseUint(p, 10, 16); e == nil {
			sourcePort = uint16(n)
		}
	}
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "vmess", SourceIP: sourceIP, SourcePort: sourcePort}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	if err := vmessWriteResponse(conn, bodyState.key, bodyState.nonce, 0, bodyState.option); err != nil {
		return err
	}
	responseWriter := io.Writer(conn)
	if security == vmessSecAES128 || security == vmessSecChaCha {
		responseKeyHash := sha256.Sum256(bodyState.key)
		responseNonceHash := sha256.Sum256(bodyState.nonce)
		responseWriter = newVMessAEADWriter(conn, vmessBodyAEAD(security, responseKeyHash[:16]), responseNonceHash[:16], bodyState.option)
	}
	// VMess 的读写各自分块加解密，读端是 body、写端是响应流，拼成一端交给转发；
	// 响应流没有半关闭，上游结束后按单向收尾计时收尾。
	client := &core.SplitStream{R: body, W: responseWriter, C: conn}
	sess.relay(client, upstream, core.RelayOptions{Limiter: a.limiters.For(user)})
	return nil
}

func (a *vmessAdapter) handleUDP(ctx context.Context, conn net.Conn, user core.User, destination vmessDestination, body *vmessBodyReader, security byte) error {
	ip := remoteIP(conn.RemoteAddr())
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "vmess", SourceIP: sourceIP}
	upstream, err := a.plane.ListenUDP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	if err := vmessWriteResponse(conn, body.key, body.nonce, 0, body.option); err != nil {
		return err
	}
	responseKeyHash := sha256.Sum256(body.key)
	responseNonceHash := sha256.Sum256(body.nonce)
	var writer io.Writer
	if security == vmessSecAES128 || security == vmessSecChaCha {
		writer = newVMessAEADWriter(conn, vmessBodyAEAD(security, responseKeyHash[:16]), responseNonceHash[:16], body.option)
	} else {
		writer = &vmessPlainChunkWriter{upstream: conn}
	}
	destinationAddr, err := vmessDestinationUDPAddr(destination)
	if err != nil {
		return err
	}
	// 上下行各占一个 goroutine（udp_relay.go）：下行不再等上行来一个包才回一个包，
	// 也不再因 2 秒内没回包就断开会话。客户端 EOF 视为正常结束。
	uplink := func(context.Context) error {
		packet := make([]byte, 64<<10)
		for {
			n, readErr := body.Read(packet)
			if readErr != nil {
				if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
					return nil
				}
				return fmt.Errorf("vmess udp body: %w", readErr)
			}
			if n == 0 {
				continue
			}
			if _, err := upstream.WriteTo(packet[:n], destinationAddr); err != nil {
				return fmt.Errorf("vmess udp upstream write: %w", err)
			}
			a.addTraffic(user, int64(n), 0)
		}
	}
	downlink := func(context.Context) error {
		response := make([]byte, 64<<10)
		for {
			rn, _, err := upstream.ReadFrom(response)
			if err != nil {
				return fmt.Errorf("vmess udp upstream read: %w", err)
			}
			if _, err := writer.Write(response[:rn]); err != nil {
				return fmt.Errorf("vmess udp response write: %w", err)
			}
			a.addTraffic(user, 0, int64(rn))
		}
	}
	return relayUDPDirections(ctx, nil, func() { _ = conn.Close(); _ = upstream.Close() }, uplink, downlink)
}

func (a *vmessAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	ln := a.listener
	packet := a.packet
	httpServer := a.httpServer
	h3Server := a.h3Server
	active := make([]net.Conn, 0, len(a.active))
	for c := range a.active {
		active = append(active, c)
	}
	packetSessions := make([]*vmessXHTTPPacketSession, 0, len(a.xhttpSessions))
	for _, session := range a.xhttpSessions {
		packetSessions = append(packetSessions, session)
	}
	a.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
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
	for _, c := range active {
		_ = c.Close()
	}
	for _, session := range packetSessions {
		session.cancel()
		_ = session.duplex.Uplink.Close(net.ErrClosed)
		_ = session.duplex.Downlink.Close(net.ErrClosed)
	}
	a.wg.Wait()
	return nil
}
func (a *vmessAdapter) removeActive(c net.Conn) { a.mu.Lock(); delete(a.active, c); a.mu.Unlock() }

// VMess authID 防重放：authID 里的时间戳允许 ±120 秒，同一个 authID 最晚在
// 首次出现 240 秒后仍能通过时间检查，所以至少要记 4 分钟。按 2 分钟分代、
// 保留 3 代：至少 4 分钟、至多 6 分钟。以前是每个连接在全局锁下扫整张表、
// 只记 2 分钟。
const (
	vmessAuthIDReplayPeriod = 2 * time.Minute
	vmessAuthIDReplayKeep   = 3
)

func (a *vmessAdapter) acceptAuthID(id [16]byte) bool {
	a.replayOnce.Do(func() {
		if a.replay == nil {
			a.replay = newReplayFilter(vmessAuthIDReplayPeriod, vmessAuthIDReplayKeep, replayFilterMaxPerGen)
		}
	})
	return a.replay.check(id[:], time.Now())
}
