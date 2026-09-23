package sing

import (
	"context"
	"net"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

// VLESSInbound 是带用户热管理与流量计数的 VLESS 入站。
//
// 构造流程与上游 sing-box 基本一致（GPL-3.0，本模块亦按 GPL-3.0 分发），
// 差别只有三处，也正是自研节点端存在的意义：
//  1. 持有 service 引用，可随时 UpdateUsers 而不必重启入站
//  2. 连接建立后包一层计数器，按用户统计流量
//  3. 记录来源 IP，供设备数限制使用
type VLESSInbound struct {
	managedBase
	router    adapter.ConnectionRouterEx
	listener  *listener.Listener
	service   *vless.Service[int]
	tlsConfig tls.ServerConfig
	transport adapter.V2RayServerTransport
}

var (
	_ adapter.Inbound              = (*VLESSInbound)(nil)
	_ adapter.TCPInjectableInbound = (*VLESSInbound)(nil)
	_ managedInbound               = (*VLESSInbound)(nil)
)

// RegisterVLESS 用我们的实现覆盖注册表里的 vless 类型。
// 必须在 include.InboundRegistry() 之后调用，才能盖住上游的注册。
func RegisterVLESS(registry *inbound.Registry) {
	inbound.Register[option.VLESSInboundOptions](registry, C.TypeVLESS, NewVLESSInbound)
}

func NewVLESSInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger,
	tag string, options option.VLESSInboundOptions) (adapter.Inbound, error) {

	in := &VLESSInbound{
		managedBase: newManagedBase(C.TypeVLESS, tag, ctx, logger),
		router:      uot.NewRouter(router, logger),
	}

	// 面板下发的用户由 AddUsers 注入，配置里的 users 通常为空。
	// 仍然接住它，是为了支持「不接面板、纯本地配置」的调试场景。
	if len(options.Users) > 0 {
		seed := make([]core.User, 0, len(options.Users))
		for _, u := range options.Users {
			seed = append(seed, core.User{UUID: u.UUID})
		}
		in.users.Add(seed)
	}

	in.service = vless.NewService[int](logger,
		adapter.NewUpstreamContextHandlerEx(in.newConnectionEx, in.newPacketConnectionEx))
	in.applyUsers()

	var err error
	if options.TLS != nil {
		in.tlsConfig, err = tls.NewServerWithOptions(tls.ServerOptions{
			Context: ctx,
			Logger:  logger,
			Options: common.PtrValueOrDefault(options.TLS),
		})
		if err != nil {
			return nil, E.Cause(err, "创建 TLS 服务端配置")
		}
	}
	if options.Transport != nil {
		in.transport, err = v2ray.NewServerTransport(ctx, logger,
			common.PtrValueOrDefault(options.Transport), in.tlsConfig,
			(*vlessTransportHandler)(in))
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

//------------------------------------------------------------------------------
// 用户管理
//------------------------------------------------------------------------------

// applyUsers 把当前用户表推给 service。
//
// UpdateUsers 是全量覆盖语义，不是增量 —— 每次都要传完整列表。
// 用户表的顺序必须稳定，否则索引与身份的对应关系会错乱（见 userTable 注释）。
func (h *VLESSInbound) applyUsers() {
	list := h.users.Snapshot()
	idx := make([]int, len(list))
	uuids := make([]string, len(list))
	flows := make([]string, len(list))
	for i, u := range list {
		idx[i] = i
		uuids[i] = u.UUID
		// flow 目前统一留空。xtls-rprx-vision 需要与客户端协商，
		// 由面板的 protocol_config 控制整个入站，而不是逐用户设置。
		flows[i] = ""
	}
	h.service.UpdateUsers(idx, uuids, flows)
}

func (h *VLESSInbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

func (h *VLESSInbound) DelUsers(uuids []string) error {
	gone := h.users.Del(uuids)
	if len(gone) == 0 {
		return nil
	}
	h.applyUsers()
	return nil
}

//------------------------------------------------------------------------------
// 生命周期
//------------------------------------------------------------------------------

func (h *VLESSInbound) Start(stage adapter.StartStage) error {
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

func (h *VLESSInbound) Close() error {
	return common.Close(h.listener, h.tlsConfig, h.transport)
}

//------------------------------------------------------------------------------
// 连接处理
//------------------------------------------------------------------------------

func (h *VLESSInbound) NewConnectionEx(ctx context.Context, conn net.Conn,
	metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	// 传输层存在时 TLS 由它接管；否则在这里握手
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

// newConnectionEx 在协议解析完成、用户身份已确定后被调用。
// 这里是唯一能同时拿到「用户是谁」和「原始连接」的位置，
// 流量计数与在线登记都必须在此完成。
func (h *VLESSInbound) newConnectionEx(ctx context.Context, conn net.Conn,
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
		// 索引越界只可能发生在用户表刚被更新、而这条连接还带着旧索引时。
		// 放行但不计费远比断开好：用户体验不该为一次内部竞态买单，
		// 少记的这点流量在下一次统计窗口就会被正常计入。
		h.logger.WarnContext(ctx, "用户索引 ", userIndex, " 已失效，本连接不计流量")
		h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
		return
	}

	metadata.User = u.UUID
	h.routeConn(ctx, h.router, u, conn, metadata, onClose)
}

func (h *VLESSInbound) newPacketConnectionEx(ctx context.Context, conn N.PacketConn,
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

//------------------------------------------------------------------------------

// vlessTransportHandler 把传输层（ws / grpc / httpupgrade 等）收到的连接
// 交回给主入站处理。类型转换而非包装，避免多一层间接调用。
type vlessTransportHandler VLESSInbound

func (t *vlessTransportHandler) NewConnectionEx(ctx context.Context, conn net.Conn,
	source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	(*VLESSInbound)(t).NewConnectionEx(ctx, conn, adapter.InboundContext{
		Source: source, Destination: destination,
	}, onClose)
}
