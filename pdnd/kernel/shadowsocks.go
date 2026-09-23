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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const ssChunkLimit = 16 * 1024

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

type ssUser struct {
	ID          int64
	DeviceLimit int
	// SpeedLimit 之前没存，面板下发的限速到这里就丢了。
	SpeedLimit int
	MasterKey  []byte
}

type shadowsocksAdapter struct {
	protocol string
	spec     InboundSpec
	method   ssMethodSpec
	mu       sync.RWMutex
	users    map[string]ssUser
	traffic  map[int64]core.UserTraffic
	online   map[int64]map[string]struct{}
	plane    DataPlane
	limiters core.SpeedLimiters
	listener net.Listener
	packet   net.PacketConn
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   map[net.Conn]struct{}
	wg       sync.WaitGroup
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
		users: make(map[string]ssUser), traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}),
	}, nil
}

func (a *shadowsocksAdapter) Protocol() string { return a.protocol }

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
	a.spec, a.plane = spec, hooks.DataPlane
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

func (a *shadowsocksAdapter) packetLoop() {
	defer a.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, addr, err := a.packet.ReadFrom(buffer)
		if err != nil {
			a.mu.RLock()
			closed := a.closed
			a.mu.RUnlock()
			if closed || a.ctx.Err() != nil {
				return
			}
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			_ = a.handlePacket(a.ctx, payload, addr)
		}()
	}
}

func (a *shadowsocksAdapter) handlePacket(ctx context.Context, wire []byte, clientAddr net.Addr) error {
	user, destination, payload, err := a.decodeUDPPacket(wire)
	if err != nil {
		return err
	}
	ip := remoteIP(clientAddr)
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("shadowsocks device limit")
	}
	defer a.leaveDevice(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "shadowsocks", SourceIP: sourceIP}
	upstream, err := a.plane.ListenUDP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	destinationAddr, err := destinationUDPAddr(destination)
	if err != nil {
		return err
	}
	if _, err := upstream.WriteTo(payload, destinationAddr); err != nil {
		return err
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	response := make([]byte, 64<<10)
	n, sourceAddr, err := upstream.ReadFrom(response)
	if err != nil {
		return err
	}
	responseDestination := destination
	if sourceAddr != nil {
		responseDestination = destinationFromNetAddr(sourceAddr)
	}
	encoded, err := a.encodeUDPPacket(user, responseDestination, response[:n])
	if err != nil {
		return err
	}
	_, err = a.packet.WriteTo(encoded, clientAddr)
	if err == nil {
		a.addTraffic(user, int64(len(payload)), int64(n))
	}
	return err
}

func destinationUDPAddr(destination vlessDestination) (net.Addr, error) {
	if destination.IP.IsValid() {
		return &net.UDPAddr{IP: net.IP(destination.IP.AsSlice()), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Domain, strconv.Itoa(int(destination.Port))))
}

func (a *shadowsocksAdapter) decodeUDPPacket(wire []byte) (core.User, vlessDestination, []byte, error) {
	var destination vlessDestination
	if len(wire) < a.method.SaltLen+16 {
		return core.User{}, destination, nil, fmt.Errorf("shadowsocks UDP packet too short")
	}
	salt := wire[:a.method.SaltLen]
	ciphertext := wire[a.method.SaltLen:]
	a.mu.RLock()
	users := make([]ssUser, 0, len(a.users))
	for _, user := range a.users {
		users = append(users, user)
	}
	a.mu.RUnlock()
	for _, candidate := range users {
		subkey, err := deriveSSSubkey(candidate.MasterKey, salt, a.method.KeyLen)
		if err != nil {
			continue
		}
		aead, err := a.method.NewAEAD(subkey)
		if err != nil {
			continue
		}
		plain, err := aead.Open(nil, makeSSNonce(0), ciphertext, nil)
		if err != nil {
			continue
		}
		consumed, err := parseSSDestination(plain, &destination)
		if err != nil {
			continue
		}
		return core.User{ID: candidate.ID, DeviceLimit: candidate.DeviceLimit, SpeedLimit: candidate.SpeedLimit}, destination, append([]byte(nil), plain[consumed:]...), nil
	}
	return core.User{}, destination, nil, fmt.Errorf("shadowsocks UDP user authentication failed")
}

func (a *shadowsocksAdapter) encodeUDPPacket(user core.User, destination vlessDestination, payload []byte) ([]byte, error) {
	key := a.userMasterKey(user.ID)
	if len(key) == 0 {
		return nil, fmt.Errorf("shadowsocks UDP user no longer exists")
	}
	salt := make([]byte, a.method.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	subkey, err := deriveSSSubkey(key, salt, a.method.KeyLen)
	if err != nil {
		return nil, err
	}
	aead, err := a.method.NewAEAD(subkey)
	if err != nil {
		return nil, err
	}
	address, err := serializeSSDestination(destination)
	if err != nil {
		return nil, err
	}
	plain := append(address, payload...)
	return append(salt, aead.Seal(nil, makeSSNonce(0), plain, nil)...), nil
}

func (a *shadowsocksAdapter) userMasterKey(id int64) []byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, user := range a.users {
		if user.ID == id {
			return append([]byte(nil), user.MasterKey...)
		}
	}
	return nil
}

func destinationFromNetAddr(addr net.Addr) vlessDestination {
	if udp, ok := addr.(*net.UDPAddr); ok {
		ip, ok := netip.AddrFromSlice(udp.IP)
		if ok {
			return vlessDestination{Host: ip.String(), IP: ip, Port: uint16(udp.Port)}
		}
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return vlessDestination{Host: addr.String()}
	}
	parsed, _ := strconv.ParseUint(port, 10, 16)
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		return vlessDestination{Host: host, IP: ip, Port: uint16(parsed)}
	}
	return vlessDestination{Host: host, Domain: host, Port: uint16(parsed)}
}

func serializeSSDestination(destination vlessDestination) ([]byte, error) {
	var out []byte
	if destination.IP.IsValid() {
		if destination.IP.Is4() {
			out = append(out, 1)
			ip := destination.IP.As4()
			out = append(out, ip[:]...)
		} else {
			out = append(out, 4)
			ip := destination.IP.As16()
			out = append(out, ip[:]...)
		}
	} else {
		host := destination.Domain
		if host == "" {
			host = destination.Host
		}
		if len(host) == 0 || len(host) > 253 {
			return nil, fmt.Errorf("shadowsocks destination domain invalid")
		}
		out = append(out, 3, byte(len(host)))
		out = append(out, host...)
	}
	if destination.Port == 0 {
		return nil, fmt.Errorf("shadowsocks destination port invalid")
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], destination.Port)
	return append(out, port[:]...), nil
}

func (a *shadowsocksAdapter) acceptLoop() {
	defer a.wg.Done()
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			a.mu.RLock()
			closed := a.closed
			a.mu.RUnlock()
			if closed || a.ctx.Err() != nil {
				return
			}
			continue
		}
		a.mu.Lock()
		a.active[conn] = struct{}{}
		a.mu.Unlock()
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			_ = a.handleConn(a.ctx, conn)
		}()
	}
}

func (a *shadowsocksAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	user, stream, destination, err := a.readRequest(conn)
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("shadowsocks device limit")
	}
	defer a.leaveDevice(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "shadowsocks", SourceIP: sourceIP}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		n, _ := core.SpeedLimitedCopy(upstream, stream, a.limiters.For(user))
		a.addTraffic(user, n, 0)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		copyWG.Done()
	}()
	go func() {
		n, _ := core.SpeedLimitedCopy(stream, upstream, a.limiters.For(user))
		a.addTraffic(user, 0, n)
		// 上游关掉写端时，这个关闭要传给客户端，否则它收不到 EOF，
		// 会一直等到自己超时——连接也就一直不释放。
		_ = stream.CloseWrite()
		copyWG.Done()
	}()
	copyWG.Wait()
	return nil
}

func (a *shadowsocksAdapter) readRequest(conn net.Conn) (core.User, *ssStream, vlessDestination, error) {
	var destination vlessDestination
	salt := make([]byte, a.method.SaltLen)
	if _, err := io.ReadFull(conn, salt); err != nil {
		return core.User{}, nil, destination, err
	}
	var encryptedLength [2 + 16]byte
	if _, err := io.ReadFull(conn, encryptedLength[:]); err != nil {
		return core.User{}, nil, destination, err
	}
	a.mu.RLock()
	users := make([]ssUser, 0, len(a.users))
	for _, user := range a.users {
		users = append(users, user)
	}
	a.mu.RUnlock()
	var selected ssUser
	var selectedAEAD cipher.AEAD
	var plainLength [2]byte
	for _, candidate := range users {
		subkey, err := deriveSSSubkey(candidate.MasterKey, salt, a.method.KeyLen)
		if err != nil {
			continue
		}
		aead, err := a.method.NewAEAD(subkey)
		if err != nil {
			continue
		}
		plain, err := aead.Open(nil, makeSSNonce(0), encryptedLength[:], nil)
		if err == nil && len(plain) == 2 {
			copy(plainLength[:], plain)
			length := int(binary.BigEndian.Uint16(plain))
			if length > 0 && length <= ssChunkLimit {
				selected, selectedAEAD = candidate, aead
				break
			}
		}
	}
	if selectedAEAD == nil {
		return core.User{}, nil, destination, fmt.Errorf("shadowsocks user authentication failed")
	}
	length := int(binary.BigEndian.Uint16(plainLength[:]))
	first := make([]byte, length+selectedAEAD.Overhead())
	if _, err := io.ReadFull(conn, first); err != nil {
		return core.User{}, nil, destination, err
	}
	firstPlain, err := selectedAEAD.Open(nil, makeSSNonce(1), first, nil)
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
	responseSubkey, err := deriveSSSubkey(selected.MasterKey, responseSalt, a.method.KeyLen)
	if err != nil {
		return core.User{}, nil, destination, err
	}
	responseAEAD, err := a.method.NewAEAD(responseSubkey)
	if err != nil {
		return core.User{}, nil, destination, err
	}
	if _, err := conn.Write(responseSalt); err != nil {
		return core.User{}, nil, destination, err
	}
	stream := &ssStream{conn: conn, aead: selectedAEAD, writeAEAD: responseAEAD, readNonce: 2, writeNonce: 0, pending: append([]byte(nil), firstPlain[consumed:]...)}
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
	pending    []byte
	writeMu    sync.Mutex
}

func (s *ssStream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	var encryptedLength [2 + 16]byte
	if _, err := io.ReadFull(s.conn, encryptedLength[:]); err != nil {
		return 0, err
	}
	plainLength, err := s.aead.Open(nil, makeSSNonce(s.readNonce), encryptedLength[:], nil)
	s.readNonce++
	if err != nil || len(plainLength) != 2 {
		return 0, fmt.Errorf("shadowsocks frame length authentication failed")
	}
	length := int(binary.BigEndian.Uint16(plainLength))
	if length == 0 || length > ssChunkLimit {
		return 0, fmt.Errorf("shadowsocks frame length invalid")
	}
	frame := make([]byte, length+s.aead.Overhead())
	if _, err := io.ReadFull(s.conn, frame); err != nil {
		return 0, err
	}
	plain, err := s.aead.Open(nil, makeSSNonce(s.readNonce), frame, nil)
	s.readNonce++
	if err != nil {
		return 0, fmt.Errorf("shadowsocks frame authentication failed")
	}
	n := copy(p, plain)
	if n < len(plain) {
		s.pending = append(s.pending, plain[n:]...)
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
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ssChunkLimit {
			chunk = chunk[:ssChunkLimit]
		}
		length := [2]byte{byte(len(chunk) >> 8), byte(len(chunk))}
		header := aead.Seal(nil, makeSSNonce(s.writeNonce), length[:], nil)
		s.writeNonce++
		body := aead.Seal(nil, makeSSNonce(s.writeNonce), chunk, nil)
		s.writeNonce++
		if _, err := s.conn.Write(append(header, body...)); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *ssStream) Close() error                       { return s.conn.Close() }

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
			a.users[entry.key] = ssUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, MasterKey: entry.master}
		}
	}
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
		}
		a.users[entry.key] = ssUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, MasterKey: entry.master}
	}
	return nil
}

func (a *shadowsocksAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, password := range ids {
		key := hex.EncodeToString(deriveSSMasterKey(password, a.method.KeyLen))
		// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着，map 只增不减。
		if entry, ok := a.users[key]; ok {
			a.limiters.Remove(entry.ID)
		}
		delete(a.users, key)
	}
	return nil
}

func (a *shadowsocksAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
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

func (a *shadowsocksAdapter) OnlineIPs() map[int64][]string {
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

func (a *shadowsocksAdapter) enterDevice(user core.User, ip string) bool {
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

func (a *shadowsocksAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *shadowsocksAdapter) addTraffic(user core.User, upload, download int64) {
	a.mu.Lock()
	traffic := a.traffic[user.ID]
	traffic.ID = user.ID
	traffic.Upload += upload
	traffic.Download += download
	a.traffic[user.ID] = traffic
	a.mu.Unlock()
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
