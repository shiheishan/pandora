package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	tuic "github.com/aegispanel/nodeagent/internal/nativewire/tuic"
	"github.com/aegispanel/nodeagent/route"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type tuicSlot struct {
	user   core.User
	uuid   [16]byte
	active bool
}

type tuicAdapter struct {
	spec InboundSpec

	mu        sync.RWMutex
	users     map[string]int
	slots     []tuicSlot
	traffic   map[int64]core.UserTraffic
	online    map[int64]map[string]struct{}
	service   *tuic.Service[int]
	packet    net.PacketConn
	plane     DataPlane
	limiters  core.SpeedLimiters
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	active    map[net.Conn]struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	updateMu  sync.Mutex
}

var _ Adapter = (*tuicAdapter)(nil)
var _ N.TCPConnectionHandlerEx = (*tuicAdapter)(nil)
var _ N.UDPConnectionHandlerEx = (*tuicAdapter)(nil)

func newTUICAdapter(spec InboundSpec) (Adapter, error) {
	return &tuicAdapter{
		spec: spec, users: make(map[string]int), traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}),
	}, nil
}

func (a *tuicAdapter) Protocol() string { return "tuic" }

func (a *tuicAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "tuic") {
		return fmt.Errorf("tuic adapter received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("tuic port is invalid")
	}
	if network := strings.TrimSpace(rawString(spec.Config.Raw, "network")); network != "" && !strings.EqualFold(network, "udp") {
		return fmt.Errorf("tuic requires UDP transport, got %q", network)
	}
	if strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")) == "" || strings.TrimSpace(rawString(spec.Config.Raw, "key_path")) == "" {
		return fmt.Errorf("tuic requires cert_path and key_path")
	}
	if cc := strings.ToLower(strings.TrimSpace(rawString(spec.Config.Raw, "congestion_control"))); cc != "" && cc != "cubic" && cc != "new_reno" && cc != "bbr" {
		return fmt.Errorf("tuic unsupported congestion_control %q", cc)
	}
	for _, key := range []string{"auth_timeout", "heartbeat", "udp_timeout"} {
		if value, exists := spec.Config.Raw[key]; exists {
			if _, err := parseHysteriaDuration(value); err != nil {
				return fmt.Errorf("tuic %s: %w", key, err)
			}
		}
	}
	if value, exists := spec.Config.Raw["zero_rtt"]; exists {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("tuic zero_rtt must be boolean")
		}
	}
	// udp_over_stream 是客户端侧的选择：它决定客户端把 UDP 包走 QUIC
	// datagram 还是走一条流。服务端两种都收，没有对应的开关可以设。
	//
	// 这里明确拒绝而不是忽略。原先是「只校验类型、Start 里从不读取」，
	// 于是管理员在面板上勾了它、保存成功、然后什么都没发生——配了没用
	// 是最误导的一种状态，比配不了更糟。
	//
	// 这个问题是逐个协议对比「面板允许配的字段」和「适配器真正读取的
	// 字段」找出来的。同一轮核查里另外两个看着像的其实没问题：
	// shadowtls 的 version 和 shadowsocks 的 plugin 都是读出来就为了
	// 报错拒绝，属于有意为之。
	//
	// 试过把这个核查写成自动化测试，没成。它要区分的是「读了值用来校验
	// 类型」和「读了值用来拒绝功能」，这两者在源码里长得一模一样——都是
	// 取值、if、return fmt.Errorf。写出来的版本要么漏报（放过了这里的
	// 原始 bug），要么误报（把上面那两个正当的拒绝也算进去）。拿修复前
	// 的代码回放才发现它其实抓不住目标，删掉了：一个抓不住的测试比没有
	// 更糟，它会让人以为这一类问题有人盯着。
	//
	// 新增配置项时的人工检查点：字段加进面板 schema 之后，确认适配器的
	// Start 路径真的读它，而不是只在 Validate 里校验一下类型。
	if _, exists := spec.Config.Raw["udp_over_stream"]; exists {
		return fmt.Errorf("tuic 入站没有 udp_over_stream 开关：它是客户端侧的 UDP 中继模式选择，服务端两种都支持，请从配置里去掉")
	}
	return nil
}

func (a *tuicAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("tuic requires context and data plane")
	}
	if err := a.Validate(spec); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(rawString(spec.Config.Raw, "cert_path"), rawString(spec.Config.Raw, "key_path"))
	if err != nil {
		return fmt.Errorf("tuic TLS certificate: %w", err)
	}
	// QUIC 强制要求 ALPN：服务端不选，客户端直接
	// CRYPTO_ERROR 0x178 "server did not select an ALPN protocol"。
	// h3 是 TUIC 生态的事实默认，Mihomo 和 sing-box 的客户端在没配
	// alpn 时发的都是它。允许覆盖，但不能留空。
	//
	// 解析放在 WithCancel 之前：这里的出错分支会直接 return，夹在
	// context 创建之后就会漏掉 cancel。
	alpn, err := stringList(spec.Config.Raw["alpn"])
	if err != nil {
		return fmt.Errorf("tuic alpn: %w", err)
	}
	if len(alpn) == 0 {
		alpn = []string{"h3"}
	}
	ctx, cancel := context.WithCancel(parent)
	tlsConfig := &hysteria2TLSConfig{std: &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   alpn,
	}}
	congestionControl := strings.ToLower(strings.TrimSpace(rawString(spec.Config.Raw, "congestion_control")))
	zeroRTT, _ := spec.Config.Raw["zero_rtt"].(bool)
	authTimeout, heartbeat, udpTimeout := 3*time.Second, 10*time.Second, 5*time.Minute
	if value, exists := spec.Config.Raw["auth_timeout"]; exists {
		authTimeout, err = parseHysteriaDuration(value)
		if err != nil {
			cancel()
			return fmt.Errorf("tuic auth_timeout: %w", err)
		}
	}
	if value, exists := spec.Config.Raw["heartbeat"]; exists {
		heartbeat, err = parseHysteriaDuration(value)
		if err != nil {
			cancel()
			return fmt.Errorf("tuic heartbeat: %w", err)
		}
	}
	if value, exists := spec.Config.Raw["udp_timeout"]; exists {
		udpTimeout, err = parseHysteriaDuration(value)
		if err != nil {
			cancel()
			return fmt.Errorf("tuic udp_timeout: %w", err)
		}
	}
	service, err := tuic.NewService[int](tuic.ServiceOptions{
		Context: ctx, Logger: logger.NOP(), TLSConfig: tlsConfig,
		CongestionControl: congestionControl, AuthTimeout: authTimeout,
		ZeroRTTHandshake: zeroRTT, Heartbeat: heartbeat, UDPTimeout: udpTimeout, Handler: a,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("tuic service: %w", err)
	}
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	packet, err := net.ListenPacket("udp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
	if err != nil {
		cancel()
		return err
	}
	a.mu.Lock()
	if a.closed || a.service != nil {
		a.mu.Unlock()
		cancel()
		_ = packet.Close()
		return fmt.Errorf("tuic adapter already started or closed")
	}
	a.spec, a.plane, a.ctx, a.cancel, a.service, a.packet = spec, hooks.DataPlane, ctx, cancel, service, packet
	a.mu.Unlock()
	if err := a.syncUsers(); err != nil {
		_ = a.Close()
		return err
	}
	if err := service.Start(packet); err != nil {
		_ = a.Close()
		return fmt.Errorf("tuic listen: %w", err)
	}
	return nil
}

func (a *tuicAdapter) syncUsers() error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.RLock()
	service := a.service
	indices := make([]int, 0, len(a.slots))
	uuidList := make([][16]byte, 0, len(a.slots))
	passwords := make([]string, 0, len(a.slots))
	for i, slot := range a.slots {
		if !slot.active {
			continue
		}
		indices = append(indices, i)
		uuidList = append(uuidList, slot.uuid)
		passwords = append(passwords, slot.user.UUID)
	}
	a.mu.RUnlock()
	if service != nil {
		service.UpdateUsers(indices, uuidList, passwords)
	}
	return nil
}

func (a *tuicAdapter) AddUsers(users []core.User) error {
	validated := make([]tuicSlot, 0, len(users))
	for _, user := range users {
		parsed, err := uuid.FromString(strings.TrimSpace(user.UUID))
		if err != nil {
			return fmt.Errorf("tuic user %q UUID is invalid", user.UUID)
		}
		user.UUID = parsed.String()
		validated = append(validated, tuicSlot{user: user, uuid: [16]byte(parsed), active: true})
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("tuic adapter is closed")
	}
	parsedUsers := make([]tuicSlot, 0, len(validated))
	for _, slot := range validated {
		if _, exists := a.users[slot.user.UUID]; exists {
			continue
		}
		parsedUsers = append(parsedUsers, slot)
	}
	for _, slot := range parsedUsers {
		a.users[slot.user.UUID] = len(a.slots)
		a.slots = append(a.slots, slot)
	}
	a.mu.Unlock()
	return a.syncUsers()
}

func (a *tuicAdapter) UpsertUsers(users []core.User) error {
	validated := make([]tuicSlot, 0, len(users))
	for _, user := range users {
		parsed, err := uuid.FromString(strings.TrimSpace(user.UUID))
		if err != nil {
			return fmt.Errorf("tuic user %q UUID is invalid", user.UUID)
		}
		user.UUID = parsed.String()
		validated = append(validated, tuicSlot{user: user, uuid: [16]byte(parsed), active: true})
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("tuic adapter is closed")
	}
	changed := false
	for _, slot := range validated {
		if index, exists := a.users[slot.user.UUID]; exists {
			a.slots[index].user = slot.user
			a.slots[index].uuid = slot.uuid
			a.slots[index].active = true
			changed = true
			continue
		}
		a.users[slot.user.UUID] = len(a.slots)
		a.slots = append(a.slots, slot)
		changed = true
	}
	a.mu.Unlock()
	if !changed {
		return nil
	}
	return a.syncUsers()
}

func (a *tuicAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	for _, id := range ids {
		parsed, err := uuid.FromString(strings.TrimSpace(id))
		if err != nil {
			continue
		}
		canonical := parsed.String()
		if index, ok := a.users[canonical]; ok {
			a.slots[index].active = false
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			a.limiters.Remove(a.slots[index].user.ID)
			delete(a.users, canonical)
		}
	}
	a.mu.Unlock()
	return a.syncUsers()
}

func (a *tuicAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, traffic := range a.traffic {
		if traffic.Upload != 0 || traffic.Download != 0 {
			out = append(out, traffic)
		}
		delete(a.traffic, id)
	}
	return out, nil
}

func (a *tuicAdapter) OnlineIPs() map[int64][]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[int64][]string, len(a.online))
	for id, ips := range a.online {
		for ip := range ips {
			out[id] = append(out[id], ip)
		}
	}
	return out
}

func (a *tuicAdapter) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	a.wg.Add(1)
	a.active[conn] = struct{}{}
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer a.removeActive(conn)
		defer conn.Close()
		if onClose != nil {
			defer onClose(nil)
		}
		index, user, ok := a.userFromContext(ctx)
		if !ok || !a.enterDevice(user, source.AddrString()) {
			return
		}
		defer a.leaveDevice(user, source.AddrString())
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "tcp", Protocol: "tuic", SourceIP: source.Addr, SourcePort: source.Port}
		upstream, err := a.plane.DialTCP(ctx, meta, destination)
		if err != nil {
			return
		}
		defer upstream.Close()
		var copyWG sync.WaitGroup
		copyWG.Add(2)
		copyDone := make(chan struct{}, 2)
		go func() {
			n, _ := core.SpeedLimitedCopy(upstream, conn, a.limiters.For(user))
			a.addTraffic(index, n, 0)
			copyDone <- struct{}{}
			copyWG.Done()
		}()
		go func() {
			n, _ := core.SpeedLimitedCopy(conn, upstream, a.limiters.For(user))
			a.addTraffic(index, 0, n)
			copyDone <- struct{}{}
			copyWG.Done()
		}()
		<-copyDone
		_ = conn.Close()
		_ = upstream.Close()
		copyWG.Wait()
	}()
}

func (a *tuicAdapter) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer conn.Close()
		if onClose != nil {
			defer onClose(nil)
		}
		index, user, ok := a.userFromContext(ctx)
		if !ok || !a.enterDevice(user, source.AddrString()) {
			return
		}
		defer a.leaveDevice(user, source.AddrString())
		meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "udp", Protocol: "tuic", SourceIP: source.Addr, SourcePort: source.Port}
		upstream, err := a.plane.ListenUDP(ctx, meta, destination)
		if err != nil {
			return
		}
		defer upstream.Close()
		bridgeCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var bridgeWG sync.WaitGroup
		bridgeWG.Add(2)
		go func() {
			defer cancel()
			defer bridgeWG.Done()
			for {
				packet := buf.NewPacket()
				dst, err := conn.ReadPacket(packet)
				if err != nil {
					packet.Release()
					return
				}
				if !dst.IsValid() {
					dst = destination
				}
				addr, err := resolveUDPAddr(bridgeCtx, dst)
				if err != nil {
					packet.Release()
					continue
				}
				if n, err := upstream.WriteTo(packet.Bytes(), addr); err != nil {
					packet.Release()
					return
				} else {
					a.addTraffic(index, int64(n), 0)
				}
				packet.Release()
			}
		}()
		go func() {
			defer cancel()
			defer bridgeWG.Done()
			data := make([]byte, 64<<10)
			for {
				n, addr, err := upstream.ReadFrom(data)
				if err != nil {
					return
				}
				packet := buf.As(append([]byte(nil), data[:n]...))
				if err := conn.WritePacket(packet, M.SocksaddrFromNet(addr).Unwrap()); err != nil {
					return
				}
				a.addTraffic(index, 0, int64(n))
			}
		}()
		go func() {
			<-bridgeCtx.Done()
			_ = conn.SetDeadline(time.Now())
			_ = upstream.SetDeadline(time.Now())
		}()
		bridgeWG.Wait()
	}()
}

func (a *tuicAdapter) userFromContext(ctx context.Context) (int, core.User, bool) {
	index, ok := auth.UserFromContext[int](ctx)
	if !ok {
		return 0, core.User{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if index < 0 || index >= len(a.slots) || !a.slots[index].active {
		return 0, core.User{}, false
	}
	return index, a.slots[index].user, true
}

func (a *tuicAdapter) enterDevice(user core.User, ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.online[user.ID]
	if set == nil {
		set = make(map[string]struct{})
		a.online[user.ID] = set
	}
	if _, exists := set[ip]; !exists && user.DeviceLimit > 0 && len(set) >= user.DeviceLimit {
		return false
	}
	set[ip] = struct{}{}
	return true
}

func (a *tuicAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *tuicAdapter) addTraffic(index int, upload, download int64) {
	a.mu.Lock()
	if index >= 0 && index < len(a.slots) {
		id := a.slots[index].user.ID
		current := a.traffic[id]
		current.ID = id
		current.Upload += upload
		current.Download += download
		a.traffic[id] = current
	}
	a.mu.Unlock()
}

func (a *tuicAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}

func (a *tuicAdapter) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		cancel, service, packet := a.cancel, a.service, a.packet
		active := make([]net.Conn, 0, len(a.active))
		for conn := range a.active {
			active = append(active, conn)
		}
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if service != nil {
			_ = service.Close()
		}
		if packet != nil {
			_ = packet.Close()
		}
		for _, conn := range active {
			_ = conn.Close()
		}
	})
	a.wg.Wait()
	return nil
}
