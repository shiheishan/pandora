// [INPUT]: 依赖 adapter.go 的 Adapter 契约与 DataPlane，依赖 vless_request.go 的 vlessDestination，依赖 core 的用户与 route 的路由
// [OUTPUT]: 对外提供 proxyAdapter（经 newSOCKSAdapter / newHTTPProxyAdapter 注册）的 Protocol、Validate、Start、用户表与计量方法、Close
// [POS]: kernel 的 socks 与 http 入站共用适配器：SOCKS4/4a/5 CONNECT、HTTP CONNECT 与正向 GET，认证绑定面板下发的用户 UUID；SOCKS5 UDP ASSOCIATE 在 proxy_udp.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// proxyAdapter is the native, deliberately small forward-proxy boundary. It
// implements SOCKS4/4a, SOCKS5 CONNECT/UDP ASSOCIATE and HTTP CONNECT/forward
// GET without embedding a third-party kernel.
type proxyAdapter struct {
	protocol string
	spec     InboundSpec
	mu       sync.RWMutex
	users    map[string]proxyUser
	traffic  map[int64]core.UserTraffic
	online   map[int64]map[string]struct{}
	listener net.Listener
	plane    DataPlane
	limiters core.SpeedLimiters
	ctx      context.Context
	cancel   context.CancelFunc
	tls      *tls.Config
	closed   bool
	active   map[net.Conn]struct{}
	wg       sync.WaitGroup
}

type proxyUser struct {
	user     core.User
	password string
}

func newSOCKSAdapter(spec InboundSpec) (Adapter, error) {
	return newProxyAdapter("socks", spec)
}

func newHTTPProxyAdapter(spec InboundSpec) (Adapter, error) {
	return newProxyAdapter("http", spec)
}

func newProxyAdapter(protocol string, spec InboundSpec) (Adapter, error) {
	return &proxyAdapter{protocol: protocol, spec: spec,
		users: make(map[string]proxyUser), traffic: make(map[int64]core.UserTraffic),
		online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}, nil
}

func (a *proxyAdapter) Protocol() string { return a.protocol }

func (a *proxyAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, a.protocol) {
		return fmt.Errorf("native %s received protocol %q", a.protocol, spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("%s port is invalid", a.protocol)
	}
	if network, _ := spec.Config.Raw["network"].(string); network != "" && !(a.protocol == "socks" && strings.EqualFold(network, "udp")) && !strings.EqualFold(network, "tcp") {
		return fmt.Errorf("native %s transport is unsupported", a.protocol)
	}
	security, _ := spec.Config.Raw["security"].(string)
	if security != "" && !strings.EqualFold(security, "none") {
		return fmt.Errorf("native %s rejects unsupported security %q", a.protocol, security)
	}
	if enabled, ok := spec.Config.Raw["tls"].(bool); ok && enabled {
		if _, _, err := loadInboundTLSConfig(spec.Config.Raw); err != nil {
			return err
		}
	}
	return nil
}

func (a *proxyAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("native %s start requires context and data plane", a.protocol)
	}
	a.mu.Lock()
	if a.listener != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("native %s adapter already started or closed", a.protocol)
	}
	tlsConfig, _, err := loadInboundTLSConfig(spec.Config.Raw)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	listen := spec.Config.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(listen, strconv.Itoa(spec.Config.Port)))
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig.Clone())
	}
	a.spec, a.plane, a.tls = spec, hooks.DataPlane, tlsConfig
	a.ctx, a.cancel = context.WithCancel(parent)
	a.listener = ln
	a.mu.Unlock()
	a.wg.Add(1)
	go a.acceptLoop()
	return nil
}

func (a *proxyAdapter) acceptLoop() {
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
		if a.closed {
			a.mu.Unlock()
			_ = conn.Close()
			continue
		}
		a.active[conn] = struct{}{}
		a.wg.Add(1)
		ctx := a.ctx
		a.mu.Unlock()
		go func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			_ = a.handleConn(ctx, conn)
		}()
	}
}

func (a *proxyAdapter) handleConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReaderSize(conn, 32<<10)
	var user core.User
	var destination vlessDestination
	var command byte
	var socksVersion byte
	var httpRequest *http.Request
	var err error
	if a.protocol == "socks" {
		user, destination, command, socksVersion, err = a.readSOCKS(reader, conn)
	} else {
		user, destination, httpRequest, err = a.readHTTP(reader, conn)
	}
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	ip := remoteIP(conn.RemoteAddr())
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("%s device limit", a.protocol)
	}
	defer a.leaveDevice(user, ip)
	if a.protocol == "socks" && command == 3 {
		return a.handleSOCKSUDP(ctx, conn, user, reader, ip)
	}
	if a.protocol == "http" && httpRequest != nil && httpRequest.Method != http.MethodConnect {
		return a.handleHTTPForward(ctx, conn, user, destination, httpRequest, ip)
	}
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: a.protocol, SourceIP: sourceIP}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		if a.protocol == "http" {
			return err
		}
		return err
	}
	defer upstream.Close()
	if a.protocol == "socks" {
		var writeErr error
		if socksVersion == 4 {
			writeErr = writeSOCKS4Reply(conn, 90, destination)
		} else {
			writeErr = writeSOCKSSuccess(conn)
		}
		if writeErr != nil {
			return writeErr
		}
	} else {
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\nProxy-Agent: Pandora\r\n\r\n"); err != nil {
			return err
		}
	}
	proxyConn := net.Conn(conn)
	if reader.Buffered() > 0 {
		proxyConn = &bufferedNetConn{Conn: conn, reader: reader}
	}
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		n, _ := core.SpeedLimitedCopy(upstream, proxyConn, a.limiters.For(user))
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

func (a *proxyAdapter) readSOCKS(reader *bufio.Reader, conn net.Conn) (core.User, vlessDestination, byte, byte, error) {
	var out vlessDestination
	version, err := reader.ReadByte()
	if err != nil {
		return core.User{}, out, 0, 0, err
	}
	if version == 4 {
		user, destination, command, err := a.readSOCKS4(reader, conn)
		return user, destination, command, version, err
	}
	if version != 5 {
		return core.User{}, out, 0, 0, fmt.Errorf("socks version %d unsupported", version)
	}
	var methodCount byte
	if methodCount, err = reader.ReadByte(); err != nil {
		return core.User{}, out, 0, 0, err
	}
	methods := make([]byte, int(methodCount))
	if len(methods) == 0 || len(methods) > 16 {
		return core.User{}, out, 0, 0, fmt.Errorf("socks5 methods invalid")
	}
	if _, err := io.ReadFull(reader, methods); err != nil {
		return core.User{}, out, 0, 0, err
	}
	// Never expose an unauthenticated forward proxy, even before the first
	// panel user snapshot has arrived.
	auth := byte(2)
	found := false
	for _, method := range methods {
		if method == auth {
			found = true
			break
		}
	}
	if !found {
		_, _ = conn.Write([]byte{5, 0xff})
		return core.User{}, out, 0, 0, fmt.Errorf("socks5 authentication method rejected")
	}
	if _, err := conn.Write([]byte{5, auth}); err != nil {
		return core.User{}, out, 0, 0, err
	}
	user := core.User{}
	if auth == 2 {
		var h [2]byte
		if _, err := io.ReadFull(reader, h[:]); err != nil || h[0] != 1 || h[1] == 0 || h[1] > 128 {
			return user, out, 0, 0, fmt.Errorf("socks5 username invalid")
		}
		name := make([]byte, h[1])
		if _, err := io.ReadFull(reader, name); err != nil {
			return user, out, 0, 0, err
		}
		if _, err := io.ReadFull(reader, h[:1]); err != nil || h[0] == 0 || h[0] > 128 {
			return user, out, 0, 0, fmt.Errorf("socks5 password invalid")
		}
		password := make([]byte, h[0])
		if _, err := io.ReadFull(reader, password); err != nil {
			return user, out, 0, 0, err
		}
		var ok bool
		user, ok = a.lookupCredential(string(name), string(password))
		if !ok {
			_, _ = conn.Write([]byte{1, 1})
			return core.User{}, out, 0, 0, fmt.Errorf("socks5 credentials rejected")
		}
		if _, err := conn.Write([]byte{1, 0}); err != nil {
			return core.User{}, out, 0, 0, err
		}
	}
	var req [4]byte
	if _, err := io.ReadFull(reader, req[:]); err != nil || req[0] != 5 || (req[1] != 1 && req[1] != 3) || req[2] != 0 {
		return user, out, 0, 0, fmt.Errorf("socks5 command unsupported")
	}
	command := req[1]
	if err := readProxyAddress(reader, req[3], &out); err != nil {
		return user, out, 0, 0, err
	}
	var port [2]byte
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return user, out, 0, 0, err
	}
	out.Port = uint16(port[0])<<8 | uint16(port[1])
	if command == 1 && out.Port == 0 {
		return user, out, 0, 0, fmt.Errorf("socks5 destination port invalid")
	}
	return user, out, command, version, nil
}

// readSOCKS4 implements RFC 1928-compatible SOCKS4 and SOCKS4a CONNECT. The
// panel's UUID is the SOCKS4 userid; unlike SOCKS5 there is no password field,
// so the userid must match a provisioned UUID exactly.
func (a *proxyAdapter) readSOCKS4(reader *bufio.Reader, conn net.Conn) (core.User, vlessDestination, byte, error) {
	var out vlessDestination
	var head [7]byte
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		return core.User{}, out, 0, err
	}
	if head[0] != 1 { // CONNECT only; BIND is intentionally not an exit-proxy primitive.
		_ = writeSOCKS4Reply(conn, 91, out)
		return core.User{}, out, 0, fmt.Errorf("socks4 command %d unsupported", head[0])
	}
	out.Port = uint16(head[1])<<8 | uint16(head[2])
	if out.Port == 0 {
		_ = writeSOCKS4Reply(conn, 91, out)
		return core.User{}, out, 0, fmt.Errorf("socks4 destination port invalid")
	}
	if head[3] == 0 && head[4] == 0 && head[5] == 0 && head[6] != 0 {
		name, err := readNULString(reader, 128)
		if err != nil {
			return core.User{}, out, 0, fmt.Errorf("socks4 userid: %w", err)
		}
		domain, err := readNULString(reader, 253)
		if err != nil {
			return core.User{}, out, 0, fmt.Errorf("socks4a domain: %w", err)
		}
		out.Domain, out.Host = domain, domain
		user, ok := a.lookupCredential(name, name)
		if !ok {
			_ = writeSOCKS4Reply(conn, 93, out)
			return core.User{}, out, 0, fmt.Errorf("socks4 credentials rejected")
		}
		return user, out, 1, nil
	}
	name, err := readNULString(reader, 128)
	if err != nil {
		return core.User{}, out, 0, fmt.Errorf("socks4 userid: %w", err)
	}
	ip := netip.AddrFrom4([4]byte{head[3], head[4], head[5], head[6]})
	out.IP, out.Host = ip, ip.String()
	user, ok := a.lookupCredential(name, name)
	if !ok {
		_ = writeSOCKS4Reply(conn, 93, out)
		return core.User{}, out, 0, fmt.Errorf("socks4 credentials rejected")
	}
	return user, out, 1, nil
}

func readNULString(reader *bufio.Reader, max int) (string, error) {
	buf := make([]byte, 0, 32)
	for len(buf) <= max {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == 0 {
			if len(buf) == 0 {
				return "", fmt.Errorf("empty value")
			}
			return string(buf), nil
		}
		buf = append(buf, b)
	}
	return "", fmt.Errorf("value exceeds %d bytes", max)
}

func (a *proxyAdapter) readHTTP(reader *bufio.Reader, conn net.Conn) (core.User, vlessDestination, *http.Request, error) {
	var out vlessDestination
	req, err := http.ReadRequest(reader)
	if err != nil {
		return core.User{}, out, nil, err
	}
	if req.URL == nil || req.URL.Host == "" || (req.Method != http.MethodConnect && req.Method != http.MethodGet) {
		_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\n\r\n")
		return core.User{}, out, nil, fmt.Errorf("http proxy supports CONNECT and GET only")
	}
	host, port, err := proxyHostPort(req.URL.Host, req.Method == http.MethodGet)
	if err != nil {
		return core.User{}, out, nil, err
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return core.User{}, out, nil, fmt.Errorf("http proxy port invalid")
	}
	out.Host, out.Port = host, uint16(parsedPort)
	if ip, err := netip.ParseAddr(host); err == nil {
		out.IP = ip
	} else {
		out.Domain = host
	}
	name, password, ok := req.BasicAuth()
	if !ok {
		if raw := req.Header.Get("Proxy-Authorization"); raw != "" {
			if value, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(raw, "Basic "))); decodeErr == nil {
				parts := strings.SplitN(string(value), ":", 2)
				if len(parts) == 2 {
					name, password, ok = parts[0], parts[1], true
				}
			}
		}
	}
	if !ok {
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=Pandora\r\nConnection: close\r\n\r\n")
		return core.User{}, out, nil, fmt.Errorf("http proxy authentication required")
	}
	if user, valid := a.lookupCredential(name, password); valid {
		return user, out, req, nil
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nConnection: close\r\n\r\n")
	return core.User{}, out, nil, fmt.Errorf("http proxy credentials rejected")
}

func proxyHostPort(raw string, getDefault bool) (string, string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		return host, port, nil
	}
	if getDefault && !strings.Contains(raw, ":") {
		return raw, "80", nil
	}
	return "", "", fmt.Errorf("http proxy target invalid: %w", err)
}

func (a *proxyAdapter) handleHTTPForward(ctx context.Context, conn net.Conn, user core.User, destination vlessDestination, req *http.Request, ip string) error {
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "tcp", Protocol: "http", SourceIP: sourceIP}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.RequestURI = req.URL.RequestURI()
	req.URL.Scheme = ""
	req.URL.Host = ""
	if err := req.Write(upstream); err != nil {
		return err
	}
	n, _ := core.SpeedLimitedCopy(conn, upstream, a.limiters.For(user))
	a.addTraffic(user, 0, n)
	// 同上：上游结束时把关闭传给客户端。
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return nil
}

func readProxyAddress(reader *bufio.Reader, kind byte, out *vlessDestination) error {
	switch kind {
	case 1:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return err
		}
		out.IP = netip.AddrFrom4([4]byte{buf[0], buf[1], buf[2], buf[3]})
		out.Host = out.IP.String()
	case 3:
		length, err := reader.ReadByte()
		if err != nil || length == 0 || length > 253 {
			return fmt.Errorf("proxy domain invalid")
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return err
		}
		out.Domain, out.Host = string(buf), string(buf)
	case 4:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return err
		}
		var ip [16]byte
		copy(ip[:], buf)
		out.IP = netip.AddrFrom16(ip)
		out.Host = out.IP.String()
	default:
		return fmt.Errorf("proxy address type unsupported")
	}
	return nil
}

func writeSOCKSSuccess(conn net.Conn) error {
	_, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func writeSOCKS4Reply(conn net.Conn, code byte, destination vlessDestination) error {
	var ip [4]byte
	if destination.IP.IsValid() && destination.IP.Is4() {
		v4 := destination.IP.As4()
		copy(ip[:], v4[:])
	}
	packet := []byte{0, code, byte(destination.Port >> 8), byte(destination.Port), ip[0], ip[1], ip[2], ip[3]}
	_, err := conn.Write(packet)
	return err
}

func (a *proxyAdapter) lookupCredential(name, password string) (core.User, bool) {
	a.mu.RLock()
	entry, ok := a.users[name]
	a.mu.RUnlock()
	return entry.user, ok && entry.password == password
}

func (a *proxyAdapter) AddUsers(users []core.User) error {
	validated := make([]proxyUser, 0, len(users))
	for _, user := range users {
		name := strings.TrimSpace(user.UUID)
		if name == "" {
			return fmt.Errorf("%s user uuid is required", a.protocol)
		}
		user.UUID = name
		validated = append(validated, proxyUser{user: user, password: name})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("native %s adapter closed", a.protocol)
	}
	for _, entry := range validated {
		name := entry.password
		if _, exists := a.users[name]; !exists {
			a.users[name] = entry
		}
	}
	return nil
}

func (a *proxyAdapter) UpsertUsers(users []core.User) error {
	validated := make([]proxyUser, 0, len(users))
	for _, user := range users {
		name := strings.TrimSpace(user.UUID)
		if name == "" {
			return fmt.Errorf("%s user uuid is required", a.protocol)
		}
		user.UUID = name
		validated = append(validated, proxyUser{user: user, password: name})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("native %s adapter closed", a.protocol)
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.password]; exists {
			a.limiters.Remove(previous.user.ID)
		}
		a.users[entry.password] = entry
	}
	return nil
}

func (a *proxyAdapter) DelUsers(ids []string) error {
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

func (a *proxyAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
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

func (a *proxyAdapter) OnlineIPs() map[int64][]string {
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

func (a *proxyAdapter) enterDevice(user core.User, ip string) bool {
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

func (a *proxyAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *proxyAdapter) addTraffic(user core.User, upload, download int64) {
	a.mu.Lock()
	t := a.traffic[user.ID]
	t.ID = user.ID
	t.Upload += upload
	t.Download += download
	a.traffic[user.ID] = t
	a.mu.Unlock()
}

func (a *proxyAdapter) Close() error {
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
	active := make([]net.Conn, 0, len(a.active))
	for conn := range a.active {
		active = append(active, conn)
	}
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range active {
		_ = conn.Close()
	}
	a.wg.Wait()
	return nil
}

func (a *proxyAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}

type bufferedNetConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedNetConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
