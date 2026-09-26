// Package sing 基于上游 sing-box 实现内核层。
//
// 核心手法：不 fork sing-box，而是用它的 inbound 注册表注册我们自己的实现。
//
// 为什么需要自己的 inbound：上游各协议的底层 service 都有 UpdateUsers，
// 但 inbound 只在构造时调用一次，没有对外暴露 —— 机场场景要求随时增删用户，
// 拿不到这个能力就只能重启入站，一个用户到期会让所有人断线。
// V2bX 的解法是 fork 整个 sing-box；我们只是重写 inbound 这一薄层，
// 上游升级时需要跟进的只有 inbound 接口，而不是整个仓库的 rebase。
//
// 许可证：sing-box 为 GPL-3.0，因此 pandora-native 整体按 GPL-3.0 分发。
// 面板本体是独立的 Go module，不链接 sing-box，不受此约束。
package sing

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/counter"
)

// 统计与用户表的实现在 core/counter 里，供所有内核共用。
// 这里用别名而不是直接改调用点，是为了让各协议文件保持简短的本地名字。
type (
	userTable     = counter.Table
	statsRegistry = counter.Registry
	onlineTracker = counter.OnlineTracker
)

var (
	newUserTable     = counter.NewTable
	newStatsRegistry = counter.NewRegistry
	newOnlineTracker = counter.NewOnlineTracker
)

// managedInbound 是所有支持用户热管理的入站必须满足的接口。
// 每加一个协议，只要实现这四个方法就能接入上层的同步逻辑。
type managedInbound interface {
	AddUsers(users []core.User) error
	UpsertUsers(users []core.User) error
	DelUsers(uuids []string) error
	Traffic() []core.UserTraffic
	Online() map[int64][]string
}

// managedBase 是各协议自定义 inbound 的共同部分。
type managedBase struct {
	inbound.Adapter
	ctx    context.Context
	logger logger.ContextLogger
	tag    string

	users  *userTable
	stats  *statsRegistry
	online *onlineTracker
}

func newManagedBase(protocol, tag string, ctx context.Context, lg logger.ContextLogger) managedBase {
	return managedBase{
		Adapter: inbound.NewAdapter(protocol, tag),
		ctx:     ctx,
		logger:  lg,
		tag:     tag,
		users:   newUserTable(),
		stats:   newStatsRegistry(),
		online:  newOnlineTracker(),
	}
}

func (b *managedBase) Traffic() []core.UserTraffic { return b.stats.Drain() }
func (b *managedBase) Online() map[int64][]string  { return b.online.Snapshot() }
func (b *managedBase) UserCount() int              { return b.users.Count() }

// ErrDeviceLimit 是超出设备数限制时给客户端的错误。
//
// 措辞刻意直白：用户看到「设备数超限」就知道去关掉别的设备，
// 而一个笼统的「连接失败」只会变成一张工单。
var ErrDeviceLimit = E.New("已达设备数上限")

// routeConn 把连接交给路由器，超出设备数限制时拒绝。
//
// 各协议的 newConnectionEx 都走这里，而不是各自调 wrapConn 再判断 ——
// 设备限制这种规则一旦有一个协议漏了，那个协议就成了绕过限制的后门，
// 而且完全看不出来。
func (b *managedBase) routeConn(ctx context.Context, router adapter.ConnectionRouterEx,
	u core.User, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	counted := b.wrapConn(u, conn, metadata.Source)
	if counted == nil {
		b.logger.InfoContext(ctx, "拒绝连接：用户 ", u.UUID, " 已达设备数上限 ", u.DeviceLimit)
		N.CloseOnHandshakeFailure(conn, onClose, ErrDeviceLimit)
		return
	}
	router.RouteConnectionEx(ctx, counted, metadata, onClose)
}

// routePacketConn 是 UDP 版本。
func (b *managedBase) routePacketConn(ctx context.Context, router adapter.ConnectionRouterEx,
	u core.User, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {

	counted := b.wrapPacketConn(u, conn, metadata.Source)
	if counted == nil {
		N.CloseOnHandshakeFailure(conn, onClose, ErrDeviceLimit)
		return
	}
	router.RoutePacketConnectionEx(ctx, counted, metadata, onClose)
}

// routeConnErr 供返回 error 的调用点使用（Shadowsocks / SOCKS 的老式 handler）。
func (b *managedBase) routeConnErr(ctx context.Context, router adapter.ConnectionRouterEx,
	u core.User, conn net.Conn, metadata adapter.InboundContext) error {

	counted := b.wrapConn(u, conn, metadata.Source)
	if counted == nil {
		return ErrDeviceLimit
	}
	return router.RouteConnection(ctx, counted, metadata)
}

func (b *managedBase) routePacketConnErr(ctx context.Context, router adapter.ConnectionRouterEx,
	u core.User, conn N.PacketConn, metadata adapter.InboundContext) error {

	counted := b.wrapPacketConn(u, conn, metadata.Source)
	if counted == nil {
		return ErrDeviceLimit
	}
	return router.RoutePacketConnection(ctx, counted, metadata)
}

// wrapConn 把连接包成计数连接并登记在线 IP。
// 返回 nil 表示该用户已达设备数上限，这条连接不该被建立。
func (b *managedBase) wrapConn(u core.User, conn net.Conn, source M.Socksaddr) net.Conn {
	if !b.online.Admit(u.ID, source.TCPAddr(), u.DeviceLimit) {
		return nil
	}
	return counter.NewConn(conn, b.stats.Get(u.ID))
}

func (b *managedBase) wrapPacketConn(u core.User, conn N.PacketConn, source M.Socksaddr) N.PacketConn {
	if !b.online.Admit(u.ID, source.UDPAddr(), u.DeviceLimit) {
		return nil
	}
	return counter.NewPacketConn(conn, b.stats.Get(u.ID))
}
