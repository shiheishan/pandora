// Package mieru 用 mieru 协议提供入站。
//
// mieru 与 sing-box 系的协议差别很大，不能挂在 sing-box 的 inbound 注册表上：
// 它自带监听器，把已认证的连接连同一个 socks5 请求一起交出来，转发由调用方做。
// 好处是集成面很窄；代价是路由、DNS、嗅探这些 sing-box 的能力用不上 ——
// 机场节点本来就是直出，这个代价可以接受。
//
// 这里绕过了 mieru 的 apis/server 门面，直接持有底层的 protocol.Mux。
// 原因只有一个：apis/server 没有暴露运行时改用户的能力，用它就只能
// 「停掉重建」，一个用户到期会踢掉这台节点上所有人。而底层的
// Mux.SetServerUsers 明确支持热更新 —— 已有连接不受影响，新连接自动用新表。
// 代价是要自己复制一遍 apis/server.Start 的组装逻辑（就是下面 Start 里那几行），
// 换来的是几百个在线用户不会因为别人的订阅到期而掉线。
//
// 许可证：mieru 为 GPL-3.0，与 sing-box 同级，不给 aegis-nodeagent
// 增加新的约束（本模块整体已按 GPL-3.0 分发）。
package mieru

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/apis/trafficpattern"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	mcommon "github.com/enfein/mieru/v3/pkg/common"
	"github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/enfein/mieru/v3/pkg/socks5"
	"google.golang.org/protobuf/proto"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/counter"
)

// Inbound 是一个 mieru 入站。
type Inbound struct {
	tag    string
	port   int
	proto  appctlpb.TransportProtocol
	log    *slog.Logger
	users  *counter.Table
	stats  *counter.Registry
	online *counter.OnlineTracker

	mu      sync.Mutex
	mux     *protocol.Mux
	cancel  context.CancelFunc
	started bool
}

func New(tag string, port int, transport string, log *slog.Logger) *Inbound {
	p := appctlpb.TransportProtocol_TCP
	if transport == "UDP" || transport == "udp" {
		p = appctlpb.TransportProtocol_UDP
	}
	return &Inbound{
		tag: tag, port: port, proto: p,
		log:    log.With("inbound", tag, "protocol", "mieru"),
		users:  counter.NewTable(),
		stats:  counter.NewRegistry(),
		online: counter.NewOnlineTracker(),
	}
}

func (h *Inbound) Traffic() []core.UserTraffic { return h.stats.Drain() }
func (h *Inbound) Online() map[int64][]string  { return h.online.Snapshot() }

// AddUsers / DelUsers 都是热更新，不重建监听、不断开已有连接。
func (h *Inbound) AddUsers(users []core.User) error {
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *Inbound) DelUsers(uuids []string) error {
	if gone := h.users.Del(uuids); len(gone) == 0 {
		return nil
	}
	return h.applyUsers()
}

// applyUsers 把当前用户表推给 mux。
//
// 注意语义：被删掉的用户不会被立刻踢下线 —— mux 只更新用户表，
// 已建立的 TCP 会话继续跑到自然结束。这是 mieru 的设计，也是合理的：
// 结算周期本来就有容差，为了立刻断掉一个人而抖动全部连接不划算。
func (h *Inbound) applyUsers() error {
	h.mu.Lock()
	if h.mux == nil {
		// 还没起来 —— 多半是首批用户刚到。mux.Start 拒绝空用户表，
		// 所以监听只能推迟到这一刻
		h.mu.Unlock()
		return h.Start()
	}
	h.mux.SetServerUsers(h.pbUsers())
	h.mu.Unlock()
	return nil
}

func (h *Inbound) pbUsers() map[string]*appctlpb.User {
	list := h.users.Snapshot()
	users := make([]*appctlpb.User, 0, len(list))
	for _, u := range list {
		// 用户名与密码同为 UUID：面板只下发一个身份字段
		users = append(users, &appctlpb.User{
			Name:     proto.String(u.UUID),
			Password: proto.String(u.UUID),
		})
	}
	return appctlcommon.UserListToMap(users)
}

// Start 组装并启动 mux。
//
// 这几行是 apis/server.Start 的等价实现 —— 唯一的区别是 mux 留在我们手里，
// 因而随后可以调 SetServerUsers。
//
// 用户表为空时不启动：mieru 拒绝没有用户的配置。启动因此是「懒」的，
// 由第一次用户同步触发；此后的增删就都是热更新了。
func (h *Inbound) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return nil
	}
	if h.users.Count() == 0 {
		// mux.Start 对空用户表直接报 "no user found"，
		// 这里静默等着 —— 首批用户同步过来时 applyUsers 会回调进来
		return nil
	}

	pattern, err := trafficpattern.NewConfig(nil)
	if err != nil {
		return fmt.Errorf("构建流量模式: %w", err)
	}
	endpoints, err := appctlcommon.PortBindingsToUnderlayProperties(
		[]*appctlpb.PortBinding{{
			Port:     proto.Int32(int32(h.port)),
			Protocol: h.proto.Enum(),
		}}, mcommon.DefaultMTU)
	if err != nil {
		return fmt.Errorf("解析端口绑定: %w", err)
	}

	mux := protocol.NewMux(false)
	mux.SetTrafficPattern(pattern).
		SetServerUsers(h.pbUsers()).
		SetEndpoints(endpoints)
	if err := mux.Start(); err != nil {
		return fmt.Errorf("启动 mieru: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.mux = mux
	h.cancel = cancel
	h.started = true

	go h.acceptLoop(ctx, mux)
	h.log.Info("mieru 入站已就绪", "port", h.port, "用户数", h.users.Count())
	return nil
}

func (h *Inbound) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	if h.mux != nil {
		h.mux.Close()
		h.mux = nil
	}
	h.started = false
	return nil
}

func (h *Inbound) acceptLoop(ctx context.Context, mux *protocol.Mux) {
	for {
		conn, err := mux.Accept()
		if err != nil {
			// mux 被关闭时 Accept 会立刻返回错误。用 ctx 区分
			// 「正常收摊」和「真出错」，否则每次停服都会刷一条误导性日志。
			select {
			case <-ctx.Done():
				return
			default:
			}
			h.log.Error("mieru Accept 失败", "err", err)
			return
		}
		go h.handle(conn)
	}
}

func (h *Inbound) handle(proxyConn net.Conn) {
	defer proxyConn.Close()

	userCtx, ok := proxyConn.(apicommon.UserContext)
	if !ok {
		h.log.Warn("mieru 连接没有用户上下文，已丢弃")
		return
	}

	// 顺序不能改：必须先读完 socks5 请求，再问 UserName()。
	//
	// mieru 的用户身份是在首个数据包解密成功时才确定的，
	// Accept 返回时连接上还一个字节都没读过 —— 此刻 UserName() 返回空串。
	// 上游示例看起来像是「拿到连接就有用户名」，那是因为它的 Accept
	// 内部已经替你读掉了 socks5 请求头。我们自己管 mux，这一步就得自己做，
	// 也就继承了这个顺序约束。
	//
	// 请求头必须限时读完，否则一条不说话的连接能一直占着 goroutine
	mcommon.SetReadTimeout(proxyConn, 10*time.Second)
	req := &model.Request{}
	err := req.ReadFromSocks5(proxyConn)
	mcommon.SetReadTimeout(proxyConn, 0)
	if err != nil {
		h.log.Debug("读取 socks5 请求失败", "err", err)
		return
	}

	u, found := h.users.ByUUID(userCtx.UserName())
	if !found {
		// 用户刚被移除，而这条连接是更新之前建立的
		h.log.Warn("mieru 用户已失效", "user", userCtx.UserName())
		return
	}

	// 设备数限制。mieru 不走 sing-box 的入站体系，这一步得自己做 ——
	// 漏掉的话它就成了绕过限制的那个协议。
	if !h.online.Admit(u.ID, proxyConn.RemoteAddr(), u.DeviceLimit) {
		h.log.Info("拒绝连接：已达设备数上限", "user", u.UUID, "limit", u.DeviceLimit)
		return
	}
	// mieru 的 UDP 走 packet-over-stream，与 TCP 共用这条 proxyConn，
	// 因此只要在这一层计数，两种流量就都算进去了
	counted := counter.NewConn(proxyConn, h.stats.Get(u.ID))

	switch req.Command {
	case constant.Socks5ConnectCmd:
		h.handleTCP(counted, req)
	case constant.Socks5UDPAssociateCmd:
		h.handleUDP(counted)
	default:
		h.log.Warn("不支持的 socks5 命令", "cmd", req.Command)
	}
}

func (h *Inbound) handleTCP(conn net.Conn, req *model.Request) {
	target, err := net.Dial("tcp", req.DstAddr.String())
	if err != nil {
		h.log.Debug("连接目标失败", "dst", req.DstAddr.String(), "err", err)
		return
	}
	defer target.Close()

	local := target.LocalAddr().(*net.TCPAddr)
	resp := &model.Response{
		Reply:    constant.Socks5ReplySuccess,
		BindAddr: model.AddrSpec{IP: local.IP, Port: local.Port},
	}
	if err := resp.WriteToSocks5(conn); err != nil {
		return
	}
	mcommon.BidiCopy(conn, target)
}

func (h *Inbound) handleUDP(conn net.Conn) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		h.log.Debug("监听 UDP 失败", "err", err)
		return
	}
	defer udpConn.Close()

	local := udpConn.LocalAddr().(*net.UDPAddr)
	resp := &model.Response{
		Reply: constant.Socks5ReplySuccess,
		// 回 0.0.0.0：客户端不该按这个地址回连，它只会往原连接发包
		BindAddr: model.AddrSpec{IP: net.IPv4zero, Port: local.Port},
	}
	if err := resp.WriteToSocks5(conn); err != nil {
		return
	}
	socks5.RunUDPAssociateLoop(udpConn, apicommon.NewPacketOverStreamTunnel(conn), &net.Resolver{})
}
