package kernel

// Shadowsocks 2022（SIP022 + SIP023）多用户入站：
//   - 配置 password = base64(iPSK)，长度等于方法密钥长度；
//   - 每用户 uPSK 由代理 UUID 按共用约定派生，用户表键为 blake3(uPSK) 前 16 字节；
//   - TCP 请求 salt | EIH | AEAD 固定头 | AEAD 变长头，EIH 解出用户 hash 后查表，会话密钥用 uPSK；
//   - 认证失败（含查不到用户、重放、超窗、请求头未读全）与经典 SS 同口径，经 drainAfterReport 排空。
// UDP 见 shadowsocks2022_udp.go，共用 ssUDPHub。

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lukechampine.com/blake3"

	"github.com/aegispanel/nodeagent/core"
)

const (
	ss2022ClientHeader byte = 0
	ss2022ServerHeader byte = 1
	ss2022WriteChunk        = 16*1024 - 1 // 写方向每块负载上限（与经典 ssChunkLimit 同值）
	ss2022MaxReadChunk      = 0xFFFF      // 读方向每块负载上限：SIP022 允许到 0xFFFF
	ss2022Overhead          = 16          // AEAD 标签
	ss2022EIHLen            = 16          // 一个身份头（aes.BlockSize）
	ss2022MaxTimeDiff       = 30          // 时间戳窗口（秒），超出按重放
	ss2022UserPSKInfo       = "pandora/ss2022/user-psk/v1"
)

type ss2022Spec struct {
	name   string
	keyLen int // 16 或 32；AEAD 一律 newAESGCM
}

// parseSS2022Spec 只认两种 AES 方法；chacha20-poly1305 给出专门的拒绝原因。
func parseSS2022Spec(method string) (ss2022Spec, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "2022-blake3-aes-128-gcm":
		return ss2022Spec{name: method, keyLen: 16}, nil
	case "2022-blake3-aes-256-gcm":
		return ss2022Spec{name: method, keyLen: 32}, nil
	case "2022-blake3-chacha20-poly1305":
		return ss2022Spec{}, fmt.Errorf("不支持的加密方式 %s：shadowsocks 2022 的 chacha20-poly1305 没有多用户身份头（SIP023 只定义了 AES 方法）；请改用 2022-blake3-aes-128-gcm 或 2022-blake3-aes-256-gcm", method)
	default:
		return ss2022Spec{}, fmt.Errorf("native shadowsocks 2022 method %q is unsupported", method)
	}
}

// parseSS2022ServerPSK 读配置的 password：标准 base64（带填充）的 iPSK，解出的长度必须等于方法的密钥长度。
func parseSS2022ServerPSK(raw map[string]any, spec ss2022Spec) ([]byte, error) {
	password, ok := raw["password"].(string)
	if !ok || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("shadowsocks 2022 需要 password（base64 的服务端身份密钥）")
	}
	ipsk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(password))
	if err != nil {
		return nil, fmt.Errorf("shadowsocks 2022 password 不是标准 base64: %w", err)
	}
	if len(ipsk) != spec.keyLen {
		return nil, fmt.Errorf("shadowsocks 2022 password 解出 %d 字节，%s 要 %d 字节", len(ipsk), spec.name, spec.keyLen)
	}
	return ipsk, nil
}

// ss2022UserPSK 按共用约定从用户的代理 UUID 算 uPSK：SHA-256(ss2022UserPSKInfo || 0x00 || 小写 UUID) 取前 keyLen 字节。
func ss2022UserPSK(uuid string, keyLen int) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte(ss2022UserPSKInfo))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.ToLower(uuid)))
	sum := h.Sum(nil)
	return sum[:keyLen]
}

// ss2022UserHash 是 SIP023 用户表的键：blake3(uPSK) 的前 16 字节。（UDP 用）
func ss2022UserHash(upsk []byte) [16]byte {
	sum := blake3.Sum256(upsk)
	var hash [16]byte
	copy(hash[:], sum[:16])
	return hash
}

type ss2022Adapter struct {
	spec      InboundSpec
	method    ss2022Spec
	ipsk      []byte       // 服务端身份密钥（配置 password），TCP 派生身份子密钥用
	ipskBlock cipher.Block // aes(iPSK)：UDP 的分离头与身份头直接用它加解（SIP023）（UDP 用）
	mu        sync.RWMutex
	// users 是用户集合的事实源（a.mu 保护），键是 ss2022UserHash(uPSK)；每次变更后重建 snapshot，收包与握手只读 snapshot。
	users    map[[16]byte]*ssUser
	snapshot atomic.Pointer[map[[16]byte]*ssUser]
	sessions userSessions
	limiters core.SpeedLimiters
	online   onlineDevices
	listener net.Listener
	packet   net.PacketConn
	plane    DataPlane
	connErr  connErrorReporter
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   map[net.Conn]struct{}
	// salts 是 TCP 请求 salt 的精确防重放表（认证通过才记），全进程共用，见 shadowsocks2022_tcp.go。
	salts *replayFilter
	// headerTimeout 只给测试缩短读请求头的截止时间，零值为 10 秒。
	headerTimeout time.Duration
	ssUDPHub      // UDP 会话表与生命周期，见 shadowsocks_udp_hub.go 与 shadowsocks2022_udp.go
	wg            sync.WaitGroup
}

func newSS2022Adapter(spec InboundSpec) (Adapter, error) {
	method, _ := spec.Config.Raw["method"].(string)
	parsed, err := parseSS2022Spec(method)
	if err != nil {
		return nil, err
	}
	if _, ok := spec.Config.Raw["network"]; ok {
		return nil, fmt.Errorf("shadowsocks 入站不再接受 network 字段：TCP 与 UDP 总是同端口一起开")
	}
	ipsk, err := parseSS2022ServerPSK(spec.Config.Raw, parsed)
	if err != nil {
		return nil, err
	}
	ipskBlock, err := aes.NewCipher(ipsk)
	if err != nil {
		return nil, err
	}
	return &ss2022Adapter{
		spec: spec, method: parsed, ipsk: ipsk, ipskBlock: ipskBlock,
		users: make(map[[16]byte]*ssUser), active: make(map[net.Conn]struct{}),
		salts:    sharedSS2022SaltFilter(),
		ssUDPHub: ssUDPHub{udp: make(map[ssUDPKey][]*ssUDPSession)},
	}, nil
}

func (a *ss2022Adapter) Protocol() string {
	return strings.ToLower(strings.TrimSpace(a.spec.Config.Protocol))
}

func (a *ss2022Adapter) Validate(spec InboundSpec) error {
	method, _ := spec.Config.Raw["method"].(string)
	parsed, err := parseSS2022Spec(method)
	if err != nil {
		return err
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("shadowsocks 2022 port is invalid")
	}
	if _, ok := spec.Config.Raw["network"]; ok {
		return fmt.Errorf("shadowsocks 入站不再接受 network 字段：TCP 与 UDP 总是同端口一起开")
	}
	if plugin, _ := spec.Config.Raw["plugin"].(string); strings.TrimSpace(plugin) != "" {
		return fmt.Errorf("native shadowsocks rejects plugin transport")
	}
	if _, err := parseSS2022ServerPSK(spec.Config.Raw, parsed); err != nil {
		return err
	}
	return nil
}

func (a *ss2022Adapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("shadowsocks 2022 start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.packet != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("shadowsocks 2022 adapter already started or closed")
	}
	a.spec, a.plane = spec, hooks.DataPlane
	a.connErr = newConnErrorReporter(hooks, spec, a.Protocol())
	a.ctx, a.cancel = context.WithCancel(parent)
	listenAddress := spec.Config.Listen
	if listenAddress == "" {
		listenAddress = "0.0.0.0"
	}
	addr := net.JoinHostPort(listenAddress, strconv.Itoa(spec.Config.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		a.cancel()
		a.mu.Unlock()
		return asExternalPortInUse([]portKey{{port: spec.Config.Port, l4: "tcp"}}, err)
	}
	packet, packetErr := net.ListenPacket("udp", addr)
	if packetErr != nil {
		_ = listener.Close()
		a.cancel()
		a.mu.Unlock()
		return asExternalPortInUse([]portKey{{port: spec.Config.Port, l4: "udp"}}, packetErr)
	}
	udpConn, ok := packet.(*net.UDPConn)
	if !ok {
		_ = packet.Close()
		_ = listener.Close()
		a.cancel()
		a.mu.Unlock()
		return fmt.Errorf("shadowsocks 2022 UDP listen: unexpected PacketConn type %T", packet)
	}
	a.listener, a.packet = listener, packet
	a.bindUDP(udpConn, ssUDPHubBinding{
		proto: "shadowsocks-2022", ctx: a.ctx, wg: &a.wg, plane: a.plane, connErr: &a.connErr,
		online: &a.online, limiters: &a.limiters, sessions: &a.sessions, encode: a.encodeUDPSession,
	})
	a.mu.Unlock()
	workers := ssUDPSlowWorkers()
	a.wg.Add(3 + workers)
	go a.acceptLoop()
	go a.packetLoop()
	go a.udpJanitor()
	for i := 0; i < workers; i++ {
		go a.udpWorker(a.slowPathUDP)
	}
	return nil
}

func (a *ss2022Adapter) acceptLoop() {
	defer a.wg.Done()
	runAcceptLoop(a.ctx.Done(), a.listener.Accept, func(conn net.Conn) {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = conn.Close()
			return
		}
		a.active[conn] = struct{}{}
		a.wg.Add(1)
		ctx := a.ctx
		a.mu.Unlock()
		goGuarded(conn, func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			_ = a.handleConn(ctx, conn)
		})
	})
}

// ss2022UserEntry 是校验过、待发布的一条用户记录。
type ss2022UserEntry struct {
	key  [16]byte
	user *ssUser
}

// userRecords 校验整批并算出每个用户的 uPSK 与用户表键；有一个不合格整批不发布。
func (a *ss2022Adapter) userRecords(users []core.User) ([]ss2022UserEntry, error) {
	out := make([]ss2022UserEntry, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" || len(user.UUID) > 256 {
			return nil, fmt.Errorf("shadowsocks 2022 user %d uuid invalid", user.ID)
		}
		upsk := ss2022UserPSK(user.UUID, a.method.keyLen)
		out = append(out, ss2022UserEntry{
			key:  ss2022UserHash(upsk),
			user: &ssUser{ID: user.ID, DeviceLimit: user.DeviceLimit, SpeedLimit: user.SpeedLimit, MasterKey: upsk},
		})
	}
	return out, nil
}

func (a *ss2022Adapter) AddUsers(users []core.User) error {
	validated, err := a.userRecords(users)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("shadowsocks 2022 adapter closed")
	}
	for _, entry := range validated {
		if _, exists := a.users[entry.key]; !exists {
			a.users[entry.key] = entry.user
		}
	}
	a.publishUsersLocked()
	return nil
}

func (a *ss2022Adapter) UpsertUsers(users []core.User) error {
	validated, err := a.userRecords(users)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("shadowsocks 2022 adapter closed")
	}
	for _, entry := range validated {
		if prev, exists := a.users[entry.key]; exists {
			a.limiters.Remove(prev.ID)
			prev.retired.Store(true)
		}
		a.users[entry.key] = entry.user
	}
	a.publishUsersLocked()
	return nil
}

func (a *ss2022Adapter) DelUsers(ids []string) error {
	a.mu.Lock()
	var removed []int64
	for _, id := range ids {
		key := ss2022UserHash(ss2022UserPSK(id, a.method.keyLen))
		if entry, ok := a.users[key]; ok {
			a.limiters.Remove(entry.ID)
			entry.retired.Store(true)
			removed = append(removed, entry.ID)
		}
		delete(a.users, key)
	}
	a.publishUsersLocked()
	a.mu.Unlock()
	a.sessions.revoke(removed)
	return nil
}

func (a *ss2022Adapter) publishUsersLocked() {
	snap := make(map[[16]byte]*ssUser, len(a.users))
	for k, v := range a.users {
		snap[k] = v
	}
	a.snapshot.Store(&snap)
}

// lookupUser 按 SIP023 的用户 hash 查当前快照，查不到返回 nil；不加锁、不分配。（UDP 用）
func (a *ss2022Adapter) lookupUser(hash [16]byte) *ssUser {
	snap := a.snapshot.Load()
	if snap == nil {
		return nil
	}
	return (*snap)[hash]
}

func (a *ss2022Adapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
}

func (a *ss2022Adapter) OnlineIPs() map[int64][]string { return a.online.snapshot() }

func (a *ss2022Adapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	listener := a.listener
	packet := a.packet
	active := make([]net.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if packet != nil {
		_ = packet.Close()
	}
	for _, conn := range active {
		_ = conn.Close()
	}
	a.closeUDPSessions()
	a.wg.Wait()
	return nil
}

func (a *ss2022Adapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}
