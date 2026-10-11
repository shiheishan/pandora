package kernel

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ssChunkLimit 是 AEAD 分块负载上限 0x3FFF（SIP004，与 ss2022MaxChunk 同值），读写两侧共用。
// 曾写成 16*1024：上游一次读满 16384 字节（TLS 记录常见）就发出超长块，规范客户端判非法断开，
// Vultr 实测表现为 HTTPS 大响应在 16KB 整数倍处截断。
const ssChunkLimit = 16*1024 - 1

type ssMethodSpec struct {
	Name    string
	KeyLen  int
	SaltLen int
	NewAEAD func([]byte) (cipher.AEAD, error)
}

var nativeSSMethods = map[string]ssMethodSpec{
	"aes-128-gcm":             {Name: "aes-128-gcm", KeyLen: 16, SaltLen: 16, NewAEAD: newAESGCM},
	"aes-192-gcm":             {Name: "aes-192-gcm", KeyLen: 24, SaltLen: 24, NewAEAD: newAESGCM},
	"aes-256-gcm":             {Name: "aes-256-gcm", KeyLen: 32, SaltLen: 32, NewAEAD: newAESGCM},
	"chacha20-ietf-poly1305":  {Name: "chacha20-ietf-poly1305", KeyLen: 32, SaltLen: 32, NewAEAD: chacha20poly1305.New},
	"xchacha20-ietf-poly1305": {Name: "xchacha20-ietf-poly1305", KeyLen: 32, SaltLen: 32, NewAEAD: chacha20poly1305.NewX},
}

// ssUser 发布后只读；同一口令被 UpsertUsers 换成新记录或被删除时 retired 置位，
// 来源 IP 提示里残留的旧指针据此作废（见 shadowsocks_match.go）。
type ssUser struct {
	ID          int64
	DeviceLimit int
	// SpeedLimit 之前没存，面板下发的限速到这里就丢了。
	SpeedLimit int
	MasterKey  []byte
	retired    atomic.Bool
	// lastAuth 是最近一次认证通过的时间（UnixNano），快照据此把活跃用户排前。
	lastAuth atomic.Int64
}

type shadowsocksAdapter struct {
	protocol string
	spec     InboundSpec
	method   ssMethodSpec
	mu       sync.RWMutex
	// users 是用户集合的事实源（a.mu 保护）；每次变更后重建 snapshot，
	// 握手只读 snapshot，不加锁、不复制。
	users      map[string]*ssUser
	snapshot   atomic.Pointer[ssUserSnapshot]
	reordering atomic.Bool
	hints      ssSourceHints
	// sessions 是按用户的在途连接表与原子流量计数（user_sessions.go）。
	sessions userSessions
	online   onlineDevices
	plane    DataPlane
	connErr  connErrorReporter
	limiters core.SpeedLimiters
	listener net.Listener
	packet   net.PacketConn
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   map[net.Conn]struct{}
	// salts 是 TCP 请求 salt 的防重放表（认证通过才记），全进程共用一张，
	// 见 replay_filter_bloom.go。
	salts *saltBloom
	// headerTimeout 只给测试缩短读请求头的截止时间，零值为 10 秒。
	headerTimeout time.Duration
	wg            sync.WaitGroup
}

func newShadowsocksAdapter(spec InboundSpec) (Adapter, error) {
	method, _ := spec.Config.Raw["method"].(string)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-") {
		return newSS2022Adapter(spec)
	}
	methodSpec, err := parseNativeSSMethod(spec.Config.Raw)
	if err != nil {
		return nil, err
	}
	return &shadowsocksAdapter{
		protocol: strings.ToLower(strings.TrimSpace(spec.Config.Protocol)), spec: spec, method: methodSpec,
		users:  make(map[string]*ssUser),
		active: make(map[net.Conn]struct{}),
		salts:  sharedSSSaltBloom(),
	}, nil
}

func (a *shadowsocksAdapter) Protocol() string { return a.protocol }

// acceptSalt 报告认证通过的请求 salt 是否没见过（没见过即记下）。
func (a *shadowsocksAdapter) acceptSalt(salt []byte) bool {
	return a.salts == nil || a.salts.check(salt, time.Now())
}

func (a *shadowsocksAdapter) Validate(spec InboundSpec) error {
	if p := strings.ToLower(strings.TrimSpace(spec.Config.Protocol)); p != "shadowsocks" && p != "ss" {
		return fmt.Errorf("native shadowsocks received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("shadowsocks port is invalid")
	}
	if network, _ := spec.Config.Raw["network"].(string); network != "" && !strings.EqualFold(network, "tcp") && !strings.EqualFold(network, "udp") {
		return fmt.Errorf("native shadowsocks currently accepts tcp and udp only")
	}
	if _, err := parseNativeSSMethod(spec.Config.Raw); err != nil {
		return err
	}
	if plugin, _ := spec.Config.Raw["plugin"].(string); strings.TrimSpace(plugin) != "" {
		return fmt.Errorf("native shadowsocks rejects plugin transport")
	}
	return nil
}

func parseNativeSSMethod(raw map[string]any) (ssMethodSpec, error) {
	method, _ := raw["method"].(string)
	method = strings.ToLower(strings.TrimSpace(method))
	spec, ok := nativeSSMethods[method]
	if !ok {
		return ssMethodSpec{}, fmt.Errorf("native shadowsocks method %q is unsupported; classic AEAD TCP/UDP is enabled", method)
	}
	if password, _ := raw["password"].(string); strings.TrimSpace(password) != "" {
		return spec, nil
	}
	return spec, nil
}

func (a *shadowsocksAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("shadowsocks start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("shadowsocks adapter already started or closed")
	}
	a.spec, a.plane, a.connErr = spec, hooks.DataPlane, newConnErrorReporter(hooks, spec, a.protocol)
	a.ctx, a.cancel = context.WithCancel(parent)
	listenAddress := spec.Config.Listen
	if listenAddress == "" {
		listenAddress = "0.0.0.0"
	}
	network, _ := spec.Config.Raw["network"].(string)
	if strings.EqualFold(network, "udp") {
		packet, packetErr := net.ListenPacket("udp", net.JoinHostPort(listenAddress, fmt.Sprintf("%d", spec.Config.Port)))
		if packetErr != nil {
			a.cancel()
			a.mu.Unlock()
			return packetErr
		}
		a.packet = packet
		a.mu.Unlock()
		a.wg.Add(1)
		go a.packetLoop()
		return nil
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(listenAddress, fmt.Sprintf("%d", spec.Config.Port)))
	if err != nil {
		a.cancel()
		a.mu.Unlock()
		return err
	}
	a.listener = listener
	a.mu.Unlock()
	a.wg.Add(1)
	go a.acceptLoop()
	return nil
}

func (a *shadowsocksAdapter) acceptLoop() {
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

// handleConn 是 TCP 会话入口，也是 shadowtls 解开外层后交进来的入口；会话
// 层失败在这里统一上报。shadowtls 托管的内层实例 connErr 带的是 shadowtls
// 的 tag 与协议名，日志里看到的是外层入站。
func (a *shadowsocksAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	err := a.serveConn(ctx, conn)
	a.connErr.conn(StageSession, conn, err)
	finishSession(conn, err)
	return err
}

// serveConn 不关 conn：由 handleConn 上报之后经 finishSession 收尾。
func (a *shadowsocksAdapter) serveConn(ctx context.Context, conn net.Conn) error {
	epoch := a.sessions.epoch()
	_ = conn.SetReadDeadline(time.Now().Add(requestHeaderTimeout(a.headerTimeout)))
	user, stream, destination, err := a.readRequest(conn)
	if err != nil {
		// 认证失败、salt 重放、首块解不开、请求头没读全就撞上截止：上报之后
		// 一直读到对端关再关（读错误本身则立即返回，读空不会多等）。
		return drainAfterReport{err}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	sess := a.sessions.open(user, epoch, conn)
	if sess == nil {
		return errSessionRevoked
	}
	defer sess.close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.online.enter(user, ip) {
		return deviceLimitError("shadowsocks")
	}
	defer a.online.leave(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "shadowsocks", SourceIP: sourceIP}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	// ssStream 自带 CloseWrite（把半关闭转给底层），转发据此把上游结束传给客户端。
	sess.relay(stream, upstream, core.RelayOptions{Limiter: a.limiters.For(user)})
	return nil
}

func (a *shadowsocksAdapter) readRequest(conn net.Conn) (core.User, *ssStream, vlessDestination, error) {
	var destination vlessDestination
	sc := getSSScratch()
	defer putSSScratch(sc)
	// salt 与加密长度块读进同一块缓冲：salt 要留给防重放表，长度块只在本函数内用。
	header := make([]byte, a.method.SaltLen+2+16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return core.User{}, nil, destination, err
	}
	salt, encryptedLength := header[:a.method.SaltLen], header[a.method.SaltLen:]
	var length int
	// 每个候选用户都要过完整的 AEAD 校验；来源 IP 提示只决定先试谁。
	selected, selectedAEAD := a.findSSUser(sc, salt, ssSourceKey(conn.RemoteAddr()), func(aead cipher.AEAD) bool {
		plain, err := aead.Open(sc.plain[:0], sc.zeroNonce(), encryptedLength, nil)
		if err != nil || len(plain) != 2 {
			return false
		}
		length = int(binary.BigEndian.Uint16(plain))
		return length > 0 && length <= ssChunkLimit
	})
	if selectedAEAD == nil {
		return core.User{}, nil, destination, markConnError(connErrAuth, fmt.Errorf("shadowsocks user authentication failed"))
	}
	// 重放：把抓到的合法首包原样重发，salt 必然相同。只在认证通过后记录，
	// 乱发的字节填不满这张表。
	if !a.acceptSalt(salt) {
		return core.User{}, nil, destination, markConnError(connErrAuth, fmt.Errorf("shadowsocks salt replayed"))
	}
	first := make([]byte, length+selectedAEAD.Overhead())
	if _, err := io.ReadFull(conn, first); err != nil {
		return core.User{}, nil, destination, err
	}
	// 原地解密：first 归本连接所有，余下明文直接当 pending，不再复制。
	firstPlain, err := selectedAEAD.Open(first[:0], makeSSNonce(1), first, nil)
	if err != nil {
		return core.User{}, nil, destination, err
	}
	consumed, err := parseSSDestination(firstPlain, &destination)
	if err != nil {
		return core.User{}, nil, destination, err
	}
	responseSalt := make([]byte, a.method.SaltLen)
	if _, err := rand.Read(responseSalt); err != nil {
		return core.User{}, nil, destination, err
	}
	sc.setSalt(responseSalt)
	responseAEAD, err := a.method.NewAEAD(sc.subkey(selected.MasterKey, a.method.KeyLen))
	if err != nil {
		return core.User{}, nil, destination, err
	}
	if _, err := conn.Write(responseSalt); err != nil {
		return core.User{}, nil, destination, err
	}
	stream := &ssStream{conn: conn, aead: selectedAEAD, writeAEAD: responseAEAD, readNonce: 2, writeNonce: 0, pending: firstPlain[consumed:]}
	return core.User{ID: selected.ID, DeviceLimit: selected.DeviceLimit, SpeedLimit: selected.SpeedLimit}, stream, destination, nil
}

func parseSSDestination(buf []byte, destination *vlessDestination) (int, error) {
	if len(buf) < 1 {
		return 0, io.ErrUnexpectedEOF
	}
	offset := 1
	switch buf[0] {
	case 1:
		if len(buf) < offset+4 {
			return 0, io.ErrUnexpectedEOF
		}
		var ip [4]byte
		copy(ip[:], buf[offset:offset+4])
		destination.IP = netip.AddrFrom4(ip)
		destination.Host = destination.IP.String()
		offset += 4
	case 3:
		if len(buf) < offset+1 {
			return 0, io.ErrUnexpectedEOF
		}
		length := int(buf[offset])
		offset++
		if length == 0 || length > 253 || len(buf) < offset+length {
			return 0, fmt.Errorf("shadowsocks domain length invalid")
		}
		destination.Domain, destination.Host = string(buf[offset:offset+length]), string(buf[offset:offset+length])
		offset += length
	case 4:
		if len(buf) < offset+16 {
			return 0, io.ErrUnexpectedEOF
		}
		var ip [16]byte
		copy(ip[:], buf[offset:offset+16])
		destination.IP = netip.AddrFrom16(ip)
		destination.Host = destination.IP.String()
		offset += 16
	default:
		return 0, fmt.Errorf("shadowsocks address type unsupported")
	}
	if len(buf) < offset+2 {
		return 0, io.ErrUnexpectedEOF
	}
	destination.Port = binary.BigEndian.Uint16(buf[offset : offset+2])
	if destination.Port == 0 {
		return 0, fmt.Errorf("shadowsocks destination port invalid")
	}
	return offset + 2, nil
}

type ssStream struct {
	conn       net.Conn
	aead       cipher.AEAD
	writeAEAD  cipher.AEAD
	readNonce  uint64
	writeNonce uint64
	// pending 是已解密、调用方还没取走的明文；pendingBuf 非空时它指向池里的
	// 那块缓冲，取完即还。
	pending       []byte
	pendingBuf    *[]byte
	nonce         [12]byte
	writeMu       sync.Mutex
	writeNonceBuf [12]byte
}

// 数据路径不再逐块分配（1c1g 实测 ss 下行分配加 GC 约占 CPU 35%）：长度头在栈上
// 原地解密；调用方缓冲装得下整块时直接读进去原地解密，装不下才借池里的块缓冲；
// 写方向在池里的缓冲上原地封装长度头与正文，一次写出。

// ssFrameBufSize 是一个完整加密块（长度头 2+16 加正文上限 0x3FFF+16）的上界。
const ssFrameBufSize = 2 + 16 + ssChunkLimit + 16

var ssFramePool = sync.Pool{New: func() any { b := make([]byte, ssFrameBufSize); return &b }}

// fillSSNonce 把计数写成 12 字节小端 nonce（与 makeSSNonce 同一口径），不分配。
func fillSSNonce(nonce *[12]byte, value uint64) []byte {
	*nonce = [12]byte{}
	for i := 0; i < len(nonce) && value != 0; i++ {
		nonce[i] = byte(value)
		value >>= 8
	}
	return nonce[:]
}

func (s *ssStream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		if len(s.pending) == 0 && s.pendingBuf != nil {
			ssFramePool.Put(s.pendingBuf)
			s.pendingBuf, s.pending = nil, nil
		}
		return n, nil
	}
	var encryptedLength [2 + 16]byte
	if _, err := io.ReadFull(s.conn, encryptedLength[:]); err != nil {
		return 0, err
	}
	plainLength, err := s.aead.Open(encryptedLength[:0], fillSSNonce(&s.nonce, s.readNonce), encryptedLength[:], nil)
	s.readNonce++
	if err != nil || len(plainLength) != 2 {
		return 0, fmt.Errorf("shadowsocks frame length authentication failed")
	}
	length := int(binary.BigEndian.Uint16(plainLength))
	if length == 0 || length > ssChunkLimit {
		return 0, fmt.Errorf("shadowsocks frame length invalid")
	}
	frameLen := length + s.aead.Overhead()
	if len(p) >= frameLen {
		// 调用方缓冲装得下整块：读进去原地解密，零分配零拷贝。
		if _, err := io.ReadFull(s.conn, p[:frameLen]); err != nil {
			return 0, err
		}
		plain, err := s.aead.Open(p[:0], fillSSNonce(&s.nonce, s.readNonce), p[:frameLen], nil)
		s.readNonce++
		if err != nil {
			return 0, fmt.Errorf("shadowsocks frame authentication failed")
		}
		return len(plain), nil
	}
	bp := ssFramePool.Get().(*[]byte)
	frame := (*bp)[:frameLen]
	if _, err := io.ReadFull(s.conn, frame); err != nil {
		ssFramePool.Put(bp)
		return 0, err
	}
	plain, err := s.aead.Open(frame[:0], fillSSNonce(&s.nonce, s.readNonce), frame, nil)
	s.readNonce++
	if err != nil {
		ssFramePool.Put(bp)
		return 0, fmt.Errorf("shadowsocks frame authentication failed")
	}
	n := copy(p, plain)
	if n < len(plain) {
		s.pending, s.pendingBuf = plain[n:], bp
	} else {
		ssFramePool.Put(bp)
	}
	return n, nil
}

func (s *ssStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	aead := s.writeAEAD
	if aead == nil {
		aead = s.aead
	}
	bp := ssFramePool.Get().(*[]byte)
	defer ssFramePool.Put(bp)
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ssChunkLimit {
			chunk = chunk[:ssChunkLimit]
		}
		length := [2]byte{byte(len(chunk) >> 8), byte(len(chunk))}
		out := aead.Seal((*bp)[:0], fillSSNonce(&s.writeNonceBuf, s.writeNonce), length[:], nil)
		s.writeNonce++
		out = aead.Seal(out, fillSSNonce(&s.writeNonceBuf, s.writeNonce), chunk, nil)
		s.writeNonce++
		if _, err := s.conn.Write(out); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *ssStream) Close() error { return s.conn.Close() }

// CloseWrite 把半关闭透传给底层连接。
//
// 加密流包了一层 net.Conn。不显式转发的话，上层那句
// `conn.(interface{ CloseWrite() error })` 断言就会失败，半关闭语义
// 静默消失——core/ratelimit.go 里警告过的「包一层就丢掉」正是这种情况，
// 只不过丢在了加密层而不是限速层。
func (s *ssStream) CloseWrite() error {
	if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
func (s *ssStream) LocalAddr() net.Addr                { return s.conn.LocalAddr() }
func (s *ssStream) RemoteAddr() net.Addr               { return s.conn.RemoteAddr() }
func (s *ssStream) SetDeadline(t time.Time) error      { return s.conn.SetDeadline(t) }
func (s *ssStream) SetReadDeadline(t time.Time) error  { return s.conn.SetReadDeadline(t) }
func (s *ssStream) SetWriteDeadline(t time.Time) error { return s.conn.SetWriteDeadline(t) }

func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func deriveSSMasterKey(password string, length int) []byte {
	var out []byte
	var previous []byte
	for len(out) < length {
		h := md5.New()
		_, _ = h.Write(previous)
		_, _ = h.Write([]byte(password))
		previous = h.Sum(nil)
		out = append(out, previous...)
	}
	return out[:length]
}

func deriveSSSubkey(master, salt []byte, length int) ([]byte, error) {
	reader := hkdf.New(sha1.New, master, salt, []byte("ss-subkey"))
	key := make([]byte, length)
	_, err := io.ReadFull(reader, key)
	return key, err
}

func makeSSNonce(value uint64) []byte {
	nonce := make([]byte, 12)
	for i := 0; i < len(nonce) && value != 0; i++ {
		nonce[i] = byte(value)
		value >>= 8
	}
	return nonce
}

func (a *shadowsocksAdapter) AddUsers(users []core.User) error {
	validated := make([]ssUserEntry, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" || len(user.UUID) > 256 {
			return fmt.Errorf("shadowsocks user %d password invalid", user.ID)
		}
		master := deriveSSMasterKey(user.UUID, a.method.KeyLen)
		validated = append(validated, ssUserEntry{key: hex.EncodeToString(master), user: user, master: master})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("shadowsocks adapter closed")
	}
	for _, entry := range validated {
		if _, exists := a.users[entry.key]; !exists {
			a.users[entry.key] = &ssUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, MasterKey: entry.master}
		}
	}
	a.publishUsersLocked()
	return nil
}

type ssUserEntry struct {
	key    string
	user   core.User
	master []byte
}

func (a *shadowsocksAdapter) UpsertUsers(users []core.User) error {
	validated := make([]ssUserEntry, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.UUID) == "" || len(user.UUID) > 256 {
			return fmt.Errorf("shadowsocks user %d password invalid", user.ID)
		}
		master := deriveSSMasterKey(user.UUID, a.method.KeyLen)
		validated = append(validated, ssUserEntry{key: hex.EncodeToString(master), user: user, master: master})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("shadowsocks adapter closed")
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.key]; exists {
			a.limiters.Remove(previous.ID)
			previous.retired.Store(true)
		}
		a.users[entry.key] = &ssUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, MasterKey: entry.master}
	}
	a.publishUsersLocked()
	return nil
}

func (a *shadowsocksAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	var removed []int64
	for _, password := range ids {
		key := hex.EncodeToString(deriveSSMasterKey(password, a.method.KeyLen))
		// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着，map 只增不减。
		if entry, ok := a.users[key]; ok {
			a.limiters.Remove(entry.ID)
			entry.retired.Store(true)
			removed = append(removed, entry.ID)
		}
		delete(a.users, key)
	}
	a.publishUsersLocked()
	a.mu.Unlock()
	// 先删表、再踢线（锁外关）：已有的长连接随之断开。
	a.sessions.revoke(removed)
	return nil
}

func (a *shadowsocksAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
}

func (a *shadowsocksAdapter) OnlineIPs() map[int64][]string { return a.online.snapshot() }

func (a *shadowsocksAdapter) addTraffic(user core.User, upload, download int64) {
	a.sessions.add(user.ID, upload, download)
}

func (a *shadowsocksAdapter) Close() error {
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
	a.wg.Wait()
	return nil
}

func (a *shadowsocksAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}
