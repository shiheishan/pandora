package kernel

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
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
	quic "github.com/apernet/quic-go"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	juicityVersion      byte = 0
	juicityAuthenticate byte = 0
	juicityNetworkTCP   byte = 1
	juicityNetworkUDP   byte = 3
	juicityAuthBytes         = 2 + 16 + 32
	juicityMaxPacket         = 64 << 10
)

type juicityUser struct {
	user   core.User
	active bool
}

type juicityAdapter struct {
	spec InboundSpec

	mu        sync.RWMutex
	users     map[string]juicityUser
	traffic   map[int64]core.UserTraffic
	online    map[int64]map[string]struct{}
	plane     DataPlane
	limiters  core.SpeedLimiters
	packet    net.PacketConn
	transport *quic.Transport
	listener  *quic.Listener
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	active    map[*quic.Conn]struct{}
	wg        sync.WaitGroup
}

var _ Adapter = (*juicityAdapter)(nil)

func newJuicityAdapter(spec InboundSpec) (Adapter, error) {
	return &juicityAdapter{
		spec:    spec,
		users:   make(map[string]juicityUser),
		traffic: make(map[int64]core.UserTraffic),
		online:  make(map[int64]map[string]struct{}),
		active:  make(map[*quic.Conn]struct{}),
	}, nil
}

func (a *juicityAdapter) Protocol() string { return "juicity" }

func (a *juicityAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "juicity") {
		return fmt.Errorf("juicity adapter received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("juicity port is invalid")
	}
	if network := strings.TrimSpace(rawString(spec.Config.Raw, "network")); network != "" && !strings.EqualFold(network, "udp") {
		return fmt.Errorf("juicity requires UDP transport, got %q", network)
	}
	if strings.TrimSpace(rawString(spec.Config.Raw, "cert_path")) == "" || strings.TrimSpace(rawString(spec.Config.Raw, "key_path")) == "" {
		return fmt.Errorf("juicity requires cert_path and key_path")
	}
	if cc := strings.ToLower(strings.TrimSpace(rawString(spec.Config.Raw, "congestion_control"))); cc != "" && cc != "cubic" && cc != "new_reno" && cc != "bbr" {
		return fmt.Errorf("juicity unsupported congestion_control %q", cc)
	}
	return nil
}

func (a *juicityAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("juicity requires context and data plane")
	}
	if err := a.Validate(spec); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(rawString(spec.Config.Raw, "cert_path"), rawString(spec.Config.Raw, "key_path"))
	if err != nil {
		return fmt.Errorf("juicity TLS certificate: %w", err)
	}
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	packet, err := net.ListenPacket("udp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}}
	transport := &quic.Transport{Conn: packet}
	listener, err := transport.Listen(tlsConfig, &quic.Config{
		MaxIncomingStreams:    100,
		MaxIncomingUniStreams: 100,
		KeepAlivePeriod:       10 * time.Second,
		HandshakeIdleTimeout:  10 * time.Second,
		MaxIdleTimeout:        2 * time.Minute,
	})
	if err != nil {
		cancel()
		_ = packet.Close()
		return fmt.Errorf("juicity QUIC listener: %w", err)
	}
	a.mu.Lock()
	if a.closed || a.listener != nil {
		a.mu.Unlock()
		cancel()
		_ = listener.Close()
		_ = transport.Close()
		return fmt.Errorf("juicity adapter already started or closed")
	}
	a.spec, a.plane, a.packet, a.transport, a.listener, a.ctx, a.cancel = spec, hooks.DataPlane, packet, transport, listener, ctx, cancel
	a.wg.Add(1)
	a.mu.Unlock()
	go a.acceptLoop()
	return nil
}

func (a *juicityAdapter) acceptLoop() {
	defer a.wg.Done()
	for {
		a.mu.RLock()
		listener, ctx := a.listener, a.ctx
		a.mu.RUnlock()
		if listener == nil || ctx == nil {
			return
		}
		conn, err := listener.Accept(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			return
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = conn.CloseWithError(0, "server closed")
			continue
		}
		a.active[conn] = struct{}{}
		a.wg.Add(1)
		a.mu.Unlock()
		go func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			a.handleConn(conn)
		}()
	}
}

func (a *juicityAdapter) handleConn(conn *quic.Conn) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		_ = conn.CloseWithError(0, "server unavailable")
		return
	}
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	uni, err := conn.AcceptUniStream(authCtx)
	cancel()
	if err != nil {
		_ = conn.CloseWithError(0x100, "authentication stream required")
		return
	}
	user, err := a.authenticate(conn, uni)
	if err != nil {
		_ = conn.CloseWithError(0x101, "authentication failed")
		return
	}
	// The official client keeps this stream open for its optional underlay
	// authentication. Pandora's native stream/packet data plane does not use
	// that extension, but drains it so the peer can close cleanly.
	go func() { _, _ = io.Copy(io.Discard, uni) }()
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		_ = conn.CloseWithError(0x102, "device limit")
		return
	}
	defer a.leaveDevice(user, ip)
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		a.mu.Lock()
		a.wg.Add(1)
		a.mu.Unlock()
		go func() {
			defer a.wg.Done()
			defer stream.Close()
			a.handleStream(ctx, conn, stream, user, ip)
		}()
	}
}

func (a *juicityAdapter) authenticate(conn *quic.Conn, stream *quic.ReceiveStream) (core.User, error) {
	var auth [juicityAuthBytes]byte
	if _, err := io.ReadFull(stream, auth[:]); err != nil {
		return core.User{}, err
	}
	if auth[0] != juicityVersion || auth[1] != juicityAuthenticate {
		return core.User{}, fmt.Errorf("unsupported juicity authentication header")
	}
	id, err := uuid.FromBytes(auth[2:18])
	if err != nil {
		return core.User{}, err
	}
	a.mu.RLock()
	entry, ok := a.users[id.String()]
	a.mu.RUnlock()
	if !ok || !entry.active {
		return core.User{}, fmt.Errorf("unknown juicity user")
	}
	state := conn.ConnectionState().TLS
	token, err := state.ExportKeyingMaterial(string(auth[2:18]), []byte(entry.user.UUID), 32)
	if err != nil {
		return core.User{}, err
	}
	if subtle.ConstantTimeCompare(token, auth[juicityAuthBytes-32:]) != 1 {
		return core.User{}, fmt.Errorf("invalid juicity token")
	}
	return entry.user, nil
}

func (a *juicityAdapter) handleStream(ctx context.Context, conn *quic.Conn, stream *quic.Stream, user core.User, ip string) {
	var network [1]byte
	if _, err := io.ReadFull(stream, network[:]); err != nil {
		return
	}
	destination, err := readJuicityAddress(stream)
	if err != nil {
		return
	}
	switch network[0] {
	case juicityNetworkTCP:
		a.handleTCPStream(ctx, conn, stream, user, ip, destination)
	case juicityNetworkUDP:
		a.handleUDPStream(ctx, conn, stream, user, ip, destination)
	default:
		return
	}
}

func (a *juicityAdapter) handleTCPStream(ctx context.Context, conn *quic.Conn, stream *quic.Stream, user core.User, ip string, destination juicityAddress) {
	sourceIP, sourcePort := sourceFromAddr(conn.RemoteAddr())
	meta := route.Meta{Domain: destination.domain(), IP: destination.ip(), Port: destination.Port, Network: "tcp", Protocol: "juicity", SourceIP: sourceIP, SourcePort: sourcePort}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return
	}
	defer upstream.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{}, 2)
	go func() {
		defer wg.Done()
		n, _ := core.SpeedLimitedCopy(upstream, stream, a.limiters.For(user))
		a.addTraffic(user, n, 0)
		done <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		n, _ := core.SpeedLimitedCopy(stream, upstream, a.limiters.For(user))
		a.addTraffic(user, 0, n)
		done <- struct{}{}
	}()
	<-done
	_ = upstream.Close()
	_ = stream.Close()
	wg.Wait()
}

func (a *juicityAdapter) handleUDPStream(ctx context.Context, conn *quic.Conn, stream *quic.Stream, user core.User, ip string, initial juicityAddress) {
	sourceIP, sourcePort := sourceFromAddr(conn.RemoteAddr())
	baseMeta := route.Meta{Network: "udp", Protocol: "juicity", SourceIP: sourceIP, SourcePort: sourcePort}
	type udpRoute struct {
		pc     net.PacketConn
		target juicityAddress
	}
	var routesMu sync.Mutex
	routes := make(map[string]udpRoute)
	var writeMu sync.Mutex
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		routesMu.Lock()
		for _, r := range routes {
			_ = r.pc.Close()
		}
		routesMu.Unlock()
	}()
	// The first address is the stream's advertised default target. Actual
	// packets carry their own destination metadata, as required by Juicity.
	_ = initial
	for {
		target, payload, err := readJuicityPacket(stream)
		if err != nil {
			return
		}
		key := target.key()
		routesMu.Lock()
		r, exists := routes[key]
		if !exists {
			meta := baseMeta
			meta.Domain, meta.IP, meta.Port = target.domain(), target.ip(), target.Port
			pc, openErr := a.plane.ListenUDP(streamCtx, meta, M.ParseSocksaddrHostPort(target.Host, target.Port))
			if openErr != nil {
				routesMu.Unlock()
				return
			}
			r = udpRoute{pc: pc, target: target}
			routes[key] = r
			go a.juicityUDPReader(streamCtx, stream, &writeMu, r, user)
		}
		routesMu.Unlock()
		addr, err := target.resolveUDPAddr()
		if err != nil {
			return
		}
		n, err := r.pc.WriteTo(payload, addr)
		if err != nil {
			return
		}
		a.addTraffic(user, int64(n), 0)
	}
}

func (a *juicityAdapter) juicityUDPReader(ctx context.Context, stream *quic.Stream, writeMu *sync.Mutex, r struct {
	pc     net.PacketConn
	target juicityAddress
}, user core.User) {
	buf := make([]byte, juicityMaxPacket)
	for {
		n, addr, err := r.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		response, ok := juicityAddressFromNetAddr(addr)
		if !ok {
			response = r.target
		}
		if n > 65535 {
			n = 65535
		}
		frame := marshalJuicityPacket(response, buf[:n])
		writeMu.Lock()
		_, writeErr := stream.Write(frame)
		writeMu.Unlock()
		if writeErr != nil {
			return
		}
		a.addTraffic(user, 0, int64(n))
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (a *juicityAdapter) AddUsers(users []core.User) error {
	validated := make([]juicityUser, 0, len(users))
	for _, user := range users {
		parsed, err := uuid.Parse(strings.TrimSpace(user.UUID))
		if err != nil {
			return fmt.Errorf("juicity user %q UUID is invalid", user.UUID)
		}
		user.UUID = parsed.String()
		validated = append(validated, juicityUser{user: user, active: true})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("juicity adapter is closed")
	}
	for _, entry := range validated {
		if _, exists := a.users[entry.user.UUID]; !exists {
			a.users[entry.user.UUID] = entry
		}
	}
	return nil
}

func (a *juicityAdapter) UpsertUsers(users []core.User) error {
	validated := make([]juicityUser, 0, len(users))
	for _, user := range users {
		parsed, err := uuid.Parse(strings.TrimSpace(user.UUID))
		if err != nil {
			return fmt.Errorf("juicity user %q UUID is invalid", user.UUID)
		}
		user.UUID = parsed.String()
		validated = append(validated, juicityUser{user: user, active: true})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("juicity adapter is closed")
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.user.UUID]; exists {
			a.limiters.Remove(previous.user.ID)
		}
		a.users[entry.user.UUID] = entry
	}
	return nil
}

func (a *juicityAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		if parsed, err := uuid.Parse(strings.TrimSpace(id)); err == nil {
			key := parsed.String()
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			if entry, ok := a.users[key]; ok {
				a.limiters.Remove(entry.user.ID)
			}
			delete(a.users, key)
		}
	}
	return nil
}

func (a *juicityAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
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

func (a *juicityAdapter) OnlineIPs() map[int64][]string {
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

func (a *juicityAdapter) enterDevice(user core.User, ip string) bool {
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

func (a *juicityAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *juicityAdapter) addTraffic(user core.User, upload, download int64) {
	a.mu.Lock()
	current := a.traffic[user.ID]
	a.traffic[user.ID] = core.UserTraffic{ID: user.ID, Upload: current.Upload + upload, Download: current.Download + download}
	a.mu.Unlock()
}

func (a *juicityAdapter) removeActive(conn *quic.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
	_ = conn.CloseWithError(0, "connection complete")
}

func (a *juicityAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	listener, transport, packet := a.listener, a.transport, a.packet
	active := make([]*quic.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if transport != nil {
		_ = transport.Close()
	}
	if packet != nil {
		_ = packet.Close()
	}
	for _, conn := range active {
		_ = conn.CloseWithError(0, "server closed")
	}
	a.wg.Wait()
	return nil
}

type juicityAddress struct {
	typ  byte
	Host string
	Port uint16
}

func (a juicityAddress) key() string {
	return net.JoinHostPort(strings.ToLower(a.Host), strconv.Itoa(int(a.Port)))
}
func (a juicityAddress) domain() string {
	if a.typ == 3 {
		return a.Host
	}
	return ""
}
func (a juicityAddress) ip() netip.Addr {
	parsed, _ := netip.ParseAddr(a.Host)
	return parsed
}
func (a juicityAddress) resolveUDPAddr() (*net.UDPAddr, error) {
	return net.ResolveUDPAddr("udp", net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port))))
}

func readJuicityAddress(r io.Reader) (juicityAddress, error) {
	var typ [1]byte
	if _, err := io.ReadFull(r, typ[:]); err != nil {
		return juicityAddress{}, err
	}
	var out juicityAddress
	out.typ = typ[0]
	switch typ[0] {
	case 1:
		var raw [6]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return out, err
		}
		out.Host = net.IP(raw[:4]).String()
		out.Port = binary.BigEndian.Uint16(raw[4:])
	case 4:
		var raw [18]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return out, err
		}
		out.Host = net.IP(raw[:16]).String()
		out.Port = binary.BigEndian.Uint16(raw[16:])
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return out, err
		}
		if length[0] == 0 || length[0] > 253 {
			return out, fmt.Errorf("juicity domain length is invalid")
		}
		host := make([]byte, length[0])
		if _, err := io.ReadFull(r, host); err != nil {
			return out, err
		}
		var port [2]byte
		if _, err := io.ReadFull(r, port[:]); err != nil {
			return out, err
		}
		out.Host = string(host)
		out.Port = binary.BigEndian.Uint16(port[:])
	default:
		return out, fmt.Errorf("juicity address type %d is unsupported", typ[0])
	}
	if out.Port == 0 {
		return out, fmt.Errorf("juicity destination port is zero")
	}
	return out, nil
}

func readJuicityPacket(r io.Reader) (juicityAddress, []byte, error) {
	target, err := readJuicityAddress(r)
	if err != nil {
		return juicityAddress{}, nil, err
	}
	var length [2]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return juicityAddress{}, nil, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n == 0 || n > juicityMaxPacket {
		return juicityAddress{}, nil, fmt.Errorf("juicity packet length is invalid")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return juicityAddress{}, nil, err
	}
	return target, payload, nil
}

func marshalJuicityPacket(target juicityAddress, payload []byte) []byte {
	address := marshalJuicityAddress(target)
	frame := make([]byte, len(address)+2+len(payload))
	copy(frame, address)
	binary.BigEndian.PutUint16(frame[len(address):], uint16(len(payload)))
	copy(frame[len(address)+2:], payload)
	return frame
}

func marshalJuicityAddress(target juicityAddress) []byte {
	host := target.Host
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			out := make([]byte, 7)
			out[0] = 1
			copy(out[1:5], v4)
			binary.BigEndian.PutUint16(out[5:], target.Port)
			return out
		}
		out := make([]byte, 19)
		out[0] = 4
		copy(out[1:17], ip.To16())
		binary.BigEndian.PutUint16(out[17:], target.Port)
		return out
	}
	if len(host) > 253 {
		host = host[:253]
	}
	out := make([]byte, 4+len(host))
	out[0] = 3
	out[1] = byte(len(host))
	copy(out[2:], host)
	binary.BigEndian.PutUint16(out[2+len(host):], target.Port)
	return out
}

func juicityAddressFromNetAddr(addr net.Addr) (juicityAddress, bool) {
	if addr == nil {
		return juicityAddress{}, false
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return juicityAddress{}, false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return juicityAddress{}, false
	}
	typ := byte(3)
	if parsed := net.ParseIP(host); parsed != nil {
		if parsed.To4() != nil {
			typ = 1
		} else {
			typ = 4
		}
	}
	return juicityAddress{typ: typ, Host: host, Port: uint16(n)}, true
}

func sourceFromAddr(addr net.Addr) (netip.Addr, uint16) {
	if addr == nil {
		return netip.Addr{}, 0
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return netip.Addr{}, 0
	}
	ip, _ := netip.ParseAddr(host)
	n, _ := strconv.ParseUint(port, 10, 16)
	return ip, uint16(n)
}
