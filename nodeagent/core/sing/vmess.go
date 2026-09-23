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
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

// VMessInbound 是带用户热管理与流量计数的 VMess 入站。
type VMessInbound struct {
	managedBase
	router    adapter.ConnectionRouterEx
	listener  *listener.Listener
	service   *vmess.Service[int]
	tlsConfig tls.ServerConfig
	transport adapter.V2RayServerTransport
}

var (
	_ adapter.Inbound              = (*VMessInbound)(nil)
	_ adapter.TCPInjectableInbound = (*VMessInbound)(nil)
	_ managedInbound               = (*VMessInbound)(nil)
)

func RegisterVMess(registry *inbound.Registry) {
	inbound.Register[option.VMessInboundOptions](registry, C.TypeVMess, NewVMessInbound)
}

func NewVMessInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.VMessInboundOptions) (adapter.Inbound, error) {

	in := &VMessInbound{
		managedBase: newManagedBase(C.TypeVMess, tag, ctx, logger),
		router:      router,
	}

	var err error
	in.router, err = mux.NewRouterWithOptions(in.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}

	var svcOpts []vmess.ServiceOption
	if options.Transport != nil && options.Transport.Type != "" {
		// 传输层已经把流量伪装好了，头部保护是多余的开销
		svcOpts = append(svcOpts, vmess.ServiceWithDisableHeaderProtection())
	}
	in.service = vmess.NewService[int](
		adapter.NewUpstreamContextHandlerEx(in.newConnectionEx, in.newPacketConnectionEx), svcOpts...)

	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			seed = append(seed, core.User{UUID: u.UUID})
		}
		in.users.Add(seed)
	}
	if err := in.applyUsers(); err != nil {
		return nil, err
	}

	if options.TLS != nil {
		in.tlsConfig, err = tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
	}
	if options.Transport != nil {
		in.transport, err = v2ray.NewServerTransport(ctx, logger,
			common.PtrValueOrDefault(options.Transport), in.tlsConfig, (*vmessTransportHandler)(in))
		if err != nil {
			return nil, E.Cause(err, "创建传输层: ", options.Transport.Type)
		}
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

// applyUsers 全量推送用户表。
// alterId 一律为 0：VMessAEAD 之后 alterId 已被上游废弃，
// 非零值反而会退回到有已知漏洞的 MD5 认证路径。
func (h *VMessInbound) applyUsers() error {
	list := h.users.Snapshot()
	idx := make([]int, len(list))
	uuids := make([]string, len(list))
	alter := make([]int, len(list))
	for i, u := range list {
		idx[i] = i
		uuids[i] = u.UUID
	}
	return h.service.UpdateUsers(idx, uuids, alter)
}

func (h *VMessInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *VMessInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *VMessInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if err := h.service.Start(); err != nil {
		return err
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

func (h *VMessInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig, h.transport, h.service)
}

func (h *VMessInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
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

func (h *VMessInbound) newConnectionEx(ctx context.Context, conn net.Conn,
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

func (h *VMessInbound) newPacketConnectionEx(ctx context.Context, conn N.PacketConn,
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

type vmessTransportHandler VMessInbound

func (t *vmessTransportHandler) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	(*VMessInbound)(t).NewConnectionEx(ctx, conn, adapter.InboundContext{
		Source: source, Destination: destination,
	}, onClose)
}
