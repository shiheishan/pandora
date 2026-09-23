package kernel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	reality "github.com/aegispanel/nodeagent/internal/reality"
)

// RealityDialContext is intentionally explicit. REALITY needs a fallback
// destination to produce a browser-compatible server flight; the native
// listener owns the authenticated handoff after the handshake completes.
type RealityDialContext func(context.Context, string, string) (net.Conn, error)

// RealityListener owns the socket, handshake worker and authenticated
// connection queue. Invalid clients are closed by the handoff worker and
// never reach a protocol adapter.
type RealityListener struct {
	inner   net.Listener
	config  *reality.Config
	conns   chan net.Conn
	done    chan struct{}
	workers sync.WaitGroup
	once    sync.Once
	mu      sync.RWMutex
	err     error

	onHandshakeError func(net.Addr, error)
}

// SetHandshakeErrorHandler 注册握手失败的观测出口。
//
// REALITY 握手失败在这里是常态而不是异常——任何扫描器、任何拿错
// public-key 的客户端都会走到。所以它不该中断服务，但也不该像原来那样
// 一声不响地丢掉：那样一个「客户端连不上」的报障，服务端手里一条线索
// 都没有，只能猜是 short-id 错了、SNI 错了还是公钥错了。
//
// 必须在 Serve/Accept 之前调用。
func (l *RealityListener) SetHandshakeErrorHandler(fn func(net.Addr, error)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.onHandshakeError = fn
	l.mu.Unlock()
}

func (l *RealityListener) reportHandshakeError(remote net.Addr, err error) {
	l.mu.RLock()
	fn := l.onHandshakeError
	l.mu.RUnlock()
	if fn != nil {
		fn(remote, err)
	}
}

type RealitySession struct {
	Conn          net.Conn
	RemoteAddr    net.Addr
	ClientVersion [3]byte
	ClientTime    time.Time
	ShortID       [8]byte
}

type realitySessionContextKey struct{}

func withRealitySession(ctx context.Context, session RealitySession) context.Context {
	return context.WithValue(ctx, realitySessionContextKey{}, session)
}

func RealitySessionFromContext(ctx context.Context) (RealitySession, bool) {
	if ctx == nil {
		return RealitySession{}, false
	}
	session, ok := ctx.Value(realitySessionContextKey{}).(RealitySession)
	return session, ok && session.Conn != nil
}

func InspectRealityConn(conn net.Conn) (RealitySession, bool) {
	realityConn, ok := conn.(*reality.Conn)
	if !ok || realityConn == nil {
		return RealitySession{}, false
	}
	return RealitySession{
		Conn:          conn,
		RemoteAddr:    conn.RemoteAddr(),
		ClientVersion: realityConn.ClientVer,
		ClientTime:    realityConn.ClientTime,
		ShortID:       realityConn.ClientShortId,
	}, true
}

func (l *RealityListener) Serve(ctx context.Context, handler func(context.Context, RealitySession) error) error {
	if ctx == nil {
		return fmt.Errorf("reality serve context 不能为空")
	}
	if handler == nil {
		return fmt.Errorf("reality serve handler 不能为空")
	}
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-closed:
		}
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		session, ok := InspectRealityConn(conn)
		if !ok {
			_ = conn.Close()
			continue
		}
		go func() {
			defer conn.Close()
			_ = handler(withRealitySession(ctx, session), session)
		}()
	}
}

func ListenReality(network, address string, spec RealityServerConfig, dial RealityDialContext) (*RealityListener, error) {
	if network == "" {
		return nil, fmt.Errorf("reality network 不能为空")
	}
	if address == "" {
		return nil, fmt.Errorf("reality listen address 不能为空")
	}
	if err := validateRealityServerConfig(spec); err != nil {
		return nil, err
	}
	inner, err := net.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("reality 监听 %s/%s: %w", network, address, err)
	}
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	config := &reality.Config{
		DialContext: dial,
		Type:        network,
		Dest:        spec.Dest,
		ServerNames: cloneRealityNames(spec.ServerNames),
		PrivateKey:  append([]byte(nil), spec.PrivateKey...),
		ShortIds:    cloneRealityShortIDs(spec.ShortIDs),
		MaxTimeDiff: spec.MaxTimeDiff,
		Xver:        spec.Xver,
	}
	// REALITY 只发它自己那个一字节的合成票据（长度伪装用），绝不能发
	// 标准的真票据：记录层里把票据改扮成 application_data 的那段改写
	// 假定 payload 只有一字节，真票据经它一改就成了非法握手消息，对端
	// 直接回 unexpected_message。
	config.SessionTicketsDisabled = true
	listener := &RealityListener{inner: inner, config: config, conns: make(chan net.Conn), done: make(chan struct{})}
	go listener.acceptHandoff()
	return listener, nil
}

func (l *RealityListener) acceptHandoff() {
	for {
		raw, err := l.inner.Accept()
		if err != nil {
			l.workers.Wait()
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
			close(l.conns)
			return
		}
		l.workers.Add(1)
		go func(raw net.Conn) {
			defer l.workers.Done()
			// Scanner connections must not pin a handshake worker forever. The
			// deadline is cleared only after the authenticated handoff succeeds.
			_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
			remote := raw.RemoteAddr()
			conn, err := reality.ServerHandoff(context.Background(), raw, l.config)
			if err != nil {
				// 原来这里是光秃秃一个 return：错误丢掉，raw 也不关。
				// 前者让「客户端连不上」变成无从查起，后者在被扫描时
				// 每条连接都要占满 15 秒 deadline 才释放。
				l.reportHandshakeError(remote, err)
				_ = raw.Close()
				return
			}
			_ = conn.SetDeadline(time.Time{})
			select {
			case l.conns <- conn:
			case <-l.done:
				_ = conn.Close()
			}
		}(raw)
	}
}

func validateRealityServerConfig(spec RealityServerConfig) error {
	if spec.Dest == "" || len(spec.PrivateKey) != 32 || len(spec.ServerNames) == 0 || len(spec.ShortIDs) == 0 {
		return fmt.Errorf("reality 服务端配置不完整")
	}
	if spec.MaxTimeDiff < 0 {
		return fmt.Errorf("reality max_time_diff 不能为负数")
	}
	return nil
}

func cloneRealityNames(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for name, enabled := range src {
		dst[name] = enabled
	}
	return dst
}

func cloneRealityShortIDs(src map[[8]byte]bool) map[[8]byte]bool {
	dst := make(map[[8]byte]bool, len(src))
	for id, enabled := range src {
		dst[id] = enabled
	}
	return dst
}

func (l *RealityListener) Accept() (net.Conn, error) {
	if l == nil || l.inner == nil || l.conns == nil {
		return nil, fmt.Errorf("reality listener 未初始化")
	}
	conn, ok := <-l.conns
	if !ok {
		l.mu.RLock()
		err := l.err
		l.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *RealityListener) Addr() net.Addr {
	if l == nil || l.inner == nil {
		return nil
	}
	return l.inner.Addr()
}

func (l *RealityListener) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		close(l.done)
		if l.inner != nil {
			err := l.inner.Close()
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
		}
	})
	return nil
}
