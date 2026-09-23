package kernel

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	nativeShadowTLS "github.com/aegispanel/nodeagent/internal/nativewire/shadowtls"
	"github.com/aegispanel/nodeagent/route"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// shadowTLSAdapter owns the ShadowTLS v3 outer handshake and hands the
// authenticated inner stream to Pandora's native classic Shadowsocks decoder.
// The decoy connection is routed through DataPlane, so the adapter never opens
// an unaccounted compatibility socket itself.
type shadowTLSAdapter struct {
	spec     InboundSpec
	mu       sync.RWMutex
	inner    *shadowsocksAdapter
	service  *nativeShadowTLS.Service
	listener net.Listener
	plane    DataPlane
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func newShadowTLSAdapter(spec InboundSpec) (Adapter, error) {
	method := shadowTLSMethod(spec.Config.Raw)
	innerValue, err := newShadowsocksAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Raw: map[string]any{"method": method}}})
	if err != nil {
		return nil, err
	}
	return &shadowTLSAdapter{spec: spec, inner: innerValue.(*shadowsocksAdapter), active: make(map[net.Conn]struct{})}, nil
}

func (a *shadowTLSAdapter) Protocol() string { return "shadowtls" }

func (a *shadowTLSAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "shadowtls") {
		return fmt.Errorf("native shadowtls received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("shadowtls port is invalid")
	}
	if network, _ := spec.Config.Raw["network"].(string); network != "" && !strings.EqualFold(network, "tcp") {
		return fmt.Errorf("native shadowtls currently accepts tcp only")
	}
	version := shadowRawInt(spec.Config.Raw, "version", 3)
	if version != 3 {
		return fmt.Errorf("native shadowtls supports version 3 only")
	}
	if strings.TrimSpace(rawString(spec.Config.Raw, "password")) == "" {
		return fmt.Errorf("shadowtls outer password is required")
	}
	server := shadowTLSServer(spec.Config.Raw)
	if !server.IsValid() {
		return fmt.Errorf("shadowtls handshake server is required")
	}
	return nil
}

func (a *shadowTLSAdapter) Start(parent context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if parent == nil || hooks.DataPlane == nil {
		return fmt.Errorf("shadowtls start requires context and data plane")
	}
	a.mu.Lock()
	if a.listener != nil || a.closed {
		a.mu.Unlock()
		return fmt.Errorf("shadowtls adapter already started or closed")
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
	a.spec, a.plane = spec, hooks.DataPlane
	a.ctx, a.cancel = context.WithCancel(parent)
	a.inner.spec = InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Listen: listen, Port: spec.Config.Port, Raw: map[string]any{"method": shadowTLSMethod(spec.Config.Raw)}}}
	a.inner.plane = hooks.DataPlane
	a.inner.ctx = a.ctx
	service, err := nativeShadowTLS.NewService(nativeShadowTLS.ServiceConfig{
		Version:     3,
		Users:       []nativeShadowTLS.User{{Name: "pandora", Password: rawString(spec.Config.Raw, "password")}},
		Handshake:   nativeShadowTLS.HandshakeConfig{Server: shadowTLSServer(spec.Config.Raw), Dialer: shadowTLSDialer{adapter: a}},
		StrictMode:  shadowRawBool(spec.Config.Raw, "strict", false),
		WildcardSNI: shadowTLSWildcardSNI(spec.Config.Raw),
		Handler:     shadowTLSHandler{adapter: a},
		Logger:      logger.NOP(),
	})
	if err != nil {
		_ = ln.Close()
		a.cancel()
		a.mu.Unlock()
		return err
	}
	a.listener, a.service = ln, service
	a.mu.Unlock()
	a.wg.Add(1)
	go a.acceptLoop()
	return nil
}

func (a *shadowTLSAdapter) acceptLoop() {
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
		service := a.service
		a.mu.Unlock()
		go func() {
			defer a.wg.Done()
			defer a.removeActive(conn)
			_ = service.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), M.Socksaddr{}, nil)
			_ = conn.Close()
		}()
	}
}

type shadowTLSHandler struct{ adapter *shadowTLSAdapter }

func (h shadowTLSHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, _ N.CloseHandlerFunc) {
	_ = h.adapter.inner.handleConn(ctx, conn)
}

type shadowTLSDialer struct{ adapter *shadowTLSAdapter }

func (d shadowTLSDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if destination.String() == "" {
		return nil, fmt.Errorf("shadowtls decoy destination empty")
	}
	meta := route.Meta{Domain: destination.Fqdn, IP: destination.Addr, Port: destination.Port, Network: "tcp", Protocol: "shadowtls-handshake"}
	return d.adapter.plane.DialTCP(ctx, meta, destination)
}
func (d shadowTLSDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, fmt.Errorf("shadowtls dialer does not support UDP")
}

// shadowTLSWildcardSNI 解析 wildcard_sni，默认关闭。
//
// 打开之后，服务端拿客户端给的 SNI 当握手目标，而不是配置里那个被借用
// 的站点——等于任何通过认证的用户都能让节点去连任意主机的 443，是一个
// SSRF 面。sing-box 那边这个开关同样默认关闭。
//
// 之前这里硬编码成 authed，除了上面那个问题，还让本地互操作测试连错了
// 地方：客户端 SNI 是 localhost，服务端就去连了本机 443 上真实跑着的
// 站点，握手回来的是那个站点的证书。
func shadowTLSWildcardSNI(raw map[string]any) nativeShadowTLS.WildcardSNI {
	switch strings.ToLower(strings.TrimSpace(rawString(raw, "wildcard_sni"))) {
	case "authed":
		return nativeShadowTLS.WildcardSNIAuthed
	case "all":
		return nativeShadowTLS.WildcardSNIAll
	default:
		return nativeShadowTLS.WildcardSNIOff
	}
}

func shadowTLSServer(raw map[string]any) M.Socksaddr {
	server := strings.TrimSpace(rawString(raw, "server"))
	if server == "" {
		server = strings.TrimSpace(rawString(raw, "handshake_server"))
	}
	if server == "" {
		return M.Socksaddr{}
	}
	if !strings.Contains(server, ":") {
		server += ":" + strconv.Itoa(shadowRawInt(raw, "server_port", 443))
	}
	return M.ParseSocksaddr(server)
}
func shadowTLSMethod(raw map[string]any) string {
	method := strings.ToLower(strings.TrimSpace(rawString(raw, "method")))
	if method == "" {
		return "aes-256-gcm"
	}
	return method
}
func (a *shadowTLSAdapter) AddUsers(users []core.User) error  { return a.inner.AddUsers(users) }
func (a *shadowTLSAdapter) UpsertUsers(users []core.User) error { return a.inner.UpsertUsers(users) }
func (a *shadowTLSAdapter) DelUsers(ids []string) error        { return a.inner.DelUsers(ids) }
func (a *shadowTLSAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.inner.SnapshotTraffic()
}
func (a *shadowTLSAdapter) OnlineIPs() map[int64][]string { return a.inner.OnlineIPs() }
func (a *shadowTLSAdapter) Close() error {
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
func (a *shadowTLSAdapter) removeActive(conn net.Conn) {
	a.mu.Lock()
	delete(a.active, conn)
	a.mu.Unlock()
}

func shadowRawInt(raw map[string]any, key string, fallback int) int {
	if value := rawValue(raw, key); value != nil {
		if parsed, valid := rawInt(value); valid {
			return parsed
		}
	}
	return fallback
}

func shadowRawBool(raw map[string]any, key string, fallback bool) bool {
	if value := rawValue(raw, key); value != nil {
		if parsed, valid := value.(bool); valid {
			return parsed
		}
	}
	return fallback
}
