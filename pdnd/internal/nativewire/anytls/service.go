package anytls

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/aegispanel/nodeagent/internal/nativewire/anytls/padding"
	"github.com/aegispanel/nodeagent/internal/nativewire/anytls/session"
	"github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// errStreamRefused 是子流打不开时经 cmdSYNACK 发给客户端的文字，与 kernel 适配器
// 的 errAnyTLSStreamRefused 同一句：中性、不带内部原因。
var errStreamRefused = errors.New("connection failed")

type Service struct {
	userAccess      sync.RWMutex
	users           map[[32]byte]string
	padding         atomic.TypedValue[*padding.PaddingFactory]
	handler         N.TCPConnectionHandlerEx
	fallbackHandler N.TCPConnectionHandlerEx
	logger          logger.ContextLogger
}

type ServiceConfig struct {
	PaddingScheme   []byte
	Users           []User
	Handler         N.TCPConnectionHandlerEx
	FallbackHandler N.TCPConnectionHandlerEx
	Logger          logger.ContextLogger
}

type User struct {
	Name     string
	Password string
}

func NewService(config ServiceConfig) (*Service, error) {
	service := &Service{
		handler:         config.Handler,
		fallbackHandler: config.FallbackHandler,
		logger:          config.Logger,
		users:           make(map[[32]byte]string),
	}

	if service.handler == nil || service.logger == nil {
		return nil, os.ErrInvalid
	}

	for _, user := range config.Users {
		service.users[sha256.Sum256([]byte(user.Password))] = user.Name
	}

	if !padding.UpdatePaddingScheme(config.PaddingScheme, &service.padding) {
		return nil, errors.New("incorrect padding scheme format")
	}

	return service, nil
}

func (s *Service) UpdateUsers(users []User) {
	u := make(map[[32]byte]string)
	for _, user := range users {
		u[sha256.Sum256([]byte(user.Password))] = user.Name
	}
	s.userAccess.Lock()
	s.users = u
	s.userAccess.Unlock()
}

// NewConnection `conn` should be plaintext
func (s *Service) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, onClose N.CloseHandlerFunc) error {
	b := buf.NewPacket()
	defer b.Release()

	n, err := b.ReadOnceFrom(conn)
	if err != nil {
		return err
	}
	conn = bufio.NewCachedConn(conn, b)

	by, err := b.ReadBytes(32)
	if err != nil {
		b.Resize(0, n)
		return s.fallback(ctx, conn, source, err, onClose)
	}
	var passwordSha256 [32]byte
	copy(passwordSha256[:], by)
	s.userAccess.RLock()
	user, ok := s.users[passwordSha256]
	s.userAccess.RUnlock()
	if ok {
		ctx = auth.ContextWithUser(ctx, user)
	} else {
		b.Resize(0, n)
		return s.fallback(ctx, conn, source, E.New("unknown user password"), onClose)
	}
	by, err = b.ReadBytes(2)
	if err != nil {
		b.Resize(0, n)
		return s.fallback(ctx, conn, source, E.Extend(err, "read padding length"), onClose)
	}
	paddingLen := binary.BigEndian.Uint16(by)
	if paddingLen > 0 {
		_, err = b.ReadBytes(int(paddingLen))
		if err != nil {
			b.Resize(0, n)
			return s.fallback(ctx, conn, source, E.Extend(err, "read padding"), onClose)
		}
	}

	session := session.NewServerSession(conn, func(stream *session.Stream) {
		destination, err := M.SocksaddrSerializer.ReadAddrPort(stream)
		if err != nil {
			s.logger.ErrorContext(ctx, "ReadAddrPort:", err)
			// 本仓库改动：上游在这里直接 return，既不回 cmdSYNACK 也不关流——流滞留
			// 在会话里直到会话结束，v2 客户端 3 秒后还会关掉整条会话。改为回中性的
			// 失败再关流（v1 客户端收不到 SYNACK，靠关流的 FIN）。
			_ = stream.HandshakeFailure(errStreamRefused)
			_ = stream.Close()
			return
		}

		s.handler.NewConnectionEx(ctx, stream, source, destination, onClose)
	}, &s.padding, s.logger)
	session.Run()
	session.Close()
	return nil
}

func (s *Service) fallback(ctx context.Context, conn net.Conn, source M.Socksaddr, err error, onClose N.CloseHandlerFunc) error {
	if s.fallbackHandler == nil {
		return E.Extend(err, "fallback disabled")
	}
	s.fallbackHandler.NewConnectionEx(ctx, conn, source, M.Socksaddr{}, onClose)
	return nil
}
