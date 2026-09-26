// [INPUT]: 依赖 adapter.go 的 Adapter 契约与 DataPlane，依赖 grpc_stream.go / xhttp_server.go 的承载与 vmess_xhttp_packet.go，依赖 core 的用户与 route 的路由
// [OUTPUT]: 对外提供 vmessAdapter（经 newVMessAdapter 注册）的 Protocol、Validate、Start、Close；包内 acceptLoop、handleConn、handleUDP、acceptAuthID
// [POS]: kernel 的 VMess 入站主体：原生 gRPC（h2c、TLS+h2）、XHTTP stream 与 packet-up/reconnect 的监听与分派，AuthID 防重放，按命令转 TCP / UDP / mux；请求头解析在 vmess_request.go，正文编解码在 vmess_codec.go，mux 在 vmess_mux.go，用户表在 vmess_users.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"context"
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
	spec          InboundSpec
	mu            sync.RWMutex
	users         map[string]vmessUser
	traffic       map[int64]core.UserTraffic
	online        map[int64]map[string]struct{}
	listener      net.Listener
	packet        net.PacketConn
	httpServer    *http.Server
	h3Server      interface{ Close() error }
	xhttpConfig   XHTTPConfig
	tlsConfig     *tls.Config
	plane         DataPlane
	limiters      core.SpeedLimiters
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	lastErr       error
	active        map[net.Conn]struct{}
	replay        map[string]time.Time
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
}

func newVMessAdapter(spec InboundSpec) (Adapter, error) {
	return &vmessAdapter{spec: spec, users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}), xhttpSessions: make(map[string]*vmessXHTTPPacketSession)}, nil
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
	a.spec, a.plane = spec, hooks.DataPlane
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
			go func() {
				defer a.wg.Done()
				defer a.removeActive(conn)
				_ = a.handleConn(connCtx, conn)
			}()
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
		a.mu.RLock()
		tlsConfig := a.tlsConfig
		ctx := a.ctx
		a.mu.RUnlock()
		if tlsConfig != nil {
			tlsConn := tls.Server(conn, tlsConfig.Clone())
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				continue
			}
			_ = conn.SetReadDeadline(time.Time{})
			conn = tlsConn
		}
		a.mu.Lock()
		a.active[conn] = struct{}{}
		a.mu.Unlock()
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			if err := a.handleConn(a.ctx, conn); err != nil {
				a.mu.Lock()
				if a.lastErr == nil {
					a.lastErr = err
				}
				a.mu.Unlock()
			}
		}()
	}
}

func (a *vmessAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReaderSize(conn, 64*1024)
	user, destination, body, security, err := a.readRequest(reader)
	if err != nil {
		return fmt.Errorf("vmess request: %w", err)
	}
	if security != vmessSecNone && security != vmessSecZero && security != vmessSecAES128 && security != vmessSecChaCha {
		return fmt.Errorf("vmess security %d is not enabled in native slice", security)
	}
	_ = conn.SetReadDeadline(time.Time{})
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("vmess device limit")
	}
	defer a.leaveDevice(user, ip)
	bodyState, ok := body.(*vmessBodyReader)
	if !ok {
		return fmt.Errorf("vmess internal body state missing")
	}
	if !a.acceptAuthID(bodyState.authID) {
		return fmt.Errorf("vmess replayed request")
	}
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
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		n, _ := core.SpeedLimitedCopy(upstream, body, a.limiters.For(user))
		a.addTraffic(user, n, 0)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		wg.Done()
	}()
	responseWriter := io.Writer(conn)
	if security == vmessSecAES128 || security == vmessSecChaCha {
		responseKeyHash := sha256.Sum256(bodyState.key)
		responseNonceHash := sha256.Sum256(bodyState.nonce)
		responseWriter = newVMessAEADWriter(conn, vmessBodyAEAD(security, responseKeyHash[:16]), responseNonceHash[:16], bodyState.option)
	}
	go func() {
		n, _ := core.SpeedLimitedCopy(responseWriter, upstream, a.limiters.For(user))
		a.addTraffic(user, 0, n)
		wg.Done()
	}()
	wg.Wait()
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
	packet := make([]byte, 64<<10)
	response := make([]byte, 64<<10)
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
		_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
		rn, _, err := upstream.ReadFrom(response)
		if err != nil {
			return fmt.Errorf("vmess udp upstream read: %w", err)
		}
		if _, err := writer.Write(response[:rn]); err != nil {
			return fmt.Errorf("vmess udp response write: %w", err)
		}
		a.addTraffic(user, int64(n), int64(rn))
	}
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

func (a *vmessAdapter) acceptAuthID(id [16]byte) bool {
	now := time.Now()
	key := string(id[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.replay == nil {
		a.replay = make(map[string]time.Time)
	}
	for k, seen := range a.replay {
		if now.Sub(seen) > 2*time.Minute {
			delete(a.replay, k)
		}
	}
	if _, exists := a.replay[key]; exists {
		return false
	}
	a.replay[key] = now
	return true
}
