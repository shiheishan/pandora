package kernel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/crypto/chacha20poly1305"
	"lukechampine.com/blake3"
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
	traffic  map[int64]core.UserTraffic
	online   map[int64]map[string]struct{}
	listener net.Listener
	plane    DataPlane
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
	return &ss2022Adapter{spec: spec, method: parsed, psk: psks[len(psks)-1], psks: psks, traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}), udp: make(map[string]*ss2022UDPSession)}, nil
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

func (a *ss2022Adapter) udpSessionGC() {
	defer a.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.cleanupUDPSessions(time.Now())
		case <-a.ctx.Done():
			return
		}
	}
}

func (a *ss2022Adapter) cleanupUDPSessions(now time.Time) {
	a.udpMu.Lock()
	for key, session := range a.udp {
		if !session.lastSeen.IsZero() && now.Sub(session.lastSeen) > ss2022UDPIdleTimeout {
			delete(a.udp, key)
		}
	}
	a.udpMu.Unlock()
}

func (a *ss2022Adapter) acceptLoop() {
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
			if err := a.handleConn(a.ctx, conn); err != nil {
				a.recordError(err)
			}
		}()
	}
}

func (a *ss2022Adapter) udpLoop() {
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
		wire := append([]byte(nil), buffer[:n]...)
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			_ = a.handleUDPPacket(a.ctx, wire, addr)
		}()
	}
}

func (a *ss2022Adapter) handleUDPPacket(ctx context.Context, wire []byte, clientAddr net.Addr) error {
	chachaUDP := strings.Contains(a.method.name, "chacha20")
	if len(wire) < aes.BlockSize+ss2022Overhead && !chachaUDP || chachaUDP && len(wire) < chacha20poly1305.NonceSizeX+16+ss2022Overhead {
		return fmt.Errorf("shadowsocks 2022 UDP packet too short")
	}
	var clientID, packetID uint64
	var remote cipher.AEAD
	var plain []byte
	var packetPSK []byte
	var authErr error
	for _, candidate := range a.psks {
		if chachaUDP {
			udpCipher, err := chacha20poly1305.NewX(candidate)
			if err != nil {
				authErr = err
				continue
			}
			candidatePlain, err := udpCipher.Open(nil, wire[:chacha20poly1305.NonceSizeX], wire[chacha20poly1305.NonceSizeX:], nil)
			if err != nil {
				authErr = err
				continue
			}
			if len(candidatePlain) < 16 {
				authErr = fmt.Errorf("shadowsocks 2022 UDP packet session header missing")
				continue
			}
			clientID = binary.BigEndian.Uint64(candidatePlain[:8])
			packetID = binary.BigEndian.Uint64(candidatePlain[8:16])
			plain = candidatePlain[16:]
			packetPSK = candidate
			break
		}
		block, err := aes.NewCipher(candidate)
		if err != nil {
			authErr = err
			continue
		}
		packetHeader := append([]byte(nil), wire[:aes.BlockSize]...)
		block.Decrypt(packetHeader, packetHeader)
		candidateRemote, err := a.method.newAEAD(ss2022SessionKey(candidate, packetHeader[:8], a.method.keyLen))
		if err != nil {
			authErr = err
			continue
		}
		candidatePlain, err := candidateRemote.Open(nil, packetHeader[4:16], wire[aes.BlockSize:], nil)
		if err != nil {
			authErr = err
			continue
		}
		clientID = binary.BigEndian.Uint64(packetHeader[:8])
		packetID = binary.BigEndian.Uint64(packetHeader[8:16])
		plain = candidatePlain
		remote = candidateRemote
		packetPSK = candidate
		break
	}
	if packetPSK == nil {
		if authErr == nil {
			authErr = fmt.Errorf("no configured PSK")
		}
		return fmt.Errorf("shadowsocks 2022 UDP packet authentication failed: %w", authErr)
	}
	if len(plain) < 11 || plain[0] != ss2022ClientHeader {
		return fmt.Errorf("shadowsocks 2022 UDP header invalid")
	}
	stamp := int64(binary.BigEndian.Uint64(plain[1:9]))
	if delta := time.Now().Unix() - stamp; delta > 30 || delta < -30 {
		return fmt.Errorf("shadowsocks 2022 UDP timestamp outside 30 seconds")
	}
	paddingLen := int(binary.BigEndian.Uint16(plain[9:11]))
	if paddingLen < 0 || 11+paddingLen >= len(plain) {
		return fmt.Errorf("shadowsocks 2022 UDP padding invalid")
	}
	var destination vlessDestination
	consumed, err := parseSSDestination(plain[11+paddingLen:], &destination)
	if err != nil {
		return err
	}
	payloadOffset := 11 + paddingLen + consumed
	if payloadOffset > len(plain) {
		return io.ErrUnexpectedEOF
	}
	payload := plain[payloadOffset:]
	ip := remoteIP(clientAddr)
	a.mu.RLock()
	user, hasUser := a.user, a.hasUser
	a.mu.RUnlock()
	if !hasUser {
		return fmt.Errorf("shadowsocks 2022 has no configured user")
	}
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("shadowsocks 2022 device limit")
	}
	defer a.leaveDevice(user, ip)
	sessionKey := clientAddr.String() + "#" + strconv.FormatUint(clientID, 10)
	a.udpMu.Lock()
	session := a.udp[sessionKey]
	if session == nil {
		var idBytes [8]byte
		if _, err := io.ReadFull(rand.Reader, idBytes[:]); err != nil {
			a.udpMu.Unlock()
			return err
		}
		serverID := binary.BigEndian.Uint64(idBytes[:])
		var local cipher.AEAD
		if !chachaUDP {
			localKey := make([]byte, a.method.keyLen)
			binary.BigEndian.PutUint64(localKey[:8], serverID)
			local, err = a.method.newAEAD(ss2022SessionKey(a.psk, localKey[:8], a.method.keyLen))
			if err != nil {
				a.udpMu.Unlock()
				return err
			}
		}
		session = &ss2022UDPSession{psk: append([]byte(nil), packetPSK...), clientID: clientID, serverID: serverID, remote: remote, local: local, user: user, lastSeen: time.Now()}
		a.udp[sessionKey] = session
	} else if !bytes.Equal(session.psk, packetPSK) {
		a.udpMu.Unlock()
		return fmt.Errorf("shadowsocks 2022 UDP PSK changed for session")
	}
	if session.clientSeen && packetID <= session.lastClientID {
		a.udpMu.Unlock()
		return fmt.Errorf("shadowsocks 2022 UDP packet replay")
	}
	session.lastClientID = packetID
	session.clientSeen = true
	session.lastSeen = time.Now()
	a.udpMu.Unlock()
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "shadowsocks-2022", SourceIP: sourceIP}
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
	encoded, err := a.encodeSS2022UDPPacket(session, responseDestination, response[:n])
	if err != nil {
		return err
	}
	if _, err := a.packet.WriteTo(encoded, clientAddr); err != nil {
		return err
	}
	a.addTraffic(user, int64(len(payload)), int64(n))
	return nil
}

func (a *ss2022Adapter) encodeSS2022UDPPacket(session *ss2022UDPSession, destination vlessDestination, payload []byte) ([]byte, error) {
	address, err := serializeSSDestination(destination)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, 0, 19+len(address)+len(payload))
	plain = append(plain, ss2022ServerHeader)
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(time.Now().Unix()))
	plain = append(plain, stamp[:]...)
	var remoteID [8]byte
	binary.BigEndian.PutUint64(remoteID[:], session.clientID)
	plain = append(plain, remoteID[:]...)
	plain = append(plain, make([]byte, 2)...)
	plain = append(plain, address...)
	plain = append(plain, payload...)
	if strings.Contains(a.method.name, "chacha20") {
		udpCipher, err := chacha20poly1305.NewX(session.psk)
		if err != nil {
			return nil, err
		}
		var sessionHeader [16]byte
		binary.BigEndian.PutUint64(sessionHeader[:8], session.clientID)
		a.udpMu.Lock()
		responseID := session.nextServerID
		session.nextServerID++
		a.udpMu.Unlock()
		binary.BigEndian.PutUint64(sessionHeader[8:], responseID)
		body := append(sessionHeader[:0:0], sessionHeader[:]...)
		body = append(body, plain...)
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, err
		}
		return append(nonce, udpCipher.Seal(nil, nonce, body, nil)...), nil
	}
	var header [16]byte
	binary.BigEndian.PutUint64(header[:8], session.serverID)
	a.udpMu.Lock()
	responseID := session.nextServerID
	session.nextServerID++
	a.udpMu.Unlock()
	binary.BigEndian.PutUint64(header[8:], responseID)
	ciphertext := session.local.Seal(nil, header[4:16], plain, nil)
	block, err := aes.NewCipher(session.psk)
	if err != nil {
		return nil, err
	}
	block.Encrypt(header[:], header[:])
	return append(header[:], ciphertext...), nil
}

func (a *ss2022Adapter) handleConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReaderSize(conn, 64*1024)
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
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("shadowsocks 2022 device limit")
	}
	defer a.leaveDevice(user, ip)
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
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		n, copyErr := io.Copy(upstream, stream)
		if copyErr != nil && !isConnClosedError(copyErr) {
			a.recordError(copyErr)
		}
		a.addTraffic(user, n, 0)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		wg.Done()
	}()
	go func() {
		n, copyErr := io.Copy(responseStream, upstream)
		if copyErr != nil && !isConnClosedError(copyErr) {
			a.recordError(copyErr)
		}
		a.addTraffic(user, 0, n)
		wg.Done()
	}()
	wg.Wait()
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
	defer a.mu.Unlock()
	if a.hasUser {
		for _, id := range ids {
			if id == a.user.UUID {
				a.hasUser = false
				break
			}
		}
	}
	return nil
}
func (a *ss2022Adapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, t := range a.traffic {
		if t.Upload != 0 || t.Download != 0 {
			out = append(out, t)
		}
		delete(a.traffic, id)
	}
	return out, nil
}
func (a *ss2022Adapter) OnlineIPs() map[int64][]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[int64][]string, len(a.online))
	for id, set := range a.online {
		for ip := range set {
			out[id] = append(out[id], ip)
		}
	}
	return out
}
func (a *ss2022Adapter) enterDevice(user core.User, ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.online[user.ID]
	if set == nil {
		set = make(map[string]struct{})
		a.online[user.ID] = set
	}
	if _, ok := set[ip]; !ok && user.DeviceLimit > 0 && len(set) >= user.DeviceLimit {
		return false
	}
	set[ip] = struct{}{}
	return true
}
func (a *ss2022Adapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}
func (a *ss2022Adapter) addTraffic(user core.User, upload, download int64) {
	a.mu.Lock()
	t := a.traffic[user.ID]
	t.ID = user.ID
	t.Upload += upload
	t.Download += download
	a.traffic[user.ID] = t
	a.mu.Unlock()
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

func ss2022Key(key []byte, length int) []byte {
	sum := sha256.Sum256(key)
	return append([]byte(nil), sum[:length]...)
}
func ss2022SessionKey(psk, salt []byte, length int) []byte {
	out := make([]byte, length)
	material := make([]byte, len(psk)+len(salt))
	copy(material, psk)
	copy(material[len(psk):], salt)
	blake3.DeriveKey(out, "shadowsocks 2022 session subkey", material)
	return out
}

func ss2022ValidateIdentityHeaders(wire, salt []byte, psks [][]byte) error {
	if len(wire) != (len(psks)-1)*aes.BlockSize {
		return fmt.Errorf("shadowsocks 2022 identity header length invalid")
	}
	for i := 0; i < len(psks)-1; i++ {
		block, err := aes.NewCipher(ss2022IdentitySubkey(psks[i], salt, len(psks[i])))
		if err != nil {
			return err
		}
		plain := make([]byte, aes.BlockSize)
		block.Decrypt(plain, wire[i*aes.BlockSize:(i+1)*aes.BlockSize])
		expectedHash := blake3.Sum512(psks[i+1])
		if subtle.ConstantTimeCompare(plain, expectedHash[:aes.BlockSize]) != 1 {
			return fmt.Errorf("shadowsocks 2022 identity header authentication failed")
		}
	}
	return nil
}

func ss2022IdentitySubkey(psk, salt []byte, length int) []byte {
	material := make([]byte, len(psk)+len(salt))
	copy(material, psk)
	copy(material[len(psk):], salt)
	out := make([]byte, length)
	blake3.DeriveKey(out, "shadowsocks 2022 identity subkey", material)
	return out
}

type ss2022Stream struct {
	reader                *bufio.Reader
	writer                io.Writer
	readAEAD, writeAEAD   cipher.AEAD
	readNonce, writeNonce []byte
	pending               []byte
	mu                    sync.Mutex
}

func ss2022ReadRawChunk(r io.Reader, aead cipher.AEAD, nonce []byte, dst []byte) error {
	wire := make([]byte, len(dst)+aead.Overhead())
	if _, err := io.ReadFull(r, wire); err != nil {
		return err
	}
	plain, err := aead.Open(nil, nonce, wire, nil)
	if err != nil {
		return fmt.Errorf("shadowsocks 2022 raw chunk authentication failed: %w", err)
	}
	if len(plain) != len(dst) {
		return fmt.Errorf("shadowsocks 2022 raw chunk length %d, want %d", len(plain), len(dst))
	}
	copy(dst, plain)
	return nil
}

func (s *ss2022Stream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	var encryptedLength [2 + ss2022Overhead]byte
	if _, err := io.ReadFull(s.reader, encryptedLength[:]); err != nil {
		return 0, err
	}
	plainLength, err := s.readAEAD.Open(nil, s.readNonce, encryptedLength[:], nil)
	ss2022IncNonce(s.readNonce)
	if err != nil || len(plainLength) != 2 {
		return 0, fmt.Errorf("shadowsocks 2022 length authentication failed")
	}
	length := int(binary.BigEndian.Uint16(plainLength))
	if length == 0 || length > ss2022MaxChunk {
		return 0, fmt.Errorf("shadowsocks 2022 frame length invalid")
	}
	frame := make([]byte, length+ss2022Overhead)
	if _, err := io.ReadFull(s.reader, frame); err != nil {
		return 0, err
	}
	plain, err := s.readAEAD.Open(nil, s.readNonce, frame, nil)
	ss2022IncNonce(s.readNonce)
	if err != nil {
		return 0, fmt.Errorf("shadowsocks 2022 frame authentication failed")
	}
	n := copy(p, plain)
	if n < len(plain) {
		s.pending = append(s.pending, plain[n:]...)
	}
	return n, nil
}
func (s *ss2022Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ss2022MaxChunk {
			chunk = chunk[:ss2022MaxChunk]
		}
		length := []byte{byte(len(chunk) >> 8), byte(len(chunk))}
		header := s.writeAEAD.Seal(nil, s.writeNonce, length, nil)
		ss2022IncNonce(s.writeNonce)
		body := s.writeAEAD.Seal(nil, s.writeNonce, chunk, nil)
		ss2022IncNonce(s.writeNonce)
		if _, err := s.writer.Write(append(header, body...)); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}
func ss2022IncNonce(nonce []byte) {
	for i := range nonce {
		nonce[i]++
		if nonce[i] != 0 {
			return
		}
	}
}
