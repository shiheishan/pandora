package sing

// QUIC 系协议：TUIC 与 Hysteria（v1）。
//
// 这两者与 Hysteria2 同构 —— 都跑在 UDP 上、没有 TCP listener、TLS 由
// QUIC 内部完成、service 的 Handler 直接给出已认证的连接。差异只在
// 用户凭据的形态：TUIC 要 UUID+密码，Hysteria v1 只要一个 auth 字符串。

import (
	"context"
	"net"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

//------------------------------------------------------------------------------
// TUIC
//------------------------------------------------------------------------------

type TUICInbound struct {
	managedBase
	router    adapter.ConnectionRouterEx
	listener  *listener.Listener
	tlsConfig tls.ServerConfig
	service   *tuic.Service[int]
}

var (
	_ adapter.Inbound = (*TUICInbound)(nil)
	_ managedInbound  = (*TUICInbound)(nil)
)

func RegisterTUIC(registry *inbound.Registry) {
	inbound.Register[option.TUICInboundOptions](registry, C.TypeTUIC, NewTUICInbound)
}

func NewTUICInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.TUICInboundOptions) (adapter.Inbound, error) {

	options.UDPFragmentDefault = true
	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}

	in := &TUICInbound{
		managedBase: newManagedBase(C.TypeTUIC, tag, ctx, logger),
		router:      uot.NewRouter(router, logger),
		tlsConfig:   tlsConfig,
		listener: listener.New(listener.Options{
			Context: ctx, Logger: logger, Listen: options.ListenOptions,
		}),
	}

	udpTimeout := C.UDPTimeout
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	}
	in.service, err = tuic.NewService[int](tuic.ServiceOptions{
		Context:           ctx,
		Logger:            logger,
		TLSConfig:         tlsConfig,
		CongestionControl: options.CongestionControl,
		AuthTimeout:       time.Duration(options.AuthTimeout),
		ZeroRTTHandshake:  options.ZeroRTTHandshake,
		Heartbeat:         time.Duration(options.Heartbeat),
		UDPTimeout:        udpTimeout,
		Handler:           in,
	})
	if err != nil {
		return nil, err
	}

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
	return in, nil
}

// applyUsers 推送用户表。
//
// TUIC 的密码沿用 UUID 本身：面板只下发一个身份字段，
// 再凭空派生一个密码只会让订阅链接与节点端各自维护一份规则，
// 迟早对不上。与 Xboard 生态的既有做法也一致。
func (h *TUICInbound) applyUsers() error {
	list := h.users.Snapshot()
	idx := make([]int, 0, len(list))
	uuids := make([][16]byte, 0, len(list))
	pwds := make([]string, 0, len(list))
	for i, u := range list {
		parsed, err := uuid.FromString(u.UUID)
		if err != nil {
			// 单个用户的 UUID 非法不该拖垮整个入站 —— 跳过它，
			// 其余用户照常服务。注意跳过后索引会与 snapshot 错位，
			// 因此 idx 用的是 append 前的位置 i（userTable 的下标），
			// byIndex 查得到的仍是同一个人。
			h.logger.Warn("TUIC 用户 ", u.UUID, " 的 UUID 非法，已跳过")
			continue
		}
		idx = append(idx, i)
		uuids = append(uuids, parsed)
		pwds = append(pwds, u.UUID)
	}
	h.service.UpdateUsers(idx, uuids, pwds)
	return nil
}

func (h *TUICInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *TUICInbound) UpsertUsers(users []core.User) error {
	if updated := h.users.Upsert(users); len(updated) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *TUICInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *TUICInbound) Start(stage adapter.StartStage) error {
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

func (h *TUICInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig, common.PtrOrNil(h.service))
}

func (h *TUICInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	quicRouteConn(ctx, &h.managedBase, h.router, conn, source, destination, onClose)
}

func (h *TUICInbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	quicRoutePacketConn(ctx, &h.managedBase, h.router, conn, source, destination, onClose)
}

//------------------------------------------------------------------------------
// Hysteria v1
//------------------------------------------------------------------------------

type HysteriaInbound struct {
	managedBase
	router    adapter.ConnectionRouterEx
	listener  *listener.Listener
	tlsConfig tls.ServerConfig
	service   *hysteria.Service[int]
}

var (
	_ adapter.Inbound = (*HysteriaInbound)(nil)
	_ managedInbound  = (*HysteriaInbound)(nil)
)

func RegisterHysteria(registry *inbound.Registry) {
	inbound.Register[option.HysteriaInboundOptions](registry, C.TypeHysteria, NewHysteriaInbound)
}

func NewHysteriaInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.HysteriaInboundOptions) (adapter.Inbound, error) {

	if options.TLS == nil || !options.TLS.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
	if err != nil {
		return nil, err
	}

	in := &HysteriaInbound{
		managedBase: newManagedBase(C.TypeHysteria, tag, ctx, logger),
		router:      router,
		tlsConfig:   tlsConfig,
		listener: listener.New(listener.Options{
			Context: ctx, Logger: logger, Listen: options.ListenOptions,
		}),
	}

	sendBps := options.Up.Value()
	if sendBps == 0 {
		sendBps = uint64(options.UpMbps) * hysteria.MbpsToBps
	}
	recvBps := options.Down.Value()
	if recvBps == 0 {
		recvBps = uint64(options.DownMbps) * hysteria.MbpsToBps
	}
	udpTimeout := C.UDPTimeout
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	}

	in.service, err = hysteria.NewService[int](hysteria.ServiceOptions{
		Context:             ctx,
		Logger:              logger,
		SendBPS:             sendBps,
		ReceiveBPS:          recvBps,
		XPlusPassword:       options.Obfs,
		TLSConfig:           tlsConfig,
		UDPTimeout:          udpTimeout,
		Handler:             in,
		ConnReceiveWindow:   options.ReceiveWindowConn,
		StreamReceiveWindow: options.ReceiveWindowClient,
		MaxIncomingStreams:  int64(options.MaxConnClient),
		DisableMTUDiscovery: options.DisableMTUDiscovery,
	})
	if err != nil {
		return nil, err
	}

	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			pwd := u.AuthString
			if pwd == "" {
				pwd = string(u.Auth)
			}
			seed = append(seed, core.User{UUID: pwd})
		}
		in.users.Add(seed)
	}
	in.applyUsers()
	return in, nil
}

func (h *HysteriaInbound) applyUsers() {
	list := h.users.Snapshot()
	idx := make([]int, len(list))
	pwds := make([]string, len(list))
	for i, u := range list {
		idx[i] = i
		pwds[i] = u.UUID
	}
	h.service.UpdateUsers(idx, pwds)
}

func (h *HysteriaInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *HysteriaInbound) UpsertUsers(users []core.User) error {
	if updated := h.users.Upsert(users); len(updated) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *HysteriaInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *HysteriaInbound) Start(stage adapter.StartStage) error {
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

func (h *HysteriaInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig, common.PtrOrNil(h.service))
}

func (h *HysteriaInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	quicRouteConn(ctx, &h.managedBase, h.router, conn, source, destination, onClose)
}

func (h *HysteriaInbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	quicRoutePacketConn(ctx, &h.managedBase, h.router, conn, source, destination, onClose)
}

//------------------------------------------------------------------------------
// 共用的路由入口
//------------------------------------------------------------------------------

// quicRouteConn 是 QUIC 系入站的通用连接处理。
// 三个协议（TUIC / Hysteria / Hysteria2）在这一步的逻辑完全一致，
// 抽出来是为了让「认证 → 计数 → 路由」这条链只有一份实现。
func quicRouteConn(ctx context.Context, base *managedBase, router adapter.ConnectionRouterEx,
	conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {

	ctx = log.ContextWithNewID(ctx)
	metadata := adapter.InboundContext{
		Inbound: base.Tag(), InboundType: base.Type(),
		Source: source, Destination: destination,
	}
	if u, ok := quicUser(ctx, base); ok {
		metadata.User = u.UUID
		base.routeConn(ctx, router, u, conn, metadata, onClose)
		return
	}
	router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func quicRoutePacketConn(ctx context.Context, base *managedBase, router adapter.ConnectionRouterEx,
	conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {

	ctx = log.ContextWithNewID(ctx)
	metadata := adapter.InboundContext{
		Inbound: base.Tag(), InboundType: base.Type(),
		Source: source, Destination: destination,
	}
	if u, ok := quicUser(ctx, base); ok {
		metadata.User = u.UUID
		base.routePacketConn(ctx, router, u, conn, metadata, onClose)
		return
	}
	router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}

func quicUser(ctx context.Context, base *managedBase) (core.User, bool) {
	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		return core.User{}, false
	}
	return base.users.ByIndex(userIndex)
}
