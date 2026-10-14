package hysteria2

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/internal/nativewire/hysteria2/internal/protocol"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/congestion"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/quic-go/quicvarint"
	qtls "github.com/sagernet/sing-quic"
	congestion_meta1 "github.com/sagernet/sing-quic/congestion_meta1"
	congestion_meta2 "github.com/sagernet/sing-quic/congestion_meta2"
	"github.com/sagernet/sing-quic/hysteria"
	hyCC "github.com/sagernet/sing-quic/hysteria/congestion"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	aTLS "github.com/sagernet/sing/common/tls"
)

type ServiceOptions struct {
	Context               context.Context
	Logger                logger.Logger
	BrutalDebug           bool
	SendBPS               uint64
	ReceiveBPS            uint64
	IgnoreClientBandwidth bool
	SalamanderPassword    string
	TLSConfig             aTLS.ServerConfig
	UDPDisabled           bool
	UDPTimeout            time.Duration
	// UDPQueueSize 是每个 UDP 会话的接收队列长度，非正值用 DefaultUDPQueueSize。
	UDPQueueSize int
	// PreAuthHeaderTimeout 是认证前每条请求流读请求头的时限（Pandora 改动），超时只拒
	// 这条流；非正值用 DefaultPreAuthHeaderTimeout。
	PreAuthHeaderTimeout time.Duration
	// PreAuthIdleTimeout 是未认证连接没有在途请求时的空闲时限（Pandora 改动），到点
	// 关连接；非正值用 DefaultPreAuthIdleTimeout。
	PreAuthIdleTimeout time.Duration
	Handler            ServerHandler
	MasqueradeHandler  http.Handler
}

type ServerHandler interface {
	N.TCPConnectionHandlerEx
	N.UDPConnectionHandlerEx
}

type Service[U comparable] struct {
	ctx                   context.Context
	logger                logger.Logger
	brutalDebug           bool
	sendBPS               uint64
	receiveBPS            uint64
	ignoreClientBandwidth bool
	salamanderPassword    string
	tlsConfig             aTLS.ServerConfig
	quicConfig            *quic.Config
	userAccess            sync.RWMutex
	userMap               map[string]U
	udpDisabled           bool
	udpTimeout            time.Duration
	udpQueueSize          int
	preAuthHeaderTimeout  time.Duration
	preAuthIdleTimeout    time.Duration
	handler               ServerHandler
	masqueradeHandler     http.Handler
	quicListener          io.Closer
}

// ServerMaxIncomingStreams 是每条 QUIC 连接上对端可同时打开的双向流数（Pandora
// 改动；上游 sing-quic 是 1<<60，等于不设上限）。
//
// hy2 是 HTTP/3 服务端，认证之前的请求按伪装站点处理：http3 每接一条双向流就起
// 一个 goroutine 读请求头，hy2 又没有认证超时。不设上限时，只完成握手、不认证的
// 客户端开流不发完请求头，就能让服务端堆起任意多的 goroutine（4000 条流实测多出
// 4008 个），连接不断就一直挂着；QUIC 里开第 N 号流还会隐式打开它之前的全部流，
// 一个大编号的 STREAM 帧就能让 quic-go 建出任意多的流对象。
//
// 1024 与 Hysteria 官方服务端的 maxIncomingStreams 缺省一致。认证之后每条 TCP
// 转发占一条双向流，sing-box 等客户端用不阻塞的 OpenStream，同一条连接上同时在途
// 的 TCP 超过 1024 条时新的会报错；quic-go 没有认证后再放宽上限的接口，见报告。
const ServerMaxIncomingStreams = 1024

func NewService[U comparable](options ServiceOptions) (*Service[U], error) {
	quicConfig := &quic.Config{
		DisablePathMTUDiscovery:        !(runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "android" || runtime.GOOS == "darwin"),
		EnableDatagrams:                !options.UDPDisabled,
		MaxIncomingStreams:             ServerMaxIncomingStreams,
		InitialStreamReceiveWindow:     hysteria.DefaultStreamReceiveWindow,
		MaxStreamReceiveWindow:         hysteria.DefaultStreamReceiveWindow,
		InitialConnectionReceiveWindow: hysteria.DefaultConnReceiveWindow,
		MaxConnectionReceiveWindow:     hysteria.DefaultConnReceiveWindow,
		MaxIdleTimeout:                 hysteria.DefaultMaxIdleTimeout,
		KeepAlivePeriod:                hysteria.DefaultKeepAlivePeriod,
		DisablePathManager:             true,
	}
	if options.PreAuthHeaderTimeout <= 0 {
		options.PreAuthHeaderTimeout = DefaultPreAuthHeaderTimeout
	}
	if options.PreAuthIdleTimeout <= 0 {
		options.PreAuthIdleTimeout = DefaultPreAuthIdleTimeout
	}
	if options.MasqueradeHandler == nil {
		options.MasqueradeHandler = http.NotFoundHandler()
	}
	if len(options.TLSConfig.NextProtos()) == 0 {
		options.TLSConfig.SetNextProtos([]string{http3.NextProtoH3})
	}
	return &Service[U]{
		ctx:                   options.Context,
		logger:                options.Logger,
		brutalDebug:           options.BrutalDebug,
		sendBPS:               options.SendBPS,
		receiveBPS:            options.ReceiveBPS,
		ignoreClientBandwidth: options.IgnoreClientBandwidth,
		salamanderPassword:    options.SalamanderPassword,
		tlsConfig:             options.TLSConfig,
		quicConfig:            quicConfig,
		userMap:               make(map[string]U),
		udpDisabled:           options.UDPDisabled,
		udpTimeout:            options.UDPTimeout,
		udpQueueSize:          options.UDPQueueSize,
		preAuthHeaderTimeout:  options.PreAuthHeaderTimeout,
		preAuthIdleTimeout:    options.PreAuthIdleTimeout,
		handler:               options.Handler,
		masqueradeHandler:     options.MasqueradeHandler,
	}, nil
}

func (s *Service[U]) UpdateUsers(userList []U, passwordList []string) {
	userMap := make(map[string]U)
	for i, user := range userList {
		userMap[passwordList[i]] = user
	}
	s.userAccess.Lock()
	s.userMap = userMap
	s.userAccess.Unlock()
}

// PatchUsers 增量更新口令表（Pandora 改动）：先删 remove 里的口令，再写入
// passwords[i] → users[i]。用户数上千时，一次增减几十人不必重建整张表。
func (s *Service[U]) PatchUsers(remove []string, users []U, passwords []string) {
	s.userAccess.Lock()
	defer s.userAccess.Unlock()
	for _, password := range remove {
		delete(s.userMap, password)
	}
	for i, user := range users {
		s.userMap[passwords[i]] = user
	}
}

// LookupUser 按口令查用户，供测试与诊断核对口令表。
func (s *Service[U]) LookupUser(password string) (U, bool) {
	s.userAccess.RLock()
	defer s.userAccess.RUnlock()
	user, ok := s.userMap[password]
	return user, ok
}

// UserCount 返回口令表条目数。
func (s *Service[U]) UserCount() int {
	s.userAccess.RLock()
	defer s.userAccess.RUnlock()
	return len(s.userMap)
}

func (s *Service[U]) Start(conn net.PacketConn) error {
	if s.salamanderPassword != "" {
		conn = newServerSalamanderConn(conn, []byte(s.salamanderPassword))
	}
	err := qtls.ConfigureHTTP3(s.tlsConfig)
	if err != nil {
		return err
	}
	listener, err := qtls.Listen(conn, s.tlsConfig, s.quicConfig)
	if err != nil {
		return err
	}
	s.quicListener = listener
	go s.loopConnections(listener)
	return nil
}

func (s *Service[U]) Close() error {
	return common.Close(
		s.quicListener,
	)
}

func (s *Service[U]) loopConnections(listener qtls.Listener) {
	for {
		connection, err := listener.Accept(s.ctx)
		if err != nil {
			if E.IsClosedOrCanceled(err) || errors.Is(err, quic.ErrServerClosed) {
				s.logger.Debug(E.Cause(err, "listener closed"))
			} else {
				s.logger.Error(E.Cause(err, "listener closed"))
			}
			return
		}
		go s.handleConnection(connection)
	}
}

// 认证前的约束（Pandora 改动，review-r3 #4、round-r5）。hy2 是 HTTP/3 服务端，认证
// 之前的请求按伪装站点处理。上游把连接整个交给 http3.Server.ServeQUICConn：每接一条
// 双向流就起一个 goroutine 读请求头，HEADERS 帧声明多长就先分配多大（至多 1MB），
// 又没有任何时限——只完成握手、不认证的客户端开满流、每条只发半个 HEADERS，每条
// 连接就能无限期挂住约 1032 个 goroutine 与数 MB 到 1GB 内存。现在由本包自己收流
// （不 fork quic-go / http3：http3 的 NewRawServerConn 与 HandleRequestStream 是公开
// 接口，控制流与 SETTINGS 帧和 ServeQUICConn 发的一样），约束都像真站一样只看请求：
//   - 读请求头限时：认证前每条请求流从收流起 preAuthHeaderTimeout 内要读完请求头，
//     超时只拒这条流（H3_REQUEST_INCOMPLETE，http3 读头失败的原有处理），连接不动；
//   - 空闲关连接：未认证的连接没有在途请求持续 preAuthIdleTimeout 才关（H3_NO_ERROR）。
//     有请求在途、伪装站正常在服务的连接不关，与连接活了多久无关；
//   - 认证前同时在途的请求（读请求头到响应结束）至多 preAuthMaxInflight 条，多出的流
//     收流时当场以 H3_REQUEST_REJECTED 拒掉（客户端可重试），不起 goroutine；
//   - 认证前 HEADERS 帧声明的长度至多 preAuthMaxHeaderBytes，超出的流以
//     H3_EXCESSIVE_LOAD 拒掉，不按声明长度分配。
//
// 于是单条未认证连接的占用有上界（至多 32 条在途请求的 goroutine 与 32×32KB 请求头），
// 只发半个请求头的流活不过读头限时，之后没有在途请求、空闲到点连接被关。认证之后的
// 流不受这些约束。
const (
	// DefaultPreAuthHeaderTimeout 与内核入站握手、读请求头的 10 秒（inboundHandshakeTimeout）同一口径。
	DefaultPreAuthHeaderTimeout = 10 * time.Second
	// DefaultPreAuthIdleTimeout 取 nginx 的缺省 keepalive_timeout 75 秒：nginx 的
	// HTTP/3 连接在没有在途请求时起这个计时，有请求到达就停，到点以 H3_NO_ERROR 关
	// （src/http/v3/ngx_http_v3_request.c 的 ngx_http_v3_init 与
	// ngx_http_v3_cleanup_connection，缺省值见 src/http/ngx_http_core_module.c 的
	// keepalive_timeout 75000）。Caddy 的同类设置 idle_timeout 缺省 5 分钟
	// （modules/caddyhttp/app.go defaultIdleTimeout，交给 quic-go 的
	// http3.Server.IdleTimeout，语义相同）。
	DefaultPreAuthIdleTimeout = 75 * time.Second
	// preAuthMaxInflight：hy2 客户端认证前只发一个请求；浏览器访问伪装站同时在途
	// 的请求一般十几条。
	preAuthMaxInflight = 32
	// preAuthMaxHeaderBytes：hy2 认证请求头不到 1KB（含随机填充）；常见 Web 服务器的
	// 请求头上限在 8–64KB（nginx 缺省 4×8KB）。
	preAuthMaxHeaderBytes = 32 << 10
)

func (s *Service[U]) handleConnection(connection *quic.Conn) {
	session := &serverSession[U]{
		Service:    s,
		ctx:        s.ctx,
		quicConn:   connection,
		connDone:   make(chan struct{}),
		udpConnMap: make(map[uint32]*udpPacketConn),
	}
	hconn, err := (&http3.Server{Handler: session}).NewRawServerConn(connection)
	if err != nil {
		_ = connection.CloseWithError(0, "")
		return
	}
	// 握手完成时没有在途请求，空闲计时从这里起。
	session.preAuthIdle = time.AfterFunc(s.preAuthIdleTimeout, session.onPreAuthIdle)
	defer session.preAuthIdle.Stop()
	go func() {
		for {
			str, err := connection.AcceptUniStream(context.Background())
			if err != nil {
				return
			}
			go hconn.HandleUnidirectionalStream(str)
		}
	}()
	for {
		str, err := connection.AcceptStream(context.Background())
		if err != nil {
			break
		}
		preAuth := !session.authenticated.Load()
		if preAuth {
			if !session.beginPreAuthRequest() {
				rejectStream(str, http3.ErrCodeRequestRejected)
				continue
			}
			// 在 begin 之后登记：流已结束时回调立即执行，也排在停空闲计时之后。
			context.AfterFunc(str.Context(), session.endPreAuthRequest)
			_ = str.SetReadDeadline(time.Now().Add(s.preAuthHeaderTimeout))
		}
		go session.serveStream(hconn, str, preAuth)
	}
	_ = connection.CloseWithError(0, "")
}

// beginPreAuthRequest 给一条认证前的请求流占在途名额，满了返回 false。在途从 0 变 1
// 时停掉空闲计时。
func (s *serverSession[U]) beginPreAuthRequest() bool {
	s.preAuthAccess.Lock()
	defer s.preAuthAccess.Unlock()
	if s.preAuthInflight >= preAuthMaxInflight {
		return false
	}
	s.preAuthInflight++
	if s.preAuthInflight == 1 {
		s.preAuthIdle.Stop()
	}
	return true
}

// endPreAuthRequest 在认证前的请求流结束（响应写完或流被拒）时归还名额；在途回到
// 0 且仍未认证时重新起空闲计时。
func (s *serverSession[U]) endPreAuthRequest() {
	s.preAuthAccess.Lock()
	defer s.preAuthAccess.Unlock()
	s.preAuthInflight--
	if s.preAuthInflight == 0 && !s.authenticated.Load() {
		s.preAuthIdle.Reset(s.preAuthIdleTimeout)
	}
}

// onPreAuthIdle 是空闲计时到点：仍未认证且没有在途请求才关连接（计时器与收流并发时
// 可能多触发一次，这里再核一遍）。
func (s *serverSession[U]) onPreAuthIdle() {
	s.preAuthAccess.Lock()
	idle := s.preAuthInflight == 0
	s.preAuthAccess.Unlock()
	if idle && !s.authenticated.Load() {
		_ = s.quicConn.CloseWithError(0, "")
	}
}

// serveStream 是 http3 handleConn 里每条双向流那一段：先看帧类型，hy2 的 TCP 请求
// 由 dispatchStream 接走，其余按 HTTP/3 请求交给 http3。认证前到达的 HEADERS 先
// 核声明长度。
func (s *serverSession[U]) serveStream(hconn *http3.RawServerConn, str *quic.Stream, preAuth bool) {
	frameType, err := quicvarint.Peek(str)
	if err == nil && preAuth && frameType == frameTypeHeaders {
		length, peekErr := peekFrameLength(str)
		switch {
		case peekErr != nil:
			rejectStream(str, http3.ErrCodeRequestIncomplete)
			return
		case length > preAuthMaxHeaderBytes:
			rejectStream(str, http3.ErrCodeExcessiveLoad)
			return
		}
	}
	handled, dispatchErr := s.dispatchStream(http3.FrameType(frameType), str, err, preAuth)
	if dispatchErr != nil {
		rejectStream(str, http3.ErrCodeRequestIncomplete)
		return
	}
	if handled {
		return
	}
	hconn.HandleRequestStream(str)
}

// frameTypeHeaders 是 HTTP/3 HEADERS 帧的类型（RFC 9114 7.2.2）。
const frameTypeHeaders = 0x01

// peekFrameLength 不消费地读出流上第一个帧的长度字段（类型 varint 之后的 varint）。
func peekFrameLength(str *quic.Stream) (uint64, error) {
	var b [16]byte
	if _, err := str.Peek(b[:1]); err != nil {
		return 0, err
	}
	typeLen := 1 << (b[0] >> 6)
	if _, err := str.Peek(b[:typeLen+1]); err != nil {
		return 0, err
	}
	lengthLen := 1 << (b[typeLen] >> 6)
	if _, err := str.Peek(b[:typeLen+lengthLen]); err != nil {
		return 0, err
	}
	length, _, err := quicvarint.Parse(b[typeLen : typeLen+lengthLen])
	return length, err
}

func rejectStream(str *quic.Stream, code http3.ErrCode) {
	str.CancelRead(quic.StreamErrorCode(code))
	str.CancelWrite(quic.StreamErrorCode(code))
}

type serverSession[U comparable] struct {
	*Service[U]
	ctx        context.Context
	quicConn   *quic.Conn
	connAccess sync.Mutex
	connDone   chan struct{}
	connErr    error
	// authenticated 在 connAccess 内由 false 置 true，且只置一次；authUser 在它之前
	// 写好，读侧先 Load 到 true 再读 authUser（Pandora 改动：并发 /auth 不再改身份）。
	authenticated atomic.Bool
	authUser      U
	// preAuthAccess 保护 preAuthInflight 与 preAuthIdle 的启停（Pandora 改动）：
	// preAuthInflight 是认证前同时在途的请求流数，见 preAuthMaxInflight；preAuthIdle
	// 是未认证连接的空闲计时，见 preAuthIdleTimeout。
	preAuthAccess   sync.Mutex
	preAuthInflight int
	preAuthIdle     *time.Timer
	udpAccess       sync.RWMutex
	// udpClosed 由 closeUDPSessions 在 udpAccess 内置位，此后不再建会话（Pandora 改动）。
	udpClosed  bool
	udpConnMap map[uint32]*udpPacketConn
	// destCache 只在 loopMessages 里用（Pandora 改动）。
	destCache destinationCache
}

func (s *serverSession[U]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 请求头已读完：撤掉认证前读请求头的限时，请求体与响应不受它约束（Pandora 改动）。
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	if r.Method == http.MethodPost && r.Host == protocol.URLHost && r.URL.Path == protocol.URLPath {
		// 认证在 connAccess 内判定并只成功一次（Pandora 改动）：上游无同步，同一连接上
		// 并发两次 /auth 会把 authUser 来回改（流量记到另一个用户）并起两个 loopMessages
		// （destCache 只许单协程）。已认证之后的 /auth 照旧回 OK，但不改身份。
		request := protocol.AuthRequestFromHeader(r.Header)
		s.connAccess.Lock()
		if s.authenticated.Load() {
			s.connAccess.Unlock()
			protocol.AuthResponseToHeader(w.Header(), protocol.AuthResponse{
				UDPEnabled: !s.udpDisabled,
				Rx:         s.receiveBPS,
				RxAuto:     s.receiveBPS == 0 && s.ignoreClientBandwidth,
			})
			w.WriteHeader(protocol.StatusAuthOK)
			return
		}
		s.userAccess.RLock()
		user, loaded := s.userMap[request.Auth]
		s.userAccess.RUnlock()
		if !loaded {
			s.connAccess.Unlock()
			s.masqueradeHandler.ServeHTTP(w, r)
			return
		}
		s.authUser = user
		s.authenticated.Store(true)
		s.connAccess.Unlock()
		var rxAuto bool
		if s.receiveBPS > 0 && s.ignoreClientBandwidth && request.Rx == 0 {
			s.logger.Debug("process connection from ", r.RemoteAddr, ": BBR disabled by server")
			s.masqueradeHandler.ServeHTTP(w, r)
			return
		} else if !(s.receiveBPS == 0 && s.ignoreClientBandwidth) && request.Rx > 0 {
			rx := request.Rx
			if s.sendBPS > 0 && rx > s.sendBPS {
				rx = s.sendBPS
			}
			s.quicConn.SetCongestionControl(hyCC.NewBrutalSender(rx, s.brutalDebug, s.logger))
		} else {
			timeFunc := ntp.TimeFuncFromContext(s.ctx)
			if timeFunc == nil {
				timeFunc = time.Now
			}
			s.quicConn.SetCongestionControl(congestion_meta2.NewBbrSender(
				congestion_meta2.DefaultClock{TimeFunc: timeFunc},
				congestion.ByteCount(s.quicConn.Config().InitialPacketSize),
				congestion.ByteCount(congestion_meta1.InitialCongestionWindow),
			))
			rxAuto = true
		}
		protocol.AuthResponseToHeader(w.Header(), protocol.AuthResponse{
			UDPEnabled: !s.udpDisabled,
			Rx:         s.receiveBPS,
			RxAuto:     rxAuto,
		})
		w.WriteHeader(protocol.StatusAuthOK)
		if s.ctx.Done() != nil {
			go func() {
				select {
				case <-s.ctx.Done():
					s.closeWithError(s.ctx.Err())
				case <-s.connDone:
				}
			}()
		}
		if !s.udpDisabled {
			go s.loopMessages()
		}
	} else {
		s.masqueradeHandler.ServeHTTP(w, r)
	}
}

func (s *serverSession[U]) dispatchStream(frameType http3.FrameType, stream *quic.Stream, err error, preAuth bool) (bool, error) {
	if !s.authenticated.Load() || err != nil {
		return false, nil
	}
	if frameType != protocol.FrameTypeTCPRequest {
		return false, nil
	}
	if preAuth {
		// 收流时还没认证、现在已认证的 TCP 请求：撤掉读请求头的限时（Pandora 改动）。
		_ = stream.SetReadDeadline(time.Time{})
	}
	_, err = quicvarint.Read(quicvarint.NewReader(stream))
	if err != nil {
		s.logger.Error(E.Cause(err, "seek frame type"))
		return true, nil
	}
	go func() {
		hErr := s.handleStream(stream)
		if hErr != nil {
			stream.CancelRead(0)
			stream.Close()
			s.logger.Error(E.Cause(hErr, "handle stream request"))
		}
	}()
	return true, nil
}

func (s *serverSession[U]) handleStream(stream *quic.Stream) error {
	destinationString, err := protocol.ReadTCPRequest(stream)
	if err != nil {
		return E.New("read TCP request")
	}
	s.handler.NewConnectionEx(auth.ContextWithUser(s.ctx, s.authUser), &serverConn{Stream: stream}, M.SocksaddrFromNet(s.quicConn.RemoteAddr()).Unwrap(), M.ParseSocksaddr(destinationString).Unwrap(), nil)
	return nil
}

func (s *serverSession[U]) closeWithError(err error) {
	s.connAccess.Lock()
	defer s.connAccess.Unlock()
	select {
	case <-s.connDone:
		return
	default:
		s.connErr = err
		close(s.connDone)
	}
	if E.IsClosedOrCanceled(err) {
		s.logger.Debug(E.Cause(err, "connection failed"))
	} else {
		s.logger.Error(E.Cause(err, "connection failed"))
	}
	_ = s.quicConn.CloseWithError(0, "")
	s.closeUDPSessions()
}

// closeUDPSessions 在连接断开时关掉挂在它上面的全部 UDP 会话（Pandora 改动）。
// 会话的 ctx 派生自服务而不是这条连接，上游不关的话要等 udpTimeout（缺省 5 分钟）
// 空闲才收尾：期间一直占着上游 socket、转发 goroutine、在线设备与每用户会话名额，
// 客户端早已断开却仍按在线上报。Close 里的 onDestroy 自己拿 udpAccess，这里先拷出再关。
func (s *serverSession[U]) closeUDPSessions() {
	s.udpAccess.Lock()
	// 置标记与拷列表在同一把锁里：TUIC 的 quic 中继模式在各单向流的 goroutine 里也会
	// 建会话，标记挡住拷贝之后才到的插入（handleUDPMessage）。
	s.udpClosed = true
	conns := make([]*udpPacketConn, 0, len(s.udpConnMap))
	for _, conn := range s.udpConnMap {
		conns = append(conns, conn)
	}
	s.udpAccess.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type serverConn struct {
	*quic.Stream
	responseWritten bool
}

func (c *serverConn) HandshakeFailure(err error) error {
	if c.responseWritten {
		return os.ErrInvalid
	}
	c.responseWritten = true
	buffer := protocol.WriteTCPResponse(false, err.Error(), nil)
	defer buffer.Release()
	return common.Error(c.Stream.Write(buffer.Bytes()))
}

func (c *serverConn) HandshakeSuccess() error {
	if c.responseWritten {
		return nil
	}
	c.responseWritten = true
	buffer := protocol.WriteTCPResponse(true, "", nil)
	defer buffer.Release()
	return common.Error(c.Stream.Write(buffer.Bytes()))
}

func (c *serverConn) Read(p []byte) (n int, err error) {
	n, err = c.Stream.Read(p)
	return n, qtls.WrapError(err)
}

func (c *serverConn) Write(p []byte) (n int, err error) {
	if !c.responseWritten {
		c.responseWritten = true
		buffer := protocol.WriteTCPResponse(true, "", p)
		defer buffer.Release()
		_, err = c.Stream.Write(buffer.Bytes())
		if err != nil {
			return 0, qtls.WrapError(err)
		}
		return len(p), nil
	}
	n, err = c.Stream.Write(p)
	return n, qtls.WrapError(err)
}

func (c *serverConn) LocalAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *serverConn) RemoteAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *serverConn) Close() error {
	c.Stream.CancelRead(0)
	return c.Stream.Close()
}
