package kernel

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

const (
	ss2022ClientHeader   byte = 0
	ss2022ServerHeader   byte = 1
	ss2022MaxChunk            = 16*1024 - 1
	ss2022Overhead            = 16
	ss2022UDPIdleTimeout      = 5 * time.Minute
)

type ss2022Spec struct {
	name    string
	keyLen  int
	newAEAD func([]byte) (cipher.AEAD, error)
}

func parseSS2022Spec(method string) (ss2022Spec, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "2022-blake3-aes-128-gcm":
		return ss2022Spec{name: method, keyLen: 16, newAEAD: newAESGCM}, nil
	case "2022-blake3-aes-256-gcm":
		return ss2022Spec{name: method, keyLen: 32, newAEAD: newAESGCM}, nil
	case "2022-blake3-chacha20-poly1305":
		return ss2022Spec{name: method, keyLen: 32, newAEAD: chacha20poly1305.New}, nil
	default:
		return ss2022Spec{}, fmt.Errorf("native shadowsocks 2022 method %q is unsupported", method)
	}
}

type ss2022Adapter struct {
	spec     InboundSpec
	method   ss2022Spec
	psk      []byte
	psks     [][]byte
	mu       sync.RWMutex
	user     core.User
	hasUser  bool
	sessions userSessions
	limiters core.SpeedLimiters
	online   onlineDevices
	listener net.Listener
	plane    DataPlane
	connErr  connErrorReporter
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	lastErr  error
	active   map[net.Conn]struct{}
	packet   net.PacketConn
	udpMu    sync.Mutex
	udp      map[string]*ss2022UDPSession
	wg       sync.WaitGroup
}

type ss2022UDPSession struct {
	psk          []byte
	clientID     uint64
	serverID     uint64
	remote       cipher.AEAD
	local        cipher.AEAD
	user         core.User
	lastClientID uint64
	nextServerID uint64
	clientSeen   bool
	lastSeen     time.Time
}

func newSS2022Adapter(spec InboundSpec) (Adapter, error) {
	method, _ := spec.Config.Raw["method"].(string)
	parsed, err := parseSS2022Spec(method)
	if err != nil {
		return nil, err
	}
	password, _ := spec.Config.Raw["password"].(string)
	if strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("shadowsocks 2022 requires base64 password PSK")
	}
	parts := strings.Split(password, ":")
	psks := make([][]byte, 0, len(parts))
	for _, part := range parts {
		psk, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(part))
		if decodeErr != nil {
			return nil, fmt.Errorf("shadowsocks 2022 password base64: %w", decodeErr)
		}
		if len(psk) < parsed.keyLen {
			return nil, fmt.Errorf("shadowsocks 2022 PSK is shorter than %d bytes", parsed.keyLen)
		}
		if len(psk) > parsed.keyLen {
			psk = ss2022Key(psk, parsed.keyLen)
		}
		psks = append(psks, psk)
	}
	if len(psks) == 0 {
		return nil, fmt.Errorf("shadowsocks 2022 requires at least one PSK")
	}
	return &ss2022Adapter{spec: spec, method: parsed, psk: psks[len(psks)-1], psks: psks, active: make(map[net.Conn]struct{}), udp: make(map[string]*ss2022UDPSession)}, nil
}

func (a *ss2022Adapter) Protocol() string {
	return strings.ToLower(strings.TrimSpace(a.spec.Config.Protocol))
}

func (a *ss2022Adapter) Validate(spec InboundSpec) error {
	method, _ := spec.Config.Raw["method"].(string)
	_, err := parseSS2022Spec(method)
	if err != nil {
		return err
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("shadowsocks 2022 port is invalid")
	}
	network, _ := spec.Config.Raw["network"].(string)
	if network != "" && !strings.EqualFold(network, "tcp") && !strings.EqualFold(network, "udp") {
		return fmt.Errorf("native shadowsocks 2022 accepts tcp or udp")
	}
	password, ok := spec.Config.Raw["password"].(string)
	if !ok || strings.TrimSpace(password) == "" {
		return fmt.Errorf("shadowsocks 2022 password is required")
	}
	return nil
}

func (a *ss2022Adapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("shadowsocks 2022 start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("shadowsocks 2022 adapter already started or closed")
	}
	a.spec, a.plane = spec, hooks.DataPlane
	a.connErr = newConnErrorReporter(hooks, spec, a.Protocol())
	a.ctx, a.cancel = context.WithCancel(parent)
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	network, _ := spec.Config.Raw["network"].(string)
	if strings.EqualFold(network, "udp") {
		packet, err := net.ListenPacket("udp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
		if err != nil {
			a.cancel()
			a.mu.Unlock()
			return err
		}
		a.packet = packet
		a.mu.Unlock()
		a.wg.Add(1)
		go a.udpLoop()
		a.wg.Add(1)
		go a.udpSessionGC()
		return nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
	if err != nil {
		a.cancel()
		a.mu.Unlock()
		return err
	}
	a.listener = ln
	a.mu.Unlock()
	a.wg.Add(1)
	go a.acceptLoop()
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
			if err := a.handleConn(ctx, conn); err != nil {
				a.recordError(err)
			}
		})
	})
}

// handleConn 是 TCP 会话入口，会话层失败在这里上报。协议名与注册表一致：
// 2022 方法与经典 AEAD 在面板里是同一个协议，只是 method 不同。
func (a *ss2022Adapter) handleConn(ctx context.Context, conn net.Conn) error {
	err := a.serveConn(ctx, conn)
	a.connErr.conn(StageSession, conn, err)
	return err
}

func (a *ss2022Adapter) serveConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	epoch := a.sessions.epoch()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// 读缓冲只给请求头用：之后转发每次读 2KB 小块走它、32KB 大块直接读底层
	// （bufio 对不小于缓冲的读直通）。原先 64KB 常驻，是每连接内存的大头之一。
	r := bufio.NewReaderSize(conn, ssHeaderReadBuffer)
	keyLen := a.method.keyLen
	salt := make([]byte, keyLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return err
	}
	if len(a.psks) > 1 {
		identity := make([]byte, (len(a.psks)-1)*aes.BlockSize)
		if _, err := io.ReadFull(r, identity); err != nil {
			return err
		}
		if err := ss2022ValidateIdentityHeaders(identity, salt, a.psks); err != nil {
			return err
		}
	}
	readAEAD, err := a.method.newAEAD(ss2022SessionKey(a.psk, salt, keyLen))
	if err != nil {
		return err
	}
	fixed := make([]byte, 11)
	readNonce := make([]byte, readAEAD.NonceSize())
	if err := ss2022ReadRawChunk(r, readAEAD, readNonce, fixed); err != nil {
		return err
	}
	ss2022IncNonce(readNonce)
	stream := &ss2022Stream{reader: r, readAEAD: readAEAD, readNonce: readNonce}
	if fixed[0] != ss2022ClientHeader {
		return fmt.Errorf("shadowsocks 2022 header type %d is not client", fixed[0])
	}
	stamp := int64(binary.BigEndian.Uint64(fixed[1:9]))
	if delta := time.Now().Unix() - stamp; delta > 30 || delta < -30 {
		return fmt.Errorf("shadowsocks 2022 timestamp outside 30 seconds")
	}
	variableLen := int(binary.BigEndian.Uint16(fixed[9:11]))
	if variableLen < 4 || variableLen > 65535 {
		return fmt.Errorf("shadowsocks 2022 variable header length invalid")
	}
	variable := make([]byte, variableLen)
	if err := ss2022ReadRawChunk(r, readAEAD, stream.readNonce, variable); err != nil {
		return err
	}
	ss2022IncNonce(stream.readNonce)
	var destination vlessDestination
	consumed, err := parseSSDestination(variable, &destination)
	if err != nil {
		return err
	}
	if consumed+2 > len(variable) {
		return io.ErrUnexpectedEOF
	}
	paddingLen := int(binary.BigEndian.Uint16(variable[consumed : consumed+2]))
	if paddingLen < 1 || consumed+2+paddingLen > len(variable) {
		return fmt.Errorf("shadowsocks 2022 padding invalid")
	}
	stream.pending = append(stream.pending, variable[consumed+2+paddingLen:]...)
	_ = conn.SetReadDeadline(time.Time{})
	a.mu.RLock()
	user, hasUser := a.user, a.hasUser
	a.mu.RUnlock()
	if !hasUser {
		return fmt.Errorf("shadowsocks 2022 has no configured user")
	}
	sess := a.sessions.open(user, epoch, conn)
	if sess == nil {
		return errSessionRevoked
	}
	defer sess.close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.online.enter(user, ip) {
		return deviceLimitError("shadowsocks 2022")
	}
	defer a.online.leave(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	var sourcePort uint16
	if _, p, e := net.SplitHostPort(conn.RemoteAddr().String()); e == nil {
		if n, e := strconv.ParseUint(p, 10, 16); e == nil {
			sourcePort = uint16(n)
		}
	}
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "shadowsocks-2022", SourceIP: sourceIP, SourcePort: sourcePort}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	responseSalt := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, responseSalt); err != nil {
		return err
	}
	writeAEAD, err := a.method.newAEAD(ss2022SessionKey(a.psk, responseSalt, keyLen))
	if err != nil {
		return err
	}
	responseFixed := make([]byte, 1+8+keyLen+2)
	responseFixed[0] = ss2022ServerHeader
	binary.BigEndian.PutUint64(responseFixed[1:9], uint64(time.Now().Unix()))
	copy(responseFixed[9:9+keyLen], salt)
	binary.BigEndian.PutUint16(responseFixed[9+keyLen:], 0)
	if _, err := conn.Write(responseSalt); err != nil {
		return err
	}
	writeNonce := make([]byte, writeAEAD.NonceSize())
	responseWire := writeAEAD.Seal(nil, writeNonce, responseFixed, nil)
	if _, err := conn.Write(responseWire); err != nil {
		return err
	}
	ss2022IncNonce(writeNonce)
	// The reference client always consumes the response payload chunk, even
	// when its declared length is zero. Emit an authenticated empty raw chunk
	// so the following stream frames start at the expected nonce.
	emptyWire := writeAEAD.Seal(nil, writeNonce, nil, nil)
	if _, err := conn.Write(emptyWire); err != nil {
		return err
	}
	ss2022IncNonce(writeNonce)
	responseStream := &ss2022Stream{writer: conn, writeAEAD: writeAEAD, writeNonce: writeNonce}
	client := &core.SplitStream{R: stream, W: responseStream, C: conn}
	sess.relay(client, upstream, core.RelayOptions{Limiter: a.limiters.For(user)})
	return nil
}

func isConnClosedError(err error) bool {
	return err == net.ErrClosed || strings.Contains(strings.ToLower(err.Error()), "closed") || strings.Contains(strings.ToLower(err.Error()), "reset")
}

func (a *ss2022Adapter) recordError(err error) {
	a.mu.Lock()
	if a.lastErr == nil {
		a.lastErr = err
	}
	a.mu.Unlock()
}

func (a *ss2022Adapter) AddUsers(users []core.User) error {
	if len(users) > 1 {
		return fmt.Errorf("native shadowsocks 2022 single-PSK slice accepts one user; relay identities are not silently collapsed")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("shadowsocks 2022 adapter closed")
	}
	if len(users) == 0 {
		a.hasUser = false
		return nil
	}
	a.user, a.hasUser = users[0], true
	return nil
}
func (a *ss2022Adapter) UpsertUsers(users []core.User) error {
	if len(users) > 1 {
		return fmt.Errorf("native shadowsocks 2022 single-PSK slice accepts one user; relay identities are not silently collapsed")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("shadowsocks 2022 adapter closed")
	}
	if len(users) == 0 {
		a.hasUser = false
		return nil
	}
	a.user, a.hasUser = users[0], true
	return nil
}

func (a *ss2022Adapter) DelUsers(ids []string) error {
	a.mu.Lock()
	var removed []int64
	if a.hasUser {
		for _, id := range ids {
			if id == a.user.UUID {
				a.hasUser = false
				removed = append(removed, a.user.ID)
				break
			}
		}
	}
	a.mu.Unlock()
	// 先删表、再踢线（锁外关）：已有的长连接、mux / QUIC 会话随之断开。
	a.sessions.revoke(removed)
	return nil
}
func (a *ss2022Adapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
}
func (a *ss2022Adapter) OnlineIPs() map[int64][]string { return a.online.snapshot() }
func (a *ss2022Adapter) addTraffic(user core.User, upload, download int64) {
	a.sessions.add(user.ID, upload, download)
}
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
	ln := a.listener
	packet := a.packet
	active := make([]net.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	a.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
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
func (a *ss2022Adapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}
