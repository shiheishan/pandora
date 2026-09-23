package kernel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/fnv"
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
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/sha3"
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

const (
	vmessMuxStatusNew       byte = 1
	vmessMuxStatusKeep      byte = 2
	vmessMuxStatusEnd       byte = 3
	vmessMuxStatusKeepAlive byte = 4
	vmessMuxOptionData      byte = 1
	vmessMuxOptionError     byte = 2
	vmessMuxNetworkTCP      byte = 1
	vmessMuxNetworkUDP      byte = 2
)

type vmessMuxSession struct {
	adapter *vmessAdapter
	ctx     context.Context
	user    core.User
	writer  io.Writer
	mu      sync.Mutex
	streams map[uint16]*vmessMuxStream
	wg      sync.WaitGroup
}

type vmessMuxStream struct {
	sessionID uint16
	network   byte
	dest      M.Socksaddr
	pipeW     *io.PipeWriter
	pipeR     *io.PipeReader
	closed    chan struct{}
}

func (a *vmessAdapter) handleMux(ctx context.Context, conn net.Conn, user core.User, body *vmessBodyReader, security byte) error {
	if err := vmessWriteResponse(conn, body.key, body.nonce, 0, body.option); err != nil {
		return err
	}
	var writer io.Writer = conn
	if security == vmessSecAES128 || security == vmessSecChaCha {
		keyHash := sha256.Sum256(body.key)
		nonceHash := sha256.Sum256(body.nonce)
		writer = newVMessAEADWriter(conn, vmessBodyAEAD(security, keyHash[:16]), nonceHash[:16], body.option)
	}
	session := &vmessMuxSession{adapter: a, ctx: ctx, user: user, writer: writer, streams: make(map[uint16]*vmessMuxStream)}
	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		streamID, status, option, network, destination, err := readVMessMuxHeader(reader)
		if err != nil {
			session.closeAll()
			session.wg.Wait()
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("vmess mux header: %w", err)
		}
		var stream *vmessMuxStream
		session.mu.Lock()
		stream = session.streams[streamID]
		session.mu.Unlock()
		switch status {
		case vmessMuxStatusNew:
			if stream != nil {
				return fmt.Errorf("vmess mux duplicate stream %d", streamID)
			}
			if network != vmessMuxNetworkTCP && network != vmessMuxNetworkUDP {
				return fmt.Errorf("vmess mux network %d unsupported", network)
			}
			stream = session.newStream(streamID, network, destination)
		case vmessMuxStatusKeep:
			if stream == nil {
				_ = session.writeClose(streamID, true)
			}
		case vmessMuxStatusEnd:
			if stream != nil {
				session.removeStream(streamID, option&vmessMuxOptionError != 0)
			}
		case vmessMuxStatusKeepAlive:
		default:
			return fmt.Errorf("vmess mux status %d unsupported", status)
		}
		if option&vmessMuxOptionData == 0 {
			continue
		}
		data, dataDestination, err := readVMessMuxData(reader, destination)
		if err != nil {
			session.closeAll()
			session.wg.Wait()
			return fmt.Errorf("vmess mux data: %w", err)
		}
		if stream == nil {
			continue
		}
		if dataDestination.IsValid() {
			stream.dest = dataDestination
		}
		if stream.network == vmessMuxNetworkTCP {
			if _, err := stream.pipeW.Write(data); err != nil {
				session.removeStream(streamID, true)
			}
		} else {
			session.wg.Add(1)
			go func(streamID uint16, stream *vmessMuxStream, payload []byte) {
				defer session.wg.Done()
				if err := session.forwardMuxUDP(stream, payload); err != nil {
					_ = session.writeClose(streamID, true)
					session.removeStream(streamID, true)
				}
			}(streamID, stream, data)
		}
	}
}

func (s *vmessMuxSession) newStream(id uint16, network byte, destination M.Socksaddr) *vmessMuxStream {
	reader, writer := io.Pipe()
	stream := &vmessMuxStream{sessionID: id, network: network, dest: destination, pipeW: writer, pipeR: reader, closed: make(chan struct{})}
	s.mu.Lock()
	s.streams[id] = stream
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if network == vmessMuxNetworkTCP {
			s.forwardMuxTCP(stream)
		}
	}()
	return stream
}

func (s *vmessMuxSession) forwardMuxTCP(stream *vmessMuxStream) {
	if !stream.dest.IsValid() {
		_ = s.writeClose(stream.sessionID, true)
		s.removeStream(stream.sessionID, true)
		return
	}
	meta := route.Meta{Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port, Network: "tcp", Protocol: "vmess-mux"}
	upstream, err := s.adapter.plane.DialTCP(s.ctx, meta, stream.dest)
	if err != nil {
		_ = s.writeClose(stream.sessionID, true)
		s.removeStream(stream.sessionID, true)
		return
	}
	defer upstream.Close()
	conn := &vmessMuxTCPConn{stream: stream, session: s}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		_, _ = core.SpeedLimitedCopy(upstream, stream.pipeR, s.adapter.limiters.For(s.user))
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		wg.Done()
	}()
	go func() { _, _ = core.SpeedLimitedCopy(conn, upstream, s.adapter.limiters.For(s.user)); wg.Done() }()
	wg.Wait()
	_ = s.writeClose(stream.sessionID, false)
	s.removeStream(stream.sessionID, false)
}

type vmessMuxTCPConn struct {
	stream  *vmessMuxStream
	session *vmessMuxSession
}

func (c *vmessMuxTCPConn) Write(p []byte) (int, error) {
	return c.session.writeData(c.stream.sessionID, p)
}

func (c *vmessMuxTCPConn) Close() error {
	return c.session.writeClose(c.stream.sessionID, false)
}

func (s *vmessMuxSession) forwardMuxUDP(stream *vmessMuxStream, payload []byte) error {
	if !stream.dest.IsValid() {
		return fmt.Errorf("vmess mux UDP destination missing")
	}
	meta := route.Meta{Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port, Network: "udp", Protocol: "vmess-mux"}
	upstream, err := s.adapter.plane.ListenUDP(s.ctx, meta, stream.dest)
	if err != nil {
		return err
	}
	defer upstream.Close()
	destinationAddr, err := vmessDestinationUDPAddr(vmessDestination{Host: stream.dest.AddrString(), Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port})
	if err != nil {
		return err
	}
	if _, err := upstream.WriteTo(payload, destinationAddr); err != nil {
		return err
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	response := make([]byte, 64<<10)
	n, sourceAddr, err := upstream.ReadFrom(response)
	if err != nil {
		return err
	}
	responseDestination := stream.dest
	if sourceAddr != nil {
		if host, portText, splitErr := net.SplitHostPort(sourceAddr.String()); splitErr == nil {
			if portValue, parseErr := strconv.ParseUint(portText, 10, 16); parseErr == nil {
				responseDestination = M.ParseSocksaddrHostPort(host, uint16(portValue))
			}
		}
	}
	return s.writePacketData(stream.sessionID, response[:n], responseDestination)
}

func (s *vmessMuxSession) writeData(id uint16, data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) > 65535 {
		data = data[:65535]
	}
	var header [8]byte
	binary.BigEndian.PutUint16(header[0:2], 4)
	binary.BigEndian.PutUint16(header[2:4], id)
	header[4], header[5] = vmessMuxStatusKeep, vmessMuxOptionData
	binary.BigEndian.PutUint16(header[6:8], uint16(len(data)))
	if _, err := s.writer.Write(header[:]); err != nil {
		return 0, err
	}
	n, err := s.writer.Write(data)
	return n, err
}

func (s *vmessMuxSession) writePacketData(id uint16, data []byte, destination M.Socksaddr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var meta bytes.Buffer
	meta.WriteByte(vmessMuxNetworkUDP)
	if err := vmessAddressSerializer.WriteAddrPort(&meta, destination); err != nil {
		return err
	}
	var header [6]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(meta.Len()+4))
	binary.BigEndian.PutUint16(header[2:4], id)
	header[4], header[5] = vmessMuxStatusKeep, vmessMuxOptionData
	if _, err := s.writer.Write(header[:]); err != nil {
		return err
	}
	if _, err := s.writer.Write(meta.Bytes()); err != nil {
		return err
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(data)))
	if _, err := s.writer.Write(length[:]); err != nil {
		return err
	}
	_, err := s.writer.Write(data)
	return err
}

func (s *vmessMuxSession) writeClose(id uint16, hasError bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var frame [6]byte
	binary.BigEndian.PutUint16(frame[0:2], 4)
	binary.BigEndian.PutUint16(frame[2:4], id)
	frame[4] = vmessMuxStatusEnd
	if hasError {
		frame[5] = vmessMuxOptionError
	}
	_, err := s.writer.Write(frame[:])
	return err
}

func (s *vmessMuxSession) removeStream(id uint16, withError bool) {
	s.mu.Lock()
	stream := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if stream != nil {
		close(stream.closed)
		_ = stream.pipeW.CloseWithError(func() error {
			if withError {
				return io.ErrUnexpectedEOF
			}
			return nil
		}())
	}
}

func (s *vmessMuxSession) closeAll() {
	s.mu.Lock()
	streams := make([]*vmessMuxStream, 0, len(s.streams))
	for id, stream := range s.streams {
		delete(s.streams, id)
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		close(stream.closed)
		_ = stream.pipeW.Close()
	}
}

func readVMessMuxHeader(reader io.Reader) (uint16, byte, byte, byte, M.Socksaddr, error) {
	var fixed [6]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, err
	}
	length := int(binary.BigEndian.Uint16(fixed[0:2]))
	if length < 4 || length > 4096 {
		return 0, 0, 0, 0, M.Socksaddr{}, fmt.Errorf("mux metadata length %d invalid", length)
	}
	meta := make([]byte, length-4)
	if _, err := io.ReadFull(reader, meta); err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, err
	}
	streamID := binary.BigEndian.Uint16(fixed[2:4])
	status, option := fixed[4], fixed[5]
	if len(meta) == 0 {
		return streamID, status, option, 0, M.Socksaddr{}, nil
	}
	network := meta[0]
	destination, err := vmessAddressSerializer.ReadAddrPort(bytes.NewReader(meta[1:]))
	if err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, fmt.Errorf("mux destination bytes %x: %w", meta, err)
	}
	return streamID, status, option, network, destination, err
}

func readVMessMuxData(reader io.Reader, destination M.Socksaddr) ([]byte, M.Socksaddr, error) {
	var length [2]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return nil, M.Socksaddr{}, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n > 65535 {
		return nil, M.Socksaddr{}, fmt.Errorf("mux data length invalid")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, M.Socksaddr{}, err
	}
	return data, destination, nil
}

func vmessDestinationUDPAddr(destination vmessDestination) (net.Addr, error) {
	if destination.IP.IsValid() {
		return &net.UDPAddr{IP: net.IP(destination.IP.AsSlice()), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Domain, strconv.Itoa(int(destination.Port))))
}

type vmessDestination struct {
	Host, Domain string
	IP           netip.Addr
	Port         uint16
}

type vmessUserCandidate struct {
	user     core.User
	key      [16]byte
	keyBlock cipher.Block
}

func (a *vmessAdapter) readRequest(r *bufio.Reader) (core.User, vmessDestination, io.Reader, byte, error) {
	a.mu.RLock()
	candidates := make([]vmessUserCandidate, 0, len(a.users))
	for id, u := range a.users {
		_, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		block, err := aes.NewCipher(vmessKDF(u.key[:], "AES Auth ID Encryption")[:16])
		if err == nil {
			candidates = append(candidates, vmessUserCandidate{user: core.User{ID: u.ID, UUID: id, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit}, key: u.key, keyBlock: block})
		}
	}
	a.mu.RUnlock()
	return readVMessRequestWithCandidates(r, candidates)
}

func readVMessRequestWithCandidates(r *bufio.Reader, candidates []vmessUserCandidate) (core.User, vmessDestination, io.Reader, byte, error) {
	// This indirection keeps the exported parser deterministic while allowing
	// the adapter to authenticate against a snapshot instead of a global map.
	var out vmessDestination
	var auth [16]byte
	if _, err := io.ReadFull(r, auth[:]); err != nil {
		return core.User{}, out, nil, 0, err
	}
	var user core.User
	var key [16]byte
	found := false
	for _, candidate := range candidates {
		plain := make([]byte, 16)
		candidate.keyBlock.Decrypt(plain, auth[:])
		if binary.BigEndian.Uint32(plain[12:]) != crc32.ChecksumIEEE(plain[:12]) {
			continue
		}
		ts := int64(binary.BigEndian.Uint64(plain[:8]))
		now := time.Now().Unix()
		if ts < now-120 || ts > now+120 {
			continue
		}
		user, key, found = candidate.user, candidate.key, true
		break
	}
	if !found {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess auth id rejected")
	}
	var lenCipher [18]byte
	if _, err := io.ReadFull(r, lenCipher[:]); err != nil {
		return core.User{}, out, nil, 0, err
	}
	var nonce [8]byte
	if _, err := io.ReadFull(r, nonce[:]); err != nil {
		return core.User{}, out, nil, 0, err
	}
	lengthPlain, err := vmessOpen(vmessKDF(key[:], "VMess Header AEAD Key_Length", auth[:], nonce[:])[:16], vmessKDF(key[:], "VMess Header AEAD Nonce_Length", auth[:], nonce[:])[:12], lenCipher[:], auth[:])
	if err != nil || len(lengthPlain) != 2 {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header length authentication failed")
	}
	headerLen := int(binary.BigEndian.Uint16(lengthPlain))
	if headerLen < 42 || headerLen > 4096 {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header length %d invalid", headerLen)
	}
	headerCipher := make([]byte, headerLen+16)
	if _, err := io.ReadFull(r, headerCipher); err != nil {
		return core.User{}, out, nil, 0, err
	}
	header, err := vmessOpen(vmessKDF(key[:], "VMess Header AEAD Key", auth[:], nonce[:])[:16], vmessKDF(key[:], "VMess Header AEAD Nonce", auth[:], nonce[:])[:12], headerCipher, auth[:])
	if err != nil {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header authentication failed")
	}
	if len(header) != headerLen || header[0] != vmessVersion || (header[37] != vmessTCP && header[37] != vmessUDP && header[37] != vmessMux) {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header version or command unsupported")
	}
	if !vmessValidHash(header) {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header checksum invalid")
	}
	security := header[35] & 0x0f
	if security != vmessSecNone && security != vmessSecZero && security != vmessSecAES128 && security != vmessSecChaCha {
		return core.User{}, out, nil, security, fmt.Errorf("vmess security %d unsupported", security)
	}
	if (security == vmessSecAES128 || security == vmessSecChaCha) && header[34]&vmessOptChunk == 0 {
		return core.User{}, out, nil, security, fmt.Errorf("vmess AES-GCM requires chunk framing")
	}
	if (security == vmessSecNone || security == vmessSecZero) && header[37] == vmessTCP && header[34] != 0 {
		return core.User{}, out, nil, security, fmt.Errorf("vmess chunk options require an authenticated body security mode")
	}
	if (security == vmessSecNone || security == vmessSecZero) && header[37] == vmessUDP && header[34] != vmessOptChunk {
		return core.User{}, out, nil, security, fmt.Errorf("vmess UDP requires plain chunk framing")
	}
	pos := 38
	if header[37] == vmessMux {
		out.Domain, out.Host, out.Port = "v1.mux.cool", "v1.mux.cool", 666
	} else {
		if pos+3 > len(header) {
			return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
		}
		out.Port = binary.BigEndian.Uint16(header[pos:])
		pos += 2
		if out.Port == 0 {
			return core.User{}, out, nil, 0, fmt.Errorf("vmess destination port invalid")
		}
		switch header[pos] {
		case 1:
			pos++
			if pos+4 > len(header) {
				return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
			}
			var ip4 [4]byte
			copy(ip4[:], header[pos:pos+4])
			out.IP = netip.AddrFrom4(ip4)
			out.Host = out.IP.String()
		case 2:
			pos++
			if pos >= len(header) || header[pos] == 0 || header[pos] > 253 {
				return core.User{}, out, nil, 0, fmt.Errorf("vmess domain length invalid")
			}
			n := int(header[pos])
			pos++
			if pos+n > len(header) {
				return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
			}
			out.Domain = string(header[pos : pos+n])
			out.Host = out.Domain
		case 3:
			pos++
			if pos+16 > len(header) {
				return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
			}
			var ip6 [16]byte
			copy(ip6[:], header[pos:pos+16])
			out.IP = netip.AddrFrom16(ip6)
			out.Host = out.IP.String()
		default:
			return core.User{}, out, nil, 0, fmt.Errorf("vmess address type unsupported")
		}
	}
	var authID [16]byte
	copy(authID[:], auth[:])
	return user, out, &vmessBodyReader{reader: r, key: append([]byte(nil), header[17:33]...), nonce: append([]byte(nil), header[1:17]...), security: security, option: header[34], command: header[37], authID: authID}, security, nil
}

type vmessBodyReader struct {
	reader           *bufio.Reader
	key, nonce       []byte
	security, option byte
	command          byte
	authID           [16]byte
	aead             *vmessAEADReader
	plainChunks      *vmessPlainChunkReader
}

func (r *vmessBodyReader) Read(p []byte) (int, error) {
	if r.security == vmessSecNone || r.security == vmessSecZero {
		if r.command == vmessUDP {
			if r.plainChunks == nil {
				r.plainChunks = &vmessPlainChunkReader{upstream: r.reader}
			}
			return r.plainChunks.Read(p)
		}
		return r.reader.Read(p)
	}
	if r.aead == nil {
		r.aead = newVMessAEADReader(r.reader, vmessBodyAEAD(r.security, r.key), r.nonce, r.option)
	}
	return r.aead.Read(p)
}

type vmessPlainChunkReader struct {
	upstream *bufio.Reader
	pending  []byte
}

func (r *vmessPlainChunkReader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	var length [2]byte
	if _, err := io.ReadFull(r.upstream, length[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n == 0 || n > 65535 {
		return 0, fmt.Errorf("vmess UDP chunk length %d invalid", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r.upstream, data); err != nil {
		return 0, err
	}
	written := copy(p, data)
	if written < len(data) {
		r.pending = append(r.pending, data[written:]...)
	}
	return written, nil
}

type vmessPlainChunkWriter struct {
	mu       sync.Mutex
	upstream io.Writer
}

func (w *vmessPlainChunkWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 65535 {
		p = p[:65535]
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(p)))
	if _, err := w.upstream.Write(length[:]); err != nil {
		return 0, err
	}
	return w.upstream.Write(p)
}

type vmessAEADReader struct {
	upstream *bufio.Reader
	gcm      cipher.AEAD
	mask     sha3.ShakeHash
	nonce    [12]byte
	count    uint16
	pending  []byte
}

func newVMessAEADReader(upstream *bufio.Reader, aead cipher.AEAD, nonce []byte, option byte) *vmessAEADReader {
	var base [12]byte
	copy(base[:], nonce)
	var mask sha3.ShakeHash
	if option&vmessOptMask != 0 {
		mask = sha3.NewShake128()
		_, _ = mask.Write(nonce)
	}
	return &vmessAEADReader{upstream: upstream, gcm: aead, mask: mask, nonce: base}
}

func (r *vmessAEADReader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	var rawLen [2]byte
	if _, err := io.ReadFull(r.upstream, rawLen[:]); err != nil {
		return 0, err
	}
	length := binary.BigEndian.Uint16(rawLen[:])
	if r.mask != nil {
		var maskCode uint16
		if err := binary.Read(r.mask, binary.BigEndian, &maskCode); err != nil {
			return 0, err
		}
		length ^= maskCode
	}
	if length < 16 || length > 65535 {
		return 0, fmt.Errorf("vmess AES chunk length %d invalid", length)
	}
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(r.upstream, ciphertext); err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint16(r.nonce[:2], r.count)
	r.count++
	plaintext, err := r.gcm.Open(nil, r.nonce[:], ciphertext, nil)
	if err != nil {
		return 0, fmt.Errorf("vmess AES chunk authentication failed: %w", err)
	}
	if len(plaintext) == 0 {
		return 0, io.EOF
	}
	n := copy(p, plaintext)
	if n < len(plaintext) {
		r.pending = append(r.pending, plaintext[n:]...)
	}
	return n, nil
}

type vmessAEADWriter struct {
	mu       sync.Mutex
	upstream io.Writer
	gcm      cipher.AEAD
	mask     sha3.ShakeHash
	nonce    [12]byte
	count    uint16
}

func newVMessAEADWriter(upstream io.Writer, aead cipher.AEAD, nonce []byte, option byte) *vmessAEADWriter {
	var base [12]byte
	copy(base[:], nonce)
	var mask sha3.ShakeHash
	if option&vmessOptMask != 0 {
		mask = sha3.NewShake128()
		_, _ = mask.Write(nonce)
	}
	return &vmessAEADWriter{upstream: upstream, gcm: aead, mask: mask, nonce: base}
}

func (w *vmessAEADWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 15000 {
			chunk = chunk[:15000]
		}
		binary.BigEndian.PutUint16(w.nonce[:2], w.count)
		w.count++
		ciphertext := w.gcm.Seal(nil, w.nonce[:], chunk, nil)
		length := uint16(len(ciphertext))
		if w.mask != nil {
			var maskCode uint16
			if err := binary.Read(w.mask, binary.BigEndian, &maskCode); err != nil {
				return written, err
			}
			length ^= maskCode
		}
		var header [2]byte
		binary.BigEndian.PutUint16(header[:], length)
		if _, err := w.upstream.Write(header[:]); err != nil {
			return written, err
		}
		if _, err := w.upstream.Write(ciphertext); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func vmessBodyAEAD(security byte, key []byte) cipher.AEAD {
	if security == vmessSecChaCha {
		derived := make([]byte, 32)
		first := md5.Sum(key)
		second := md5.Sum(first[:])
		copy(derived, first[:])
		copy(derived[16:], second[:])
		aead, _ := chacha20poly1305.New(derived)
		return aead
	}
	aead, _ := aesGCM(key)
	return aead
}

func (a *vmessAdapter) AddUsers(users []core.User) error {
	validated := make([]vmessUserEntry, 0, len(users))
	for _, u := range users {
		parsed, err := uuid.Parse(u.UUID)
		if err != nil {
			return fmt.Errorf("vmess user %q uuid invalid", u.UUID)
		}
		validated = append(validated, vmessUserEntry{uuid: parsed.String(), user: u, key: vmessCommandKey(parsed)})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vmess adapter closed")
	}
	for _, entry := range validated {
		if _, ok := a.users[entry.uuid]; !ok {
			a.users[entry.uuid] = vmessUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, key: entry.key}
		}
	}
	return nil
}

type vmessUserEntry struct {
	uuid string
	user core.User
	key  [16]byte
}

func (a *vmessAdapter) UpsertUsers(users []core.User) error {
	validated := make([]vmessUserEntry, 0, len(users))
	for _, u := range users {
		parsed, err := uuid.Parse(u.UUID)
		if err != nil {
			return fmt.Errorf("vmess user %q uuid invalid", u.UUID)
		}
		u.UUID = parsed.String()
		validated = append(validated, vmessUserEntry{uuid: u.UUID, user: u, key: vmessCommandKey(parsed)})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vmess adapter closed")
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.uuid]; exists {
			a.limiters.Remove(previous.ID)
		}
		a.users[entry.uuid] = vmessUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, key: entry.key}
	}
	return nil
}

func (a *vmessAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			key := parsed.String()
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			if entry, ok := a.users[key]; ok {
				a.limiters.Remove(entry.ID)
			}
			delete(a.users, key)
		}
	}
	return nil
}
func (a *vmessAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, t := range a.traffic {
		if t.Upload != 0 || t.Download != 0 {
			out = append(out, t)
		}
		delete(a.traffic, id)
	}
	return out, nil
}
func (a *vmessAdapter) OnlineIPs() map[int64][]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[int64][]string, len(a.online))
	for id, set := range a.online {
		for ip := range set {
			out[id] = append(out[id], ip)
		}
	}
	return out
}
func (a *vmessAdapter) enterDevice(u core.User, ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.online[u.ID]
	if set == nil {
		set = map[string]struct{}{}
		a.online[u.ID] = set
	}
	if _, ok := set[ip]; !ok && u.DeviceLimit > 0 && len(set) >= u.DeviceLimit {
		return false
	}
	set[ip] = struct{}{}
	return true
}
func (a *vmessAdapter) leaveDevice(u core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[u.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, u.ID)
		}
	}
}
func (a *vmessAdapter) addTraffic(u core.User, up, down int64) {
	a.mu.Lock()
	t := a.traffic[u.ID]
	t.ID = u.ID
	t.Upload += up
	t.Download += down
	a.traffic[u.ID] = t
	a.mu.Unlock()
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

func vmessCommandKey(id uuid.UUID) [16]byte {
	h := md5.New()
	_, _ = h.Write(id[:])
	_, _ = h.Write([]byte("c48619fe-8f02-49e0-b9e9-edf763e17e21"))
	var out [16]byte
	h.Sum(out[:0])
	return out
}
func vmessKDF(key []byte, salt string, path ...[]byte) []byte {
	factory := func() hash.Hash { return hmac.New(sha256.New, vmessKDFRoot) }
	values := make([][]byte, 0, len(path)+1)
	values = append(values, []byte(salt))
	values = append(values, path...)
	for _, value := range values {
		parent := factory
		copied := append([]byte(nil), value...)
		factory = func() hash.Hash { return hmac.New(parent, copied) }
	}
	h := factory()
	_, _ = h.Write(key)
	return h.Sum(nil)
}

func vmessOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, nonce, ciphertext, aad)
}
func vmessValidHash(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write(header[:len(header)-4])
	return binary.BigEndian.Uint32(header[len(header)-4:]) == h.Sum32()
}
func vmessWriteResponse(w io.Writer, requestKey, requestNonce []byte, response, option byte) error {
	keyHash := sha256.Sum256(requestKey)
	nonceHash := sha256.Sum256(requestNonce)
	responseKey, responseNonce := keyHash[:16], nonceHash[:16]
	lengthKey := vmessKDF(responseKey, "AEAD Resp Header Len Key")[:16]
	lengthNonce := vmessKDF(responseNonce, "AEAD Resp Header Len IV")[:12]
	lengthCipher, err := aesGCM(lengthKey)
	if err != nil {
		return err
	}
	lengthPlain := []byte{0, 4}
	lengthCiphertext := lengthCipher.Seal(nil, lengthNonce, lengthPlain, nil)
	payloadKey := vmessKDF(responseKey, "AEAD Resp Header Key")[:16]
	payloadNonce := vmessKDF(responseNonce, "AEAD Resp Header IV")[:12]
	payloadCipher, err := aesGCM(payloadKey)
	if err != nil {
		return err
	}
	payload := payloadCipher.Seal(nil, payloadNonce, []byte{response, option, 0, 0}, nil)
	_, err = w.Write(append(lengthCiphertext, payload...))
	return err
}

func aesGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
