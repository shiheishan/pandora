package kernel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"
)

// naiveAdapter implements the NaiveProxy HTTP/2 CONNECT wire contract in
// Pandora. The first eight data writes/reads use Naive's 3-byte length/padding
// records; after that the stream is raw. No sing-box/xray data plane is used.
type naiveAdapter struct {
	spec     InboundSpec
	mu       sync.RWMutex
	users    map[string]proxyUser
	traffic  map[int64]core.UserTraffic
	online   map[int64]map[string]struct{}
	listener net.Listener
	server   *http.Server
	plane    DataPlane
	limiters core.SpeedLimiters
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func newNaiveAdapter(spec InboundSpec) (Adapter, error) {
	return &naiveAdapter{spec: spec, users: make(map[string]proxyUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}, nil
}

func (a *naiveAdapter) Protocol() string { return "naive" }

func (a *naiveAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "naive") {
		return fmt.Errorf("native naive received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("naive port is invalid")
	}
	if network, _ := spec.Config.Raw["network"].(string); network != "" && !strings.EqualFold(network, "tcp") {
		return fmt.Errorf("native naive currently accepts tcp only")
	}
	security, _ := spec.Config.Raw["security"].(string)
	if security != "" && !strings.EqualFold(security, "none") {
		return fmt.Errorf("native naive rejects unsupported security %q", security)
	}
	enabled, ok := spec.Config.Raw["tls"].(bool)
	if !ok || !enabled {
		return fmt.Errorf("native naive requires TLS with ALPN h2")
	}
	if _, _, err := loadInboundTLSConfig(spec.Config.Raw); err != nil {
		return err
	}
	return nil
}

func (a *naiveAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("native naive start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("native naive adapter already started or closed")
	}
	tlsConfig, _, err := loadInboundTLSConfig(spec.Config.Raw)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	tlsConfig = tlsConfig.Clone()
	tlsConfig.NextProtos = []string{http2.NextProtoTLS}
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
	if err != nil {
		a.mu.Unlock()
		return err
	}
	ln = tls.NewListener(ln, tlsConfig)
	a.spec, a.plane = spec, hooks.DataPlane
	a.ctx, a.cancel = context.WithCancel(parent)
	server := &http.Server{Handler: http.HandlerFunc(a.serveHTTP), MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return a.ctx }}
	if err := http2.ConfigureServer(server, &http2.Server{}); err != nil {
		_ = ln.Close()
		a.cancel()
		a.mu.Unlock()
		return err
	}
	a.listener, a.server = ln, server
	a.mu.Unlock()
	a.wg.Add(1)
	go func() { defer a.wg.Done(); _ = server.Serve(ln) }()
	return nil
}

func (a *naiveAdapter) serveHTTP(w http.ResponseWriter, req *http.Request) {
	if req == nil || req.Method != http.MethodConnect || strings.TrimSpace(req.Header.Get("Padding")) == "" || len(req.Header.Get("Padding")) > 1024 {
		naiveReject(w, http.StatusBadRequest)
		return
	}
	name, password, ok := parseNaiveBasicAuth(req.Header.Get("Proxy-Authorization"))
	user, valid := a.lookupCredential(name, password)
	if !ok || !valid {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Pandora"`)
		naiveReject(w, http.StatusProxyAuthRequired)
		return
	}
	destination, err := parseNaiveDestination(req)
	if err != nil {
		naiveReject(w, http.StatusBadRequest)
		return
	}
	w.Header().Set("Padding", naivePaddingHeader())
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	conn := newNaiveStreamConn(req.Body, w, req.RemoteAddr, w.(http.Flusher))
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
	defer a.wg.Done()
	defer a.removeActive(conn)
	_ = a.handleStream(ctx, conn, user, destination)
}

func parseNaiveBasicAuth(value string) (string, string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "basic") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	credentials := strings.SplitN(string(decoded), ":", 2)
	if len(credentials) != 2 || credentials[0] == "" {
		return "", "", false
	}
	return credentials[0], credentials[1], true
}

func parseNaiveDestination(req *http.Request) (vlessDestination, error) {
	var out vlessDestination
	authority := strings.TrimSpace(req.Header.Get("-connect-authority"))
	if authority == "" && req.URL != nil {
		authority = strings.TrimSpace(req.URL.Host)
	}
	if authority == "" {
		authority = strings.TrimSpace(req.Host)
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return out, fmt.Errorf("naive CONNECT authority invalid: %w", err)
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return out, fmt.Errorf("naive CONNECT port invalid")
	}
	if host == "" || strings.ContainsAny(host, "\r\n") {
		return out, fmt.Errorf("naive CONNECT host invalid")
	}
	out.Host, out.Port = host, uint16(parsed)
	if ip, err := netip.ParseAddr(host); err == nil {
		out.IP = ip
	} else {
		out.Domain = host
	}
	return out, nil
}

func (a *naiveAdapter) handleStream(ctx context.Context, conn net.Conn, user core.User, destination vlessDestination) error {
	defer conn.Close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("naive device limit")
	}
	defer a.leaveDevice(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "naive", SourceIP: sourceIP}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		n, _ := core.SpeedLimitedCopy(upstream, conn, a.limiters.For(user))
		a.addTraffic(user, n, 0)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		copyWG.Done()
	}()
	go func() {
		n, _ := core.SpeedLimitedCopy(conn, upstream, a.limiters.For(user))
		a.addTraffic(user, 0, n)
		// 上游关掉写端时，这个关闭要传给客户端，否则它收不到 EOF，
		// 会一直等到自己超时——连接也就一直不释放。
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		copyWG.Done()
	}()
	copyWG.Wait()
	return nil
}

func (a *naiveAdapter) lookupCredential(name, password string) (core.User, bool) {
	a.mu.RLock()
	entry, ok := a.users[name]
	a.mu.RUnlock()
	return entry.user, ok && entry.password == password
}
func (a *naiveAdapter) AddUsers(users []core.User) error {
	validated := make([]proxyUser, 0, len(users))
	for _, user := range users {
		name := strings.TrimSpace(user.UUID)
		if name == "" {
			return fmt.Errorf("naive user uuid is required")
		}
		user.UUID = name
		validated = append(validated, proxyUser{user: user, password: name})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("native naive adapter closed")
	}
	for _, entry := range validated {
		name := entry.password
		if _, exists := a.users[name]; !exists {
			a.users[name] = entry
		}
	}
	return nil
}
func (a *naiveAdapter) UpsertUsers(users []core.User) error {
	validated := make([]proxyUser, 0, len(users))
	for _, user := range users {
		name := strings.TrimSpace(user.UUID)
		if name == "" {
			return fmt.Errorf("naive user uuid is required")
		}
		user.UUID = name
		validated = append(validated, proxyUser{user: user, password: name})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("native naive adapter closed")
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.password]; exists {
			a.limiters.Remove(previous.user.ID)
		}
		a.users[entry.password] = entry
	}
	return nil
}

func (a *naiveAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		name := strings.TrimSpace(id)
		// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
		if entry, ok := a.users[name]; ok {
			a.limiters.Remove(entry.user.ID)
		}
		delete(a.users, name)
	}
	return nil
}
func (a *naiveAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
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
func (a *naiveAdapter) OnlineIPs() map[int64][]string {
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
func (a *naiveAdapter) enterDevice(user core.User, ip string) bool {
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
func (a *naiveAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}
func (a *naiveAdapter) addTraffic(user core.User, upload, download int64) {
	a.mu.Lock()
	t := a.traffic[user.ID]
	t.ID = user.ID
	t.Upload += upload
	t.Download += download
	a.traffic[user.ID] = t
	a.mu.Unlock()
}
func (a *naiveAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}
func (a *naiveAdapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	listener, server := a.listener, a.server
	active := make([]net.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	a.mu.Unlock()
	if server != nil {
		_ = server.Close()
	} else if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range active {
		_ = conn.Close()
	}
	a.wg.Wait()
	return nil
}

func naiveReject(w http.ResponseWriter, status int) {
	if hijacker, ok := w.(http.Hijacker); ok && status != http.StatusOK {
		if conn, _, err := hijacker.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	w.WriteHeader(status)
}
func naivePaddingHeader() string { return "~pandora-native-naive-padding~" }

type naiveStreamConn struct {
	reader  io.ReadCloser
	writer  http.ResponseWriter
	flusher http.Flusher
	remote  net.Addr
	state   naivePaddingState
	writeMu sync.Mutex
}
type naivePaddingState struct{ readFrames, writeFrames, readRemaining, paddingRemaining int }

func newNaiveStreamConn(reader io.ReadCloser, writer http.ResponseWriter, remote string, flusher http.Flusher) net.Conn {
	return &naiveStreamConn{reader: reader, writer: writer, flusher: flusher, remote: naiveAddr(remote)}
}
func (c *naiveStreamConn) Read(p []byte) (int, error) { return c.state.read(c.reader, p) }
func (c *naiveStreamConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.state.write(c.writer, c.flusher, p)
}
func (c *naiveStreamConn) Close() error                     { return c.reader.Close() }
func (c *naiveStreamConn) LocalAddr() net.Addr              { return naiveAddr("pandora-naive") }
func (c *naiveStreamConn) RemoteAddr() net.Addr             { return c.remote }
func (c *naiveStreamConn) SetDeadline(time.Time) error      { return nil }
func (c *naiveStreamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *naiveStreamConn) SetWriteDeadline(time.Time) error { return nil }
func (s *naivePaddingState) read(reader io.Reader, p []byte) (int, error) {
	if s.readRemaining > 0 {
		if len(p) > s.readRemaining {
			p = p[:s.readRemaining]
		}
		n, err := reader.Read(p)
		if err == nil {
			s.readRemaining -= n
		}
		return n, err
	}
	if s.paddingRemaining > 0 {
		if _, err := io.CopyN(io.Discard, reader, int64(s.paddingRemaining)); err != nil {
			return 0, err
		}
		s.paddingRemaining = 0
	}
	if s.readFrames < 8 {
		var header [3]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return 0, err
		}
		size, pad := int(binary.BigEndian.Uint16(header[:2])), int(header[2])
		if size == 0 || size > 65535 {
			return 0, fmt.Errorf("naive data frame length invalid")
		}
		if len(p) > size {
			p = p[:size]
		}
		n, err := io.ReadFull(reader, p)
		if err == io.ErrUnexpectedEOF && n > 0 {
			err = nil
		}
		if err != nil {
			return n, err
		}
		s.readFrames++
		s.readRemaining = size - n
		s.paddingRemaining = pad
		return n, nil
	}
	return reader.Read(p)
}
func (s *naivePaddingState) write(writer io.Writer, flusher http.Flusher, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 65535 {
			chunk = chunk[:65535]
		}
		if s.writeFrames < 8 {
			var padByte [1]byte
			_, _ = rand.Read(padByte[:])
			pad := int(padByte[0])
			packet := make([]byte, 3+len(chunk)+pad)
			binary.BigEndian.PutUint16(packet[:2], uint16(len(chunk)))
			packet[2] = byte(pad)
			copy(packet[3:], chunk)
			if pad > 0 {
				_, _ = rand.Read(packet[3+len(chunk):])
			}
			if _, err := writer.Write(packet); err != nil {
				return total, err
			}
			s.writeFrames++
		} else {
			if _, err := writer.Write(chunk); err != nil {
				return total, err
			}
		}
		total += len(chunk)
		p = p[len(chunk):]
		if flusher != nil {
			flusher.Flush()
		}
	}
	return total, nil
}

type naiveAddr string

func (a naiveAddr) Network() string { return "tcp" }
func (a naiveAddr) String() string  { return string(a) }
