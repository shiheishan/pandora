package sing

import (
	"context"
	"net"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/trojan"
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

// TrojanInbound 是带用户热管理与流量计数的 Trojan 入站。
//
// Trojan 用密码而非 UUID 标识用户，这里直接把 core.User.UUID 当作密码 ——
// 面板下发的是同一个字段，由协议层自行解释（见 core.User 注释）。
type TrojanInbound struct {
	managedBase
	router    adapter.ConnectionRouterEx
	listener  *listener.Listener
	service   *trojan.Service[int]
	tlsConfig tls.ServerConfig
	transport adapter.V2RayServerTransport
}

var (
	_ adapter.Inbound              = (*TrojanInbound)(nil)
	_ adapter.TCPInjectableInbound = (*TrojanInbound)(nil)
	_ managedInbound               = (*TrojanInbound)(nil)
)

func RegisterTrojan(registry *inbound.Registry) {
	inbound.Register[option.TrojanInboundOptions](registry, C.TypeTrojan, NewTrojanInbound)
}

func NewTrojanInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.TrojanInboundOptions) (adapter.Inbound, error) {

	in := &TrojanInbound{
		managedBase: newManagedBase(C.TypeTrojan, tag, ctx, logger),
		router:      router,
	}

	var err error
	if options.TLS != nil {
		in.tlsConfig, err = tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
	}

	// 不接 fallback：机场场景下 Trojan 的端口只服务订阅用户，
	// 回落到网站会让节点端多背一个 HTTP 服务的攻击面，得不偿失。
	in.service = trojan.NewService[int](
		adapter.NewUpstreamContextHandlerEx(in.newConnectionEx, in.newPacketConnectionEx), nil, logger)

	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			seed = append(seed, core.User{UUID: u.Password})
		}
		in.users.Add(seed)
	}
	if err := in.applyUsers(); err != nil {
		return nil, err
	}

	if options.Transport != nil {
		in.transport, err = v2ray.NewServerTransport(ctx, logger,
			common.PtrValueOrDefault(options.Transport), in.tlsConfig, (*trojanTransportHandler)(in))
		if err != nil {
			return nil, E.Cause(err, "创建传输层: ", options.Transport.Type)
		}
	}
	in.router, err = mux.NewRouterWithOptions(in.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}

	in.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: in,
	})
	return in, nil
}

func (h *TrojanInbound) applyUsers() error {
	list := h.users.Snapshot()
	idx := make([]int, len(list))
	pwds := make([]string, len(list))
	for i, u := range list {
		idx[i] = i
		pwds[i] = u.UUID
	}
	return h.service.UpdateUsers(idx, pwds)
}

func (h *TrojanInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *TrojanInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *TrojanInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return E.Cause(err, "启动 TLS")
		}
	}
	if h.transport != nil {
		return h.transport.Serve(h.listener.TCPListener())
	}
	return h.listener.Start()
}

func (h *TrojanInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig, h.transport)
}

func (h *TrojanInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	if h.tlsConfig != nil && h.transport == nil {
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

func (h *TrojanInbound) newConnectionEx(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	u, ok := h.users.ByIndex(userIndex)
	if !ok {
		h.logger.WarnContext(ctx, "用户索引 ", userIndex, " 已失效，本连接不计流量")
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = u.UUID
	h.routeConn(ctx, h.router, u, conn, metadata, onClose)
}

func (h *TrojanInbound) newPacketConnectionEx(ctx context.Context, conn N.PacketConn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrInvalid)
		return
	}
	u, ok := h.users.ByIndex(userIndex)
	if !ok {
		h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = u.UUID
	h.routePacketConn(ctx, h.router, u, conn, metadata, onClose)
}

type trojanTransportHandler TrojanInbound

func (t *trojanTransportHandler) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	(*TrojanInbound)(t).NewConnectionEx(ctx, conn, adapter.InboundContext{
		Source: source, Destination: destination,
	}, onClose)
}
