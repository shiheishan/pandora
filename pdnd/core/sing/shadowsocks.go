package sing

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"

	"github.com/aegispanel/nodeagent/core"
)

// ShadowsocksInbound 是带用户热管理与流量计数的 Shadowsocks 多用户入站。
//
// 与其它协议的区别：Shadowsocks 的用户凭据是 PSK（密码），
// 且 2022 系列方法要求服务端也有一个自己的 Password（iPSK 派生的根密钥）。
// 面板下发的用户密码放在 core.User.UUID 里，节点级的 Password 来自入站配置。
type ShadowsocksInbound struct {
	managedBase
	router   adapter.ConnectionRouterEx
	listener *listener.Listener
	service  shadowsocks.MultiService[int]
}

var (
	_ adapter.Inbound              = (*ShadowsocksInbound)(nil)
	_ adapter.TCPInjectableInbound = (*ShadowsocksInbound)(nil)
	_ managedInbound               = (*ShadowsocksInbound)(nil)
)

func RegisterShadowsocks(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocks, NewShadowsocksInbound)
}

func NewShadowsocksInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {

	in := &ShadowsocksInbound{
		managedBase: newManagedBase(C.TypeShadowsocks, tag, ctx, logger),
		router:      uot.NewRouter(router, logger),
	}

	var err error
	in.router, err = mux.NewRouterWithOptions(in.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}

	udpTimeout := C.UDPTimeout
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	}

	handler := adapter.NewUpstreamHandler(adapter.InboundContext{},
		in.newConnection, in.newPacketConnection, nil)

	switch {
	case common.Contains(shadowaead_2022.List, options.Method):
		in.service, err = shadowaead_2022.NewMultiServiceWithPassword[int](
			options.Method, options.Password, int64(udpTimeout.Seconds()),
			handler, ntp.TimeFuncFromContext(ctx))
	case common.Contains(shadowaead.List, options.Method):
		in.service, err = shadowaead.NewMultiService[int](
			options.Method, int64(udpTimeout.Seconds()), handler)
	default:
		// 单用户的老式 stream 加密不在支持范围内：机场要按用户计费，
		// 而 stream 方法没有多用户能力，一个端口只能服务一个人
		return nil, E.New("不支持的加密方式: ", options.Method)
	}
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
	if err := in.applyUsers(); err != nil {
		return nil, err
	}

	in.listener = listener.New(listener.Options{
		Context:                  ctx,
		Logger:                   logger,
		Network:                  options.Network.Build(),
		Listen:                   options.ListenOptions,
		ConnectionHandler:        in,
		PacketHandler:            in,
		ThreadUnsafePacketWriter: true,
	})
	return in, nil
}

func (h *ShadowsocksInbound) applyUsers() error {
	list := h.users.Snapshot()
	idx := make([]int, len(list))
	psks := make([]string, len(list))
	for i, u := range list {
		idx[i] = i
		psks[i] = u.UUID
	}
	return h.service.UpdateUsersWithPasswords(idx, psks)
}

func (h *ShadowsocksInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *ShadowsocksInbound) UpsertUsers(users []core.User) error {
	if updated := h.users.Upsert(users); len(updated) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *ShadowsocksInbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *ShadowsocksInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return h.listener.Start()
}

func (h *ShadowsocksInbound) Close() error {
	return h.listener.Close()
}

func (h *ShadowsocksInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	err := h.service.NewConnection(ctx, conn, adapter.UpstreamMetadata(metadata))
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil && !E.IsClosedOrCanceled(err) {
		h.logger.ErrorContext(ctx, E.Cause(err, "处理来自 ", metadata.Source, " 的连接"))
	}
}

func (h *ShadowsocksInbound) NewPacketEx(buffer *buf.Buffer, source M.Socksaddr) {
	err := h.service.NewPacket(h.ctx, &ssPacketWriter{h.listener.PacketWriter()},
		buffer, M.Metadata{Source: source})
	if err != nil {
		h.logger.Error(E.Cause(err, "处理来自 ", source, " 的报文"))
	}
}

func (h *ShadowsocksInbound) newConnection(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext) error {

	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		return os.ErrInvalid
	}
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()

	u, ok := h.users.ByIndex(userIndex)
	if !ok {
		h.logger.WarnContext(ctx, "用户索引 ", userIndex, " 已失效，本连接不计流量")
		return h.router.RouteConnection(ctx, conn, metadata)
	}
	metadata.User = u.UUID
	return h.routeConnErr(ctx, h.router, u, conn, metadata)
}

func (h *ShadowsocksInbound) newPacketConnection(ctx context.Context, conn N.PacketConn,
	metadata adapter.InboundContext) error {

	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded {
		return os.ErrInvalid
	}
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()

	u, ok := h.users.ByIndex(userIndex)
	if !ok {
		return h.router.RoutePacketConnection(ctx, conn, metadata)
	}
	metadata.User = u.UUID
	return h.routePacketConnErr(ctx, h.router, u, conn, metadata)
}

// ssPacketWriter 把 listener 的写侧包成一个 N.PacketConn。
//
// Shadowsocks 的 UDP 是无连接的：每个报文独立解密后才知道属于哪个用户，
// service.NewPacket 需要一个「回信通道」而不是真正的连接。
// 读侧永远不会被调用 —— 报文由 listener 直接投递到 NewPacketEx。
type ssPacketWriter struct {
	N.PacketWriter
}

func (w *ssPacketWriter) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, os.ErrInvalid
}

func (w *ssPacketWriter) Close() error                       { return nil }
func (w *ssPacketWriter) LocalAddr() net.Addr                { return nil }
func (w *ssPacketWriter) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (w *ssPacketWriter) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (w *ssPacketWriter) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
