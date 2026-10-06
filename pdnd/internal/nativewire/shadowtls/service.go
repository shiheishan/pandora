// [INPUT]: 依赖 sing 的 bufio.CopyConn / task.Group 做握手与诱饵中继，依赖 v1_server.go、v2_server.go、v3_server.go 的帧级握手状态机
// [OUTPUT]: 对外提供 Service（NewService / NewConnection）、ServiceConfig、User、HandshakeConfig、WildcardSNI、DefaultHandshakeTimeout
// [POS]: nativewire/shadowtls 的服务端入口：按版本走伪装握手、判定认证与否，认证流交给 Handler，未认证流回落到诱饵站点；判定前限时 HandshakeTimeout，判定后不限时；唯一调用方是 kernel/shadowtls.go

package shadowtls

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"net"
	"os"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/debug"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/task"
)

type Service struct {
	version                int
	password               string
	users                  []User
	handshake              HandshakeConfig
	handshakeForServerName map[string]HandshakeConfig
	strictMode             bool
	wildcardSNI            WildcardSNI
	handler                N.TCPConnectionHandlerEx
	logger                 logger.ContextLogger
	handshakeTimeout       time.Duration
}

type WildcardSNI int

const (
	WildcardSNIOff WildcardSNI = iota
	WildcardSNIAuthed
	WildcardSNIAll
)

type ServiceConfig struct {
	Version                int
	Password               string // for protocol version 2
	Users                  []User // for protocol version 3
	Handshake              HandshakeConfig
	HandshakeForServerName map[string]HandshakeConfig // for protocol version 2/3
	StrictMode             bool                       // for protocol version 3
	WildcardSNI            WildcardSNI                // for protocol version 3
	Handler                N.TCPConnectionHandlerEx
	Logger                 logger.ContextLogger
	// HandshakeTimeout 限定认证判定之前等对端的时间，<=0 取 DefaultHandshakeTimeout。
	HandshakeTimeout time.Duration
}

// DefaultHandshakeTimeout 是 HandshakeTimeout 的缺省值。kernel 会显式传入
// 自己的 inboundHandshakeTimeout（同为 10 秒，口径以 kernel 为准）；这里的
// 缺省只是兜底，让零值配置不至于退回「静默连接永远挂着」。
const DefaultHandshakeTimeout = 10 * time.Second

type User struct {
	Name     string
	Password string
}

type HandshakeConfig struct {
	Server M.Socksaddr
	Dialer N.Dialer
}

func NewService(config ServiceConfig) (*Service, error) {
	service := &Service{
		version:                config.Version,
		password:               config.Password,
		users:                  config.Users,
		handshake:              config.Handshake,
		handshakeForServerName: config.HandshakeForServerName,
		strictMode:             config.StrictMode,
		wildcardSNI:            config.WildcardSNI,
		handler:                config.Handler,
		logger:                 config.Logger,
		handshakeTimeout:       config.HandshakeTimeout,
	}
	if service.handshakeTimeout <= 0 {
		service.handshakeTimeout = DefaultHandshakeTimeout
	}

	if !service.handshake.Server.IsValid() && service.wildcardSNI == WildcardSNIOff {
		return nil, E.New("missing default handshake information")
	}

	if service.handler == nil || service.logger == nil {
		return nil, os.ErrInvalid
	}
	switch config.Version {
	case 1, 2:
	case 3:
		if len(service.users) == 0 {
			return nil, E.New("missing users")
		}
	default:
		return nil, E.New("unknown protocol version: ", config.Version)
	}

	return service, nil
}

func (s *Service) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) error {
	switch s.version {
	default:
		fallthrough
	case 1:
		// v1 没有认证判定，握手一结束就交给 Handler：整段握手中继都是判定前，
		// 两条连接挂同一个截止时间（客户端不说话、诱饵不回话都会触发），
		// 交出连接之前清掉。
		deadline := time.Now().Add(s.handshakeTimeout)
		_ = conn.SetDeadline(deadline)
		handshakeConn, err := s.handshake.Dialer.DialContext(ctx, N.NetworkTCP, s.handshake.Server)
		if err != nil {
			return E.Cause(err, "server handshake")
		}
		_ = handshakeConn.SetDeadline(deadline)

		var group task.Group
		group.Append("client handshake", func(ctx context.Context) error {
			return copyUntilHandshakeFinished(handshakeConn, conn)
		})
		group.Append("server handshake", func(ctx context.Context) error {
			return copyUntilHandshakeFinished(conn, handshakeConn)
		})
		group.FastFail()
		group.Cleanup(func() {
			handshakeConn.Close()
		})
		err = group.Run(ctx)
		if err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Time{})
		s.logger.TraceContext(ctx, "handshake finished")
		s.handler.NewConnectionEx(ctx, conn, source, destination, onClose)
		return nil
	case 2:
		// 只限时读 ClientHello。v2 不认证 ClientHello，之后的握手中继在判定
		// （首个带正确哈希的应用数据帧）之前对外就是诱饵中继，不能限时。
		clientHelloFrame, err := s.readClientHello(conn)
		if err != nil {
			return E.Cause(err, "read client handshake")
		}
		serverName, err := extractServerName(clientHelloFrame.Bytes())
		var handshakeConfig HandshakeConfig
		if err == nil {
			if customHandshake, found := s.handshakeForServerName[serverName]; found {
				handshakeConfig = customHandshake
			} else {
				handshakeConfig = s.handshake
			}
		} else {
			handshakeConfig = s.handshake
		}
		handshakeConn, err := handshakeConfig.Dialer.DialContext(ctx, N.NetworkTCP, handshakeConfig.Server)
		if err != nil {
			return E.Cause(err, "server handshake")
		}
		hashConn := newHashWriteConn(conn, s.password)
		go bufio.Copy(hashConn, handshakeConn)
		var request *buf.Buffer
		request, err = copyUntilHandshakeFinishedV2(ctx, s.logger, handshakeConn, bufio.NewCachedConn(conn, clientHelloFrame), hashConn, 2)
		if err == nil {
			s.logger.TraceContext(ctx, "handshake finished")
			handshakeConn.Close()
			s.handler.NewConnectionEx(ctx, bufio.NewCachedConn(newConn(conn), request), source, destination, onClose)
			return nil
		} else if err == os.ErrPermission {
			s.logger.WarnContext(ctx, "fallback connection")
			hashConn.Fallback()
			return common.Error(bufio.Copy(handshakeConn, conn))
		} else {
			return err
		}
	case 3:
		// 限时只覆盖两步：读 ClientHello；校验通过时与诱饵交换 ClientHello /
		// ServerHello。校验失败的回落 CopyConn、等首个 HMAC 帧的握手中继都
		// 不限时——前者就是诱饵会话，后者对重放 ClientHello 的探测者也是。
		clientHelloFrame, err := s.readClientHello(conn)
		if err != nil {
			return E.Cause(err, "read client handshake")
		}
		defer clientHelloFrame.Release()
		serverName, err := extractServerName(clientHelloFrame.Bytes())
		if err != nil {
			return E.Cause(err, "extract server name")
		}
		var (
			handshakeConfig HandshakeConfig
			isCustom        bool
		)
		if customHandshake, found := s.handshakeForServerName[serverName]; found {
			handshakeConfig = customHandshake
			isCustom = true
		} else {
			handshakeConfig = s.handshake
			if s.wildcardSNI != WildcardSNIOff {
				handshakeConfig.Server = M.Socksaddr{
					Fqdn: serverName,
					Port: 443,
				}
			}
		}
		var handshakeConn net.Conn
		user, err := verifyClientHello(clientHelloFrame.Bytes(), s.users)
		if err != nil {
			s.logger.WarnContext(ctx, E.Cause(err, "client hello verify failed"))
			if s.wildcardSNI == WildcardSNIAll || isCustom {
				handshakeConn, err = handshakeConfig.Dialer.DialContext(ctx, N.NetworkTCP, handshakeConfig.Server)
			} else {
				handshakeConn, err = s.handshake.Dialer.DialContext(ctx, N.NetworkTCP, s.handshake.Server)
			}
			if err != nil {
				return E.Cause(err, "server handshake")
			}
			return bufio.CopyConn(ctx, bufio.NewCachedConn(conn, clientHelloFrame), handshakeConn)
		}
		if user.Name != "" {
			ctx = auth.ContextWithUser(ctx, user.Name)
		}
		s.logger.TraceContext(ctx, "client hello verify success")

		handshakeConn, err = handshakeConfig.Dialer.DialContext(ctx, N.NetworkTCP, handshakeConfig.Server)
		if err != nil {
			return E.Cause(err, "server handshake")
		}

		serverHelloFrame, err := s.relayServerHello(conn, handshakeConn, clientHelloFrame)
		clientHelloFrame.Release()
		if err != nil {
			handshakeConn.Close()
			return err
		}

		serverRandom := extractServerRandom(serverHelloFrame.Bytes())

		if serverRandom == nil {
			s.logger.WarnContext(ctx, "server random extract failed, will copy bidirectional")
			return bufio.CopyConn(ctx, conn, handshakeConn)
		}

		if s.strictMode && !isServerHelloSupportTLS13(serverHelloFrame.Bytes()) {
			s.logger.WarnContext(ctx, "TLS 1.3 is not supported, will copy bidirectional")
			return bufio.CopyConn(ctx, conn, handshakeConn)
		}

		serverHelloFrame.Release()
		if debug.Enabled {
			s.logger.TraceContext(ctx, "client authenticated. server random extracted: ", hex.EncodeToString(serverRandom))
		}
		hmacWrite := hmac.New(sha1.New, []byte(user.Password))
		hmacWrite.Write(serverRandom)
		hmacAdd := hmac.New(sha1.New, []byte(user.Password))
		hmacAdd.Write(serverRandom)
		hmacAdd.Write([]byte("S"))
		hmacVerify := hmac.New(sha1.New, []byte(user.Password))
		hmacVerifyReset := func() {
			hmacVerify.Reset()
			hmacVerify.Write(serverRandom)
			hmacVerify.Write([]byte("C"))
		}

		var clientFirstFrame *buf.Buffer
		var group task.Group
		var handshakeFinished bool
		group.Append("client handshake relay", func(ctx context.Context) error {
			clientFrame, cErr := copyByFrameUntilHMACMatches(conn, handshakeConn, hmacVerify, hmacVerifyReset)
			if cErr == nil {
				clientFirstFrame = clientFrame
				handshakeFinished = true
				handshakeConn.Close()
			}
			return cErr
		})
		group.Append("server handshake relay", func(ctx context.Context) error {
			cErr := copyByFrameWithModification(handshakeConn, conn, user.Password, serverRandom, hmacWrite)
			if E.IsClosedOrCanceled(cErr) && handshakeFinished {
				return nil
			}
			return cErr
		})
		group.Cleanup(func() {
			handshakeConn.Close()
		})
		err = group.Run(ctx)
		if err != nil {
			return E.Cause(err, "handshake relay")
		}
		s.logger.TraceContext(ctx, "handshake relay finished")
		s.handler.NewConnectionEx(ctx, bufio.NewCachedConn(newVerifiedConn(conn, hmacAdd, hmacVerify, nil), clientFirstFrame), source, destination, onClose)
		return nil
	}
}

// ============================================================
//  判定前限时
// ============================================================
//
// 截止时间只挂在「等对端第一段握手数据」上，判定一出就清：
//   - 认证失败回落诱饵、v2 的握手中继、v3 等首个 HMAC 帧，这几段对外都是
//     诱饵站点的 TLS 会话。真站点不会在 10 秒时掐断空闲但存活的会话，这里
//     掐了就是指纹；合法客户端握手后到第一次写之前本来就可能空闲。
//   - 所以这些阶段在这里不设上限；v1 没有回落，整段握手都在判定前，另算。

// readClientHello 限时读客户端的第一个 TLS 记录，读完（无论成败）即清掉
// 截止时间，之后的回落与握手中继不受它约束。
func (s *Service) readClientHello(conn net.Conn) (*buf.Buffer, error) {
	_ = conn.SetDeadline(time.Now().Add(s.handshakeTimeout))
	frame, err := extractFrame(conn)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		return nil, err
	}
	return frame, nil
}

// relayServerHello 是 v3 通过 ClientHello 校验后的一步：把 ClientHello 转给
// 诱饵、读回 ServerHello、再转给客户端。两条连接同挂一个截止时间，成功后
// 都清掉；失败时由调用方关诱饵连接。能走到这里的只有合法或重放的
// ClientHello，仍在判定之前。
func (s *Service) relayServerHello(conn net.Conn, handshakeConn net.Conn, clientHello *buf.Buffer) (*buf.Buffer, error) {
	deadline := time.Now().Add(s.handshakeTimeout)
	_ = conn.SetDeadline(deadline)
	_ = handshakeConn.SetDeadline(deadline)
	if _, err := handshakeConn.Write(clientHello.Bytes()); err != nil {
		return nil, E.Cause(err, "write client handshake")
	}
	serverHelloFrame, err := extractFrame(handshakeConn)
	if err != nil {
		return nil, E.Cause(err, "read server handshake")
	}
	if _, err = conn.Write(serverHelloFrame.Bytes()); err != nil {
		serverHelloFrame.Release()
		return nil, E.Cause(err, "write server handshake")
	}
	_ = conn.SetDeadline(time.Time{})
	_ = handshakeConn.SetDeadline(time.Time{})
	return serverHelloFrame, nil
}
