package tuic

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/sagernet/quic-go"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"github.com/gofrs/uuid/v5"
)

type ServiceOptions struct {
	Context           context.Context
	Logger            logger.Logger
	TLSConfig         aTLS.ServerConfig
	CongestionControl string
	AuthTimeout       time.Duration
	ZeroRTTHandshake  bool
	Heartbeat         time.Duration
	UDPTimeout        time.Duration
	// UDPQueueSize 是每个 UDP 会话的接收队列长度，非正值用 DefaultUDPQueueSize
	// （Pandora 改动）。
	UDPQueueSize int
	Handler      ServiceHandler
}

// 每条 QUIC 连接上对端可同时打开的流数（Pandora 改动；上游 sing-quic 是 1<<60，
// 等于不设上限）。
//
// 上限同时约束认证之前：对端只要完成 TLS 握手就能把流开到上限，每条流在
// quic-go 里是一个流对象（本机实测连同测试客户端一侧约 3KB），上限即这段时间
// 的内存上限；而 QUIC 里开第 N 号流会隐式打开它之前的全部流，1<<60 时一个大
// 编号的 STREAM 帧就能让 quic-go 建出任意多的流对象。
//
// 取值按客户端行为定：sing-quic（sing-box 1.13）的客户端用不阻塞的 OpenStream /
// OpenUniStream，到上限立即报错、不等额度，一条 TUIC 连接上同时在途的 TCP 连接
// 数因此不能超过双向流上限（TestClientStreamLimitBehavior）。1024 与 Hysteria
// 官方服务端的 maxIncomingStreams 缺省一致，tun 模式的日常用量（几十到几百）
// 留足余量。单向流只承载认证、Dissociate 与 udp_relay_mode=quic 的 UDP 包（每包
// 一条）；面板订阅不下发 quic 模式（客户端缺省 native 走 DATAGRAM），手工改成
// quic 模式时在途包数超过 1024 的部分会被客户端丢掉。
const (
	serverMaxIncomingStreams    = 1024
	serverMaxIncomingUniStreams = 1024
)

type ServiceHandler interface {
	N.TCPConnectionHandlerEx
	N.UDPConnectionHandlerEx
}

type Service[U comparable] struct {
	ctx               context.Context
	logger            logger.Logger
	tlsConfig         aTLS.ServerConfig
	heartbeat         time.Duration
	quicConfig        *quic.Config
	userAccess        sync.RWMutex
	userMap           map[[16]byte]U
	passwordMap       map[U]string
	congestionControl string
	authTimeout       time.Duration
	udpTimeout        time.Duration
	udpQueueSize      int
	handler           ServiceHandler

	quicListener io.Closer
}

func NewService[U comparable](options ServiceOptions) (*Service[U], error) {
	if options.AuthTimeout == 0 {
		options.AuthTimeout = 3 * time.Second
	}
	if options.Heartbeat == 0 {
		options.Heartbeat = 10 * time.Second
	}
	quicConfig := &quic.Config{
		DisablePathMTUDiscovery: !(runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "android" || runtime.GOOS == "darwin"),
		EnableDatagrams:         true,
		Allow0RTT:               options.ZeroRTTHandshake,
		MaxIncomingStreams:      serverMaxIncomingStreams,
		MaxIncomingUniStreams:   serverMaxIncomingUniStreams,
		DisablePathManager:      true,
	}
	switch options.CongestionControl {
	case "":
		options.CongestionControl = "cubic"
	case "cubic", "new_reno", "bbr":
	default:
		return nil, E.New("unknown congestion control algorithm: ", options.CongestionControl)
	}
	return &Service[U]{
		ctx:               options.Context,
		logger:            options.Logger,
		tlsConfig:         options.TLSConfig,
		heartbeat:         options.Heartbeat,
		quicConfig:        quicConfig,
		userMap:           make(map[[16]byte]U),
		congestionControl: options.CongestionControl,
		authTimeout:       options.AuthTimeout,
		udpTimeout:        options.UDPTimeout,
		udpQueueSize:      options.UDPQueueSize,
		handler:           options.Handler,
	}, nil
}

func (s *Service[U]) UpdateUsers(userList []U, uuidList [][16]byte, passwordList []string) {
	userMap := make(map[[16]byte]U)
	passwordMap := make(map[U]string)
	for index := range userList {
		userMap[uuidList[index]] = userList[index]
		passwordMap[userList[index]] = passwordList[index]
	}
	s.userAccess.Lock()
	s.userMap = userMap
	s.passwordMap = passwordMap
	s.userAccess.Unlock()
}

func (s *Service[U]) Start(conn net.PacketConn) error {
	if !s.quicConfig.Allow0RTT {
		listener, err := qtls.Listen(conn, s.tlsConfig, s.quicConfig)
		if err != nil {
			return err
		}
		s.quicListener = listener
		go func() {
			for {
				connection, hErr := listener.Accept(s.ctx)
				if hErr != nil {
					if E.IsClosedOrCanceled(hErr) || errors.Is(hErr, quic.ErrServerClosed) {
						s.logger.Debug(E.Cause(hErr, "listener closed"))
					} else {
						s.logger.Error(E.Cause(hErr, "listener closed"))
					}
					return
				}
				go s.handleConnection(connection)
			}
		}()
	} else {
		listener, err := qtls.ListenEarly(conn, s.tlsConfig, s.quicConfig)
		if err != nil {
			return err
		}
		s.quicListener = listener
		go func() {
			for {
				connection, hErr := listener.Accept(s.ctx)
				if hErr != nil {
					if E.IsClosedOrCanceled(hErr) || errors.Is(hErr, quic.ErrServerClosed) {
						s.logger.Debug(E.Cause(hErr, "listener closed"))
					} else {
						s.logger.Error(E.Cause(hErr, "listener closed"))
					}
					return
				}
				go s.handleConnection(connection)
			}
		}()
	}
	return nil
}

func (s *Service[U]) Close() error {
	return common.Close(
		s.quicListener,
	)
}

func (s *Service[U]) handleConnection(connection *quic.Conn) {
	setCongestion(s.ctx, connection, s.congestionControl)
	session := &serverSession[U]{
		Service:    s,
		ctx:        s.ctx,
		quicConn:   connection,
		connDone:   make(chan struct{}),
		authDone:   make(chan struct{}),
		udpConnMap: make(map[uint16]*udpPacketConn),
	}
	session.handle()
}

type serverSession[U comparable] struct {
	*Service[U]
	ctx        context.Context
	quicConn   *quic.Conn
	connAccess sync.Mutex
	connDone   chan struct{}
	connErr    error
	authDone   chan struct{}
	authUser   U
	udpAccess  sync.RWMutex
	// udpClosed 由 closeUDPSessions 在 udpAccess 内置位，此后不再建会话（Pandora 改动）。
	udpClosed  bool
	udpConnMap map[uint16]*udpPacketConn
	// destCache 只在 loopMessages 里用（Pandora 改动）。
	destCache destinationCache
}

func (s *serverSession[U]) handle() {
	if s.ctx.Done() != nil {
		go func() {
			select {
			case <-s.ctx.Done():
				s.closeWithError(s.ctx.Err())
			case <-s.connDone:
			}
		}()
	}
	go s.loopUniStreams()
	go s.loopStreams()
	go s.loopMessages()
	go s.handleAuthTimeout()
	go s.loopHeartbeats()
}

// loopUniStreams 收单向流（Pandora 改动：认证前不放大）。
//
// 上游每接一条流就起一个 goroutine、先借 32KB 缓冲再等认证；流数上限又是
// 1<<60，只完成 TLS 握手、不认证的客户端在认证超时（3 秒）内能让服务端堆起
// 任意多的 goroutine 与缓冲。现在认证完成之前：
//   - 就在本 goroutine 里逐条读 2 字节命令头，不起 goroutine、不借缓冲；
//   - 认证流当场校验，通过即放行之前暂存的流；
//   - Packet / Dissociate 流只记下（流对象与命令字节），认证通过后再交给
//     各自的 goroutine；条数受 MaxIncomingUniStreams 约束（serverMaxIncomingUniStreams）。
//
// 正常客户端的认证流是连接上的第一批单向流之一，且随流就带着数据，逐条读不会
// 拖慢它；不发数据的流会卡住本循环，但那只会让这条连接撞上认证超时。
func (s *serverSession[U]) loopUniStreams() {
	var parked []parkedUniStream
	authenticated := false
	release := func() {
		for _, p := range parked {
			go s.runUniStream(p.stream, p.command)
		}
		parked = nil
	}
	for {
		uniStream, err := s.quicConn.AcceptUniStream(s.ctx)
		if err != nil {
			return
		}
		if !authenticated {
			select {
			case <-s.authDone:
				authenticated = true
				release()
			default:
			}
		}
		if authenticated {
			go func() {
				command, err := readUniCommand(uniStream)
				if err != nil {
					uniStream.CancelRead(0)
					s.closeWithError(E.Cause(err, "handle uni stream"))
					return
				}
				s.runUniStream(uniStream, command)
			}()
			continue
		}
		command, err := readUniCommand(uniStream)
		if err == nil && command == CommandAuthenticate {
			err = s.authenticate(uniStream)
			uniStream.CancelRead(0)
			if err == nil {
				authenticated = true
				release()
				continue
			}
		} else if err == nil && (command == CommandPacket || command == CommandDissociate) {
			parked = append(parked, parkedUniStream{stream: uniStream, command: command})
			continue
		} else if err == nil {
			err = E.New("unknown command ", command)
		}
		uniStream.CancelRead(0)
		for _, p := range parked {
			p.stream.CancelRead(0)
		}
		s.closeWithError(E.Cause(err, "handle uni stream"))
		return
	}
}

// parkedUniStream 是认证前到达、命令头已读的单向流（Pandora 改动）。
type parkedUniStream struct {
	stream  *quic.ReceiveStream
	command byte
}

// runUniStream 处理命令头已读的单向流，出错即关连接。
func (s *serverSession[U]) runUniStream(stream *quic.ReceiveStream, command byte) {
	err := s.handleUniStream(stream, command)
	if err != nil {
		s.closeWithError(E.Cause(err, "handle uni stream"))
	}
}

// readUniCommand 读单向流的 2 字节头（版本、命令），不借缓冲（Pandora 改动）。
func readUniCommand(stream *quic.ReceiveStream) (byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		return 0, E.Cause(err, "read request")
	}
	if header[0] != Version {
		return 0, E.New("unknown version ", header[0])
	}
	return header[1], nil
}

// authenticate 校验认证流的其余部分（命令头之后的 UUID 与令牌），通过即关 authDone。
// 只在认证完成之前由 loopUniStreams 调用（之后到的认证流在 handleUniStream 里被拒），
// authDone 因此只关一次。
func (s *serverSession[U]) authenticate(stream *quic.ReceiveStream) error {
	var request [AuthenticateLen - 2]byte
	if _, err := io.ReadFull(stream, request[:]); err != nil {
		return E.Cause(err, "authentication: read request")
	}
	var userUUID [16]byte
	copy(userUUID[:], request[:16])
	s.userAccess.RLock()
	user, loaded := s.userMap[userUUID]
	password := s.passwordMap[user]
	s.userAccess.RUnlock()
	if !loaded {
		return E.New("authentication: unknown user ", uuid.UUID(userUUID))
	}
	handshakeState := s.quicConn.ConnectionState()
	tuicToken, err := handshakeState.TLS.ExportKeyingMaterial(string(userUUID[:]), []byte(password), 32)
	if err != nil {
		return E.Cause(err, "authentication: export keying material")
	}
	if !bytes.Equal(tuicToken, request[16:16+32]) {
		return E.New("authentication: token mismatch")
	}
	s.authUser = user
	close(s.authDone)
	return nil
}

// handleUniStream 处理认证之后（或认证前暂存、认证后放行）的单向流，命令头已读。
// 上游先借 32KB 缓冲读头，再把缓冲里多读的部分拼回流前面；现在头按字节读，
// 负载直接从流里读（Pandora 改动）。
func (s *serverSession[U]) handleUniStream(stream *quic.ReceiveStream, command byte) error {
	defer stream.CancelRead(0)
	switch command {
	case CommandAuthenticate:
		return E.New("authentication: multiple authentication requests")
	case CommandPacket:
		select {
		case <-s.connDone:
			return s.connErr
		case <-s.authDone:
		}
		message := allocMessage()
		err := readUDPMessage(message, stream)
		if err != nil {
			message.release()
			return err
		}
		s.handleUDPMessage(message, true)
		return nil
	case CommandDissociate:
		select {
		case <-s.connDone:
			return s.connErr
		case <-s.authDone:
		}
		var sessionID uint16
		err := binary.Read(stream, binary.BigEndian, &sessionID)
		if err != nil {
			return err
		}
		s.udpAccess.RLock()
		udpConn, loaded := s.udpConnMap[sessionID]
		s.udpAccess.RUnlock()
		if loaded {
			udpConn.closeWithError(E.New("remote closed"))
			s.udpAccess.Lock()
			delete(s.udpConnMap, sessionID)
			s.udpAccess.Unlock()
		}
		return nil
	default:
		return E.New("unknown command ", command)
	}
}

func (s *serverSession[U]) handleAuthTimeout() {
	select {
	case <-s.connDone:
	case <-s.authDone:
	case <-time.After(s.authTimeout):
		s.closeWithError(E.New("authentication timeout"))
	}
}

// loopStreams 收双向流。认证完成之前不 Accept（Pandora 改动）：早到的双向流留在
// quic-go 的接收队列里（条数受 MaxIncomingStreams 约束），不起 goroutine、不读。
func (s *serverSession[U]) loopStreams() {
	select {
	case <-s.connDone:
		return
	case <-s.authDone:
	}
	for {
		stream, err := s.quicConn.AcceptStream(s.ctx)
		if err != nil {
			return
		}
		go func() {
			err = s.handleStream(stream)
			if err != nil {
				stream.CancelRead(0)
				stream.Close()
				s.logger.Error(E.Cause(err, "handle stream request"))
			}
		}()
	}
}

func (s *serverSession[U]) handleStream(stream *quic.Stream) error {
	buffer := buf.NewSize(2 + M.MaxSocksaddrLength)
	defer buffer.Release()
	_, err := buffer.ReadAtLeastFrom(stream, 2)
	if err != nil {
		return E.Cause(err, "read request")
	}
	version, _ := buffer.ReadByte()
	if version != Version {
		return E.New("unknown version ", buffer.Byte(0))
	}
	command, _ := buffer.ReadByte()
	if command != CommandConnect {
		return E.New("unsupported stream command ", command)
	}
	destination, err := AddressSerializer.ReadAddrPort(io.MultiReader(buffer, stream))
	if err != nil {
		return E.Cause(err, "read request destination")
	}
	select {
	case <-s.connDone:
		return s.connErr
	case <-s.authDone:
	}
	var conn net.Conn = &serverConn{
		Stream:      stream,
		destination: destination,
	}
	if !buffer.IsEmpty() {
		conn = bufio.NewCachedConn(conn, buffer.ToOwned())
	}
	s.handler.NewConnectionEx(auth.ContextWithUser(s.ctx, s.authUser), conn, M.SocksaddrFromNet(s.quicConn.RemoteAddr()).Unwrap(), destination, nil)
	return nil
}

func (s *serverSession[U]) loopHeartbeats() {
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-s.connDone:
			return
		case <-ticker.C:
			err := s.quicConn.SendDatagram([]byte{Version, CommandHeartbeat})
			if err != nil {
				s.closeWithError(E.Cause(err, "send heartbeat"))
			}
		}
	}
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
	destination M.Socksaddr
}

func (c *serverConn) Read(p []byte) (n int, err error) {
	n, err = c.Stream.Read(p)
	return n, qtls.WrapError(err)
}

func (c *serverConn) Write(p []byte) (n int, err error) {
	n, err = c.Stream.Write(p)
	return n, qtls.WrapError(err)
}

func (c *serverConn) LocalAddr() net.Addr {
	return c.destination
}

func (c *serverConn) RemoteAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *serverConn) Close() error {
	c.Stream.CancelRead(0)
	return c.Stream.Close()
}
