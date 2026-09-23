package sing

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

// Hysteria2Inbound 是带用户热管理与流量计数的 Hysteria2 入站。
//
// Hysteria2 跑在 QUIC 上，没有 TCP listener —— 只有一个 UDP socket，
// 连接是 QUIC 流。因此这里没有 TLS 握手代码：TLS 是 QUIC 的一部分，
// 由 service 内部完成。
type Hysteria2Inbound struct {
	managedBase
	router    adapter.Router
	listener  *listener.Listener
	tlsConfig tls.ServerConfig
	service   *hysteria2.Service[int]
}

var (
	_ adapter.Inbound = (*Hysteria2Inbound)(nil)
	_ managedInbound  = (*Hysteria2Inbound)(nil)
)

func RegisterHysteria2(registry *inbound.Registry) {
	inbound.Register[option.Hysteria2InboundOptions](registry, C.TypeHysteria2, NewHysteria2Inbound)
}

func NewHysteria2Inbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.Hysteria2InboundOptions) (adapter.Inbound, error) {

	options.UDPFragmentDefault = true
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}

	var salamander string
	if options.Obfs != nil {
		if options.Obfs.Password == "" {
			return nil, E.New("缺少 obfs 密码")
		}
		if options.Obfs.Type != hysteria2.ObfsTypeSalamander {
			return nil, E.New("未知的 obfs 类型: ", options.Obfs.Type)
		}
		salamander = options.Obfs.Password
	}

	in := &Hysteria2Inbound{
		managedBase: newManagedBase(C.TypeHysteria2, tag, ctx, logger),
		router:      router,
		tlsConfig:   tlsConfig,
		listener: listener.New(listener.Options{
			Context: ctx,
			Logger:  logger,
			Listen:  options.ListenOptions,
		}),
	}

	udpTimeout := C.UDPTimeout
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	}

	in.service, err = hysteria2.NewService[int](hysteria2.ServiceOptions{
		Context:               ctx,
		Logger:                logger,
		BrutalDebug:           options.BrutalDebug,
		SendBPS:               uint64(options.UpMbps * hysteria.MbpsToBps),
		ReceiveBPS:            uint64(options.DownMbps * hysteria.MbpsToBps),
		SalamanderPassword:    salamander,
		TLSConfig:             tlsConfig,
		IgnoreClientBandwidth: options.IgnoreClientBandwidth,
		UDPTimeout:            udpTimeout,
		Handler:               in,
	})
	if err != nil {
		return nil, err
	}

	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			seed = append(seed, core.User{UUID: u.Password})
		}
		in.users.Add(seed)
	}
	in.applyUsers()
	return in, nil
}

func (h *Hysteria2Inbound) applyUsers() {
	list := h.users.Snapshot()
	idx := make([]int, len(list))
	pwds := make([]string, len(list))
	for i, u := range list {
		idx[i] = i
		pwds[i] = u.UUID
	}
	h.service.UpdateUsers(idx, pwds)
}

func (h *Hysteria2Inbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *Hysteria2Inbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *Hysteria2Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if err := h.tlsConfig.Start(); err != nil {
		return err
	}
	packetConn, err := h.listener.ListenUDP()
	if err != nil {
		return err
	}
	return h.service.Start(packetConn)
}

func (h *Hysteria2Inbound) Close() error {
	return common.Close(h.listener, h.tlsConfig, common.PtrOrNil(h.service))
}

func (h *Hysteria2Inbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {

	ctx = log.ContextWithNewID(ctx)
	metadata := adapter.InboundContext{
		Inbound: h.Tag(), InboundType: h.Type(),
		Source: source, Destination: destination,
	}
	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	u, ok := h.users.ByIndex(userIndex)
	if !ok {
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}
	metadata.User = u.UUID
	h.routeConn(ctx, h.router, u, conn, metadata, onClose)
}

func (h *Hysteria2Inbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {

	ctx = log.ContextWithNewID(ctx)
	metadata := adapter.InboundContext{
		Inbound: h.Tag(), InboundType: h.Type(),
		Source: source, Destination: destination,
	}
	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		h.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
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
