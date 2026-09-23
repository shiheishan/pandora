package sing

import (
	std_bufio "bufio"
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/http"
	"github.com/sagernet/sing/protocol/socks"
	"github.com/sagernet/sing/protocol/socks/socks4"
	"github.com/sagernet/sing/protocol/socks/socks5"

	"github.com/aegispanel/nodeagent/core"
)

// ProxyInbound 实现 SOCKS / HTTP / mixed 三种明文代理入站。
//
// 三者的用户模型完全一致（用户名 + 密码），握手之后的处理也一致，
// 差别只在第一个字节怎么解析 —— mixed 就是「看第一个字节再决定」。
// 因此这里只有一份实现，由 mode 决定是否做协议嗅探。
//
// 这类协议本身不加密，机场很少直接对外提供，但客户端（小火箭等）
// 普遍支持，作为落地入口或内网中转很有用，因此一并实现。
type ProxyInbound struct {
	managedBase
	mode       string // socks / http / mixed
	router     adapter.ConnectionRouterEx
	listener   *listener.Listener
	tlsConfig  tls.ServerConfig
	udpTimeout time.Duration

	// auth.Authenticator 内部是一个无锁的 map，构造后不可改。
	// 热更新用户只能整体换掉它，用原子指针保证读侧（每条新连接）
	// 永远看到一个完整一致的快照，而不是改到一半的 map。
	authn atomic.Pointer[auth.Authenticator]
}

var (
	_ adapter.Inbound              = (*ProxyInbound)(nil)
	_ adapter.TCPInjectableInbound = (*ProxyInbound)(nil)
	_ managedInbound               = (*ProxyInbound)(nil)
)

func RegisterProxy(registry *inbound.Registry) {
	inbound.Register[option.SocksInboundOptions](registry, C.TypeSOCKS,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger,
			tag string, o option.SocksInboundOptions) (adapter.Inbound, error) {
			return newProxyInbound(ctx, router, logger, tag, C.TypeSOCKS, "socks",
				o.ListenOptions, o.Users, nil, time.Duration(o.UDPTimeout))
		})
	inbound.Register[option.HTTPMixedInboundOptions](registry, C.TypeHTTP,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger,
			tag string, o option.HTTPMixedInboundOptions) (adapter.Inbound, error) {
			return newProxyInbound(ctx, router, logger, tag, C.TypeHTTP, "http",
				o.ListenOptions, o.Users, o.TLS, time.Duration(o.UDPTimeout))
		})
	inbound.Register[option.HTTPMixedInboundOptions](registry, C.TypeMixed,
		func(ctx context.Context, router adapter.Router, logger log.ContextLogger,
			tag string, o option.HTTPMixedInboundOptions) (adapter.Inbound, error) {
			return newProxyInbound(ctx, router, logger, tag, C.TypeMixed, "mixed",
				o.ListenOptions, o.Users, o.TLS, time.Duration(o.UDPTimeout))
		})
}

func newProxyInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag, protocol, mode string, listenOptions option.ListenOptions, users []auth.User,
	tlsOptions *option.InboundTLSOptions, udpTimeout time.Duration) (adapter.Inbound, error) {

	if udpTimeout == 0 {
		udpTimeout = C.UDPTimeout
	}
	in := &ProxyInbound{
		managedBase: newManagedBase(protocol, tag, ctx, logger),
		mode:        mode,
		router:      uot.NewRouter(router, logger),
		udpTimeout:  udpTimeout,
	}

	if len(users) > 0 {
		seed := make([]core.User, 0, len(users))
		for _, u := range users {
			seed = append(seed, core.User{UUID: u.Username})
		}
		in.users.Add(seed)
	}
	in.applyUsers()

	if tlsOptions != nil {
		tlsConfig, err := tls.NewServerWithOptions(tls.ServerOptions{
			Context: ctx, Logger: logger,
			Options:        common.PtrValueOrDefault(tlsOptions),
			KTLSCompatible: true,
		})
		if err != nil {
			return nil, err
		}
		in.tlsConfig = tlsConfig
	}

	in.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            listenOptions,
		ConnectionHandler: in,
	})
	return in, nil
}

// applyUsers 用当前用户表重建认证器。
// 用户名与密码同为 UUID —— 面板只有一个身份字段，
// 拆成两个反而要在订阅生成侧再编一套规则。
func (h *ProxyInbound) applyUsers() {
	list := h.users.Snapshot()
	users := make([]auth.User, 0, len(list))
	for _, u := range list {
		users = append(users, auth.User{Username: u.UUID, Password: u.UUID})
	}
	h.authn.Store(auth.NewAuthenticator(users))
}

func (h *ProxyInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *ProxyInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *ProxyInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return E.Cause(err, "启动 TLS")
		}
	}
	return h.listener.Start()
}

func (h *ProxyInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig)
}

func (h *ProxyInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	err := h.handshake(ctx, conn, metadata, onClose)
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil && !E.IsClosedOrCanceled(err) {
		h.logger.ErrorContext(ctx, E.Cause(err, "处理来自 ", metadata.Source, " 的连接"))
	}
}

func (h *ProxyInbound) handshake(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) error {

	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			return E.Cause(err, "TLS 握手")
		}
		conn = tlsConn
	}

	authn := h.authn.Load()
	handler := adapter.NewUpstreamHandlerEx(metadata, h.newUserConnection, h.newUserPacketConnection)
	reader := std_bufio.NewReader(conn)

	switch h.mode {
	case "socks":
		return socks.HandleConnectionEx(ctx, conn, reader, authn, handler,
			h.listener, h.udpTimeout, metadata.Source, onClose)
	case "http":
		return http.HandleConnectionEx(ctx, conn, reader, authn, handler, metadata.Source, onClose)
	default:
		// mixed：SOCKS4/5 的首字节是版本号，HTTP 的首字节是方法名的首字母。
		// 两者的取值空间不重叠，一个字节就足以区分。
		head, err := reader.Peek(1)
		if err != nil {
			return E.Cause(err, "读取首字节")
		}
		switch head[0] {
		case socks4.Version, socks5.Version:
			return socks.HandleConnectionEx(ctx, conn, reader, authn, handler,
				h.listener, h.udpTimeout, metadata.Source, onClose)
		default:
			return http.HandleConnectionEx(ctx, conn, reader, authn, handler, metadata.Source, onClose)
		}
	}
}

func (h *ProxyInbound) newUserConnection(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	name, _ := auth.UserFromContext[string](ctx)
	u, ok := h.users.ByUUID(name)
	if !ok {
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = u.UUID
	h.routeConn(ctx, h.router, u, conn, metadata, onClose)
}

func (h *ProxyInbound) newUserPacketConnection(ctx context.Context, conn N.PacketConn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	name, _ := auth.UserFromContext[string](ctx)
	u, ok := h.users.ByUUID(name)
	if !ok {
		h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = u.UUID
	h.routePacketConn(ctx, h.router, u, conn, metadata, onClose)
}
