package sing

import (
	"context"
	"net"
	"strings"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
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
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

// AnyTLSInbound 是带用户热管理与流量计数的 AnyTLS 入站。
//
// 与其它协议的一处关键差异：AnyTLS 在 context 里传的是用户名字符串
// 而不是索引，所以这里把用户名直接设成 UUID，回查时用 byUUID。
// 这反而更稳 —— 用户表增删不会让正在传输的连接错认身份。
type AnyTLSInbound struct {
	managedBase
	router    adapter.ConnectionRouterEx
	listener  *listener.Listener
	tlsConfig tls.ServerConfig
	service   *anytls.Service
}

var (
	_ adapter.Inbound              = (*AnyTLSInbound)(nil)
	_ adapter.TCPInjectableInbound = (*AnyTLSInbound)(nil)
	_ managedInbound               = (*AnyTLSInbound)(nil)
)

func RegisterAnyTLS(registry *inbound.Registry) {
	inbound.Register[option.AnyTLSInboundOptions](registry, C.TypeAnyTLS, NewAnyTLSInbound)
}

func NewAnyTLSInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {

	in := &AnyTLSInbound{
		managedBase: newManagedBase(C.TypeAnyTLS, tag, ctx, logger),
		router:      uot.NewRouter(router, logger),
	}

	if options.TLS != nil && options.TLS.Enabled {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		in.tlsConfig = tlsConfig
	}

	paddingScheme := padding.DefaultPaddingScheme
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}

	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			seed = append(seed, core.User{UUID: u.Password})
		}
		in.users.Add(seed)
	}

	service, err := anytls.NewService(anytls.ServiceConfig{
		Users:         in.anytlsUsers(),
		PaddingScheme: paddingScheme,
		Handler:       (*anytlsHandler)(in),
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}
	in.service = service

	in.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: in,
	})
	return in, nil
}

// anytlsUsers 把用户表翻译成 AnyTLS 的形态。
// Name 与 Password 同为 UUID：Name 是 service 回传给我们的标识，
// 让它等于 UUID 才能在 newConnectionEx 里一步查到人。
func (h *AnyTLSInbound) anytlsUsers() []anytls.User {
	list := h.users.Snapshot()
	out := make([]anytls.User, 0, len(list))
	for _, u := range list {
		out = append(out, anytls.User{Name: u.UUID, Password: u.UUID})
	}
	return out
}

func (h *AnyTLSInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	h.service.UpdateUsers(h.anytlsUsers())
	return nil
}

func (h *AnyTLSInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	h.service.UpdateUsers(h.anytlsUsers())
	return nil
}

func (h *AnyTLSInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return err
		}
	}
	return h.listener.Start()
}

func (h *AnyTLSInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig)
}

func (h *AnyTLSInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "来自 ", metadata.Source, " 的连接：TLS 握手失败"))
			return
		}
		conn = tlsConn
	}
	if err := h.service.NewConnection(adapter.WithContext(ctx, &metadata),
		conn, metadata.Source, onClose); err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "处理来自 ", metadata.Source, " 的连接"))
	}
}

// anytlsHandler 接收 service 解出的已认证连接。
type anytlsHandler AnyTLSInbound

func (t *anytlsHandler) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {

	h := (*AnyTLSInbound)(t)
	metadata := adapter.InboundContext{
		Inbound: h.Tag(), InboundType: h.Type(),
		Source: source, Destination: destination.Unwrap(),
	}
	name, _ := auth.UserFromContext[string](ctx)
	u, ok := h.users.ByUUID(name)
	if !ok {
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = u.UUID
	h.routeConn(ctx, h.router, u, conn, metadata, onClose)
}
