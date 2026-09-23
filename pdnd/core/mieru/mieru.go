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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
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
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// Transport is the narrow egress contract used when Mieru is hosted by
// Pandora NativeCore. The protocol decoder remains Mieru-owned; routing and
// accounting stay in the NativeCore DataPlane instead of calling net.Dial.
type Transport interface {
	DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error)
	ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error)
}

// Inbound 是一个 mieru 入站。
type Inbound struct {
	tag    string
	port   int
	proto  appctlpb.TransportProtocol
	log    *slog.Logger
	users  *counter.Table
	stats  *counter.Registry
	online *counter.OnlineTracker
	// limiters 按用户限速。Mieru 的双向拷贝在上游库内部，插不进搬运层，
	// 只能把连接包一层——和流量计数走的是同一条路子。
	limiters core.SpeedLimiters

	mu        sync.Mutex
	mux       *protocol.Mux
	transport Transport
	cancel    context.CancelFunc
	started   bool
}

func (h *Inbound) SetTransport(transport Transport) {
	h.mu.Lock()
	h.transport = transport
	h.mu.Unlock()
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
	// Table.Add historically ignored empty identities. Reject the whole batch
	// first so a panel sync cannot report success while silently publishing only
	// a prefix of its requested users.
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" {
			return fmt.Errorf("mieru user %d uuid is required", user.ID)
		}
	}
	if added := h.users.Add(users); len(added) == 0 {
		return nil
	}
	return h.applyUsers()
}

func (h *Inbound) UpsertUsers(users []core.User) error {
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" {
			return fmt.Errorf("mieru user %d uuid is required", user.ID)
		}
	}
	if updated := h.users.Upsert(users); len(updated) == 0 {
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
		go h.handle(ctx, conn)
	}
}

func (h *Inbound) handle(ctx context.Context, proxyConn net.Conn) {
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
	// 限速套在计数之外：同一用户的所有连接、上下行共用一个令牌桶。
	// UDP 走 packet-over-stream，与 TCP 共用这条连接，一并限住。

	// 限速套在计数之外：同一用户的所有连接、上下行共用一个令牌桶。
	// UDP 走 packet-over-stream，与 TCP 共用这条连接，一并限住。
	limited := core.NewSpeedLimitedConn(counted, h.limiters.For(u))

	switch req.Command {
	case constant.Socks5ConnectCmd:
		h.handleTCP(ctx, limited, req)
	case constant.Socks5UDPAssociateCmd:
		h.handleUDP(ctx, limited)
	default:
		h.log.Warn("不支持的 socks5 命令", "cmd", req.Command)
	}
}

func (h *Inbound) handleTCP(ctx context.Context, conn net.Conn, req *model.Request) {
	destination := M.ParseSocksaddr(req.DstAddr.String())
	var target net.Conn
	var err error
	h.mu.Lock()
	transport := h.transport
	h.mu.Unlock()
	if transport != nil {
		var sourceIP netip.Addr
		var sourcePort uint16
		if host, port, splitErr := net.SplitHostPort(conn.RemoteAddr().String()); splitErr == nil {
			sourceIP, _ = netip.ParseAddr(host)
			if parsed, parseErr := strconv.ParseUint(port, 10, 16); parseErr == nil {
				sourcePort = uint16(parsed)
			}
		}
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "tcp", Protocol: "mieru", SourceIP: sourceIP, SourcePort: sourcePort}
		target, err = transport.DialTCP(ctx, meta, destination)
	} else {
		target, err = net.Dial("tcp", req.DstAddr.String())
	}
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

func (h *Inbound) handleUDP(ctx context.Context, conn net.Conn) {
	h.mu.Lock()
	transport := h.transport
	h.mu.Unlock()
	if transport == nil {
		// Standalone compatibility mode retains the upstream implementation.
		udpConn, err := net.ListenUDP("udp", nil)
		if err != nil {
			h.log.Debug("监听 UDP 失败", "err", err)
			return
		}
		defer udpConn.Close()
		local := udpConn.LocalAddr().(*net.UDPAddr)
		resp := &model.Response{
			Reply:    constant.Socks5ReplySuccess,
			BindAddr: model.AddrSpec{IP: net.IPv4zero, Port: local.Port},
		}
		if err := resp.WriteToSocks5(conn); err != nil {
			return
		}
		_ = socks5.RunUDPAssociateLoop(udpConn, apicommon.NewPacketOverStreamTunnel(conn), &net.Resolver{})
		return
	}

	// NativeCore mode uses a packet-over-stream loop whose destination sockets
	// are created by the Pandora DataPlane. This keeps Mieru's wire framing while
	// routing every UDP destination through the same policy/accounting boundary.
	resp := &model.Response{
		Reply: constant.Socks5ReplySuccess,
		// UDP is carried on the authenticated stream; clients send packets back
		// through that stream rather than dialing this bind address.
		BindAddr: model.AddrSpec{IP: net.IPv4zero, Port: 0},
	}
	if err := resp.WriteToSocks5(conn); err != nil {
		return
	}
	if err := h.runNativeUDPLoop(ctx, conn, transport); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		h.log.Debug("mieru 原生 UDP 会话结束", "err", err)
	}
}

type mieruUDPInbound struct {
	payload []byte
	dst     model.AddrSpec
	header  []byte
}

type mieruUDPOutbound struct {
	payload []byte
	addr    net.Addr
}

type mieruUDPRoute struct {
	conn   net.PacketConn
	cancel context.CancelFunc
}

// runNativeUDPLoop is deliberately kept local to the Mieru adapter instead of
// calling the upstream *net.UDPConn-only helper. That helper would force a
// direct socket and bypass NativeCore's route selection and accounting.
func (h *Inbound) runNativeUDPLoop(ctx context.Context, streamConn net.Conn, transport Transport) error {
	tunnel := apicommon.NewPacketOverStreamTunnel(streamConn)
	inCh := make(chan mieruUDPInbound)
	outCh := make(chan mieruUDPOutbound, 16)
	errCh := make(chan error, 2)
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		buf := make([]byte, 1<<16)
		for {
			n, err := tunnel.Read(buf)
			if err != nil {
				errCh <- err
				return
			}
			dst, payload, header, err := parseMieruUDPPacket(buf[:n])
			if err != nil {
				errCh <- err
				return
			}
			item := mieruUDPInbound{payload: append([]byte(nil), payload...), dst: dst, header: header}
			select {
			case inCh <- item:
			case <-loopCtx.Done():
				return
			}
		}
	}()

	var routes sync.Map // map[string]*mieruUDPRoute
	defer func() {
		routes.Range(func(_, value any) bool {
			r := value.(*mieruUDPRoute)
			r.cancel()
			_ = r.conn.Close()
			return true
		})
	}()
	for {
		select {
		case err := <-errCh:
			return err
		case item := <-inCh:
			destination, err := resolveMieruUDPAddr(loopCtx, item.dst)
			if err != nil {
				return err
			}
			key := destination.String()
			value, loaded := routes.Load(key)
			var r *mieruUDPRoute
			if loaded {
				r = value.(*mieruUDPRoute)
			} else {
				metaIP, _ := netip.ParseAddr(destination.IP.String())
				meta := route.Meta{Network: "udp", Protocol: "mieru", IP: metaIP, Port: uint16(destination.Port)}
				if item.dst.FQDN != "" {
					meta.Domain = item.dst.FQDN
				}
				pc, err := transport.ListenUDP(loopCtx, meta, M.ParseSocksaddrHostPort(destination.IP.String(), uint16(destination.Port)))
				if err != nil {
					return err
				}
				routeCtx, routeCancel := context.WithCancel(loopCtx)
				r = &mieruUDPRoute{conn: pc, cancel: routeCancel}
				actual, already := routes.LoadOrStore(key, r)
				if already {
					routeCancel()
					_ = pc.Close()
					r = actual.(*mieruUDPRoute)
				} else {
					go h.readMieruUDPRoute(routeCtx, pc, outCh)
				}
			}
			if _, err := r.conn.WriteTo(item.payload, destination); err != nil {
				return err
			}
		case item := <-outCh:
			if _, err := writeMieruUDPPacket(tunnel, item.addr, item.payload); err != nil {
				return err
			}
		case <-loopCtx.Done():
			return loopCtx.Err()
		}
	}
}

func (h *Inbound) readMieruUDPRoute(ctx context.Context, conn net.PacketConn, outCh chan<- mieruUDPOutbound) {
	buf := make([]byte, 1<<16)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		item := mieruUDPOutbound{payload: append([]byte(nil), buf[:n]...), addr: addr}
		select {
		case outCh <- item:
		case <-ctx.Done():
			return
		}
	}
}

func parseMieruUDPPacket(pkt []byte) (model.AddrSpec, []byte, []byte, error) {
	if len(pkt) <= 6 || pkt[0] != 0 || pkt[1] != 0 || pkt[2] != 0 {
		return model.AddrSpec{}, nil, nil, fmt.Errorf("invalid mieru UDP associate packet")
	}
	r := bytes.NewReader(pkt[3:])
	dst := model.AddrSpec{}
	if err := dst.ReadFromSocks5(r); err != nil {
		return model.AddrSpec{}, nil, nil, err
	}
	headerLen := len(pkt) - r.Len()
	return dst, pkt[headerLen:], append([]byte(nil), pkt[:headerLen]...), nil
}

func resolveMieruUDPAddr(ctx context.Context, dst model.AddrSpec) (*net.UDPAddr, error) {
	if dst.IP != nil {
		return &net.UDPAddr{IP: dst.IP, Port: dst.Port}, nil
	}
	if dst.FQDN == "" {
		return nil, fmt.Errorf("mieru UDP destination has no address")
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "udp", dst.FQDN)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = fmt.Errorf("no address for %s", dst.FQDN)
		}
		return nil, err
	}
	return &net.UDPAddr{IP: addrs[0].AsSlice(), Port: dst.Port}, nil
}

func writeMieruUDPPacket(tunnel *apicommon.PacketOverStreamTunnel, addr net.Addr, payload []byte) (int, error) {
	var spec model.NetAddrSpec
	if err := spec.From(addr); err != nil {
		return 0, err
	}
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0})
	if err := spec.AddrSpec.WriteToSocks5(&buf); err != nil {
		return 0, err
	}
	buf.Write(payload)
	return tunnel.Write(buf.Bytes())
}
