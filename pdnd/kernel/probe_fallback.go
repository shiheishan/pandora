package kernel

// 抗主动探测的公共件。
//
// 认证不过的连接不能「立刻断开」：0 秒断连本身就是代理指纹。这里提供两条
// 出路，由各入站在认证判定失败的那一刻调用：
//
//   - 回落（probeFallback）：把已经读到的字节原样补发给回落目标，然后
//     双向拷贝。目标只来自入站配置（REALITY 的 dest、Trojan/AnyTLS/Naive 的
//     raw `fallback`），探测方的任何输入都不能决定转发去哪。
//   - 中性页面（serveFallbackHTTP + neutralHTTPHandler）：没配回落时由一个标准 net/http 服务接住，
//     所有路径都回 Go 自带的 404，不带任何品牌字样。
//
// 两条路都有上限：上行、下行字节数，总时长，以及全进程并发数。上限是为了
// 不让节点被当成去回落目标的免费代理，不是为了限速正常探测——真站点的一个
// 页面远小于这些值。

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

const (
	// probeFallbackDialTimeout 是拨回落目标的上限。回落目标通常在本机或同机房。
	probeFallbackDialTimeout = 5 * time.Second
	// probeFallbackMaxDuration 是一条回落 / 中性页面会话的总时长上限。真站点的
	// keep-alive 空闲超时一般在一两分钟内，正常探测碰不到这个值。
	probeFallbackMaxDuration = 5 * time.Minute
	// probeFallbackMaxUpload / probeFallbackMaxDownload 是一条会话的上下行字节
	// 上限；超了直接断开两端。
	probeFallbackMaxUpload   = 4 << 20
	probeFallbackMaxDownload = 32 << 20
	// probeFallbackMaxConcurrent 是全进程同时在跑的回落会话上限。满了就退回
	// 原来的做法（直接关），只占一个 fd、不再多拨一条出站。
	probeFallbackMaxConcurrent = 1024
	// neutralHTTPIdleTimeout 是中性页面 keep-alive 的空闲上限，取常见 Web 服务器
	// 的量级（nginx 默认 75 秒）。
	neutralHTTPIdleTimeout = 75 * time.Second
)

var probeFallbackSlots = make(chan struct{}, probeFallbackMaxConcurrent)

// acquireProbeFallbackSlot 拿一个全进程并发名额；拿不到返回 false。
func acquireProbeFallbackSlot() bool {
	select {
	case probeFallbackSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseProbeFallbackSlot() { <-probeFallbackSlots }

// parseProbeFallback 读入站 raw 配置里可选的 `fallback`（"host:port"）。
// 没配返回空串。只接受显式的主机加端口，不接受 URL、不接受空主机。
func parseProbeFallback(raw map[string]any) (string, error) {
	value, exists := raw["fallback"]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("fallback must be a \"host:port\" string")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if strings.Contains(text, "://") || strings.ContainsAny(text, "/?#@ \t\r\n") {
		return "", fmt.Errorf("fallback must be \"host:port\", got %q", text)
	}
	host, port, err := net.SplitHostPort(text)
	if err != nil {
		return "", fmt.Errorf("fallback must be \"host:port\": %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("fallback host is empty")
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsed == 0 {
		return "", fmt.Errorf("fallback port is invalid")
	}
	return net.JoinHostPort(host, port), nil
}

// dialProbeFallback 拨配置里的回落目标。addr 只能来自 parseProbeFallback。
func dialProbeFallback(ctx context.Context, addr string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dialer := &net.Dialer{Timeout: probeFallbackDialTimeout}
	return dialer.DialContext(ctx, "tcp", addr)
}

// relayProbeFallback 把 prefix 补发给 target 后双向拷贝，直到一端结束或撞上
// 上限。返回时 target 已关；client 只在出错或撞上限时被关（REALITY 的原始
// 连接由调用方统一关）。
func relayProbeFallback(client net.Conn, prefix []byte, target net.Conn) {
	defer target.Close()
	deadline := time.Now().Add(probeFallbackMaxDuration)
	_ = client.SetDeadline(deadline)
	_ = target.SetDeadline(deadline)
	if len(prefix) > 0 {
		if _, err := target.Write(prefix); err != nil {
			_ = client.Close()
			return
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 上行：客户端 → 回落目标。
		n, err := io.Copy(target, io.LimitReader(client, probeFallbackMaxUpload-int64(len(prefix))+1))
		if err != nil || n > probeFallbackMaxUpload-int64(len(prefix)) {
			_ = client.Close()
			_ = target.Close()
			return
		}
		// 客户端正常 FIN：把半关传给回落目标，让它把响应写完。
		if cw, ok := target.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	// 下行：回落目标 → 客户端。
	n, err := io.Copy(client, io.LimitReader(target, probeFallbackMaxDownload+1))
	if err != nil || n > probeFallbackMaxDownload {
		_ = client.Close()
	} else if cw, ok := client.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = target.Close()
	// 下行结束后上行再拷也没有接收方了；关掉 client 的读侧由调用方的 Close
	// 完成，这里只等上行 goroutine 在截止时间内退出。
	_ = client.SetReadDeadline(time.Now())
	wg.Wait()
}

// neutralHTTPHandler 是中性页面：所有请求都回 Go 标准库的 404。
var neutralHTTPHandler = http.HandlerFunc(http.NotFound)

var neutralHTTPErrorLog = log.New(io.Discard, "", 0)

// serveFallbackHTTP 用标准 net/http 接住一条认证失败的连接：h2 为真（TLS 协商
// 出 h2）时按 HTTP/2 服务，否则按 HTTP/1.x；请求交给 handler（中性 404 或
// 反代到回落）。prefix 先于 conn 上剩余的字节被读到。阻塞到会话结束（对端
// 断开、空闲超时或总时长上限）。
func serveFallbackHTTP(conn net.Conn, prefix []byte, h2 bool, handler http.Handler) {
	wrapped := newProbePrefixConn(conn, prefix)
	timer := time.AfterFunc(probeFallbackMaxDuration, func() { _ = wrapped.Close() })
	defer timer.Stop()
	base := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: inboundHandshakeTimeout,
		IdleTimeout:       neutralHTTPIdleTimeout,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          neutralHTTPErrorLog,
	}
	if h2 {
		(&http2.Server{IdleTimeout: neutralHTTPIdleTimeout}).ServeConn(wrapped, &http2.ServeConnOpts{BaseConfig: base, Handler: handler})
		_ = wrapped.Close()
		return
	}
	// 关掉 HTTP/2 升级（h2c），只讲 HTTP/1.x。
	base.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	listener := newSingleConnListener(wrapped)
	_ = base.Serve(listener)
	<-listener.closed
}

// negotiatedH2 报告 conn 的 TLS 是否协商出 h2。非 *tls.Conn 一律当 HTTP/1.x。
func negotiatedH2(conn net.Conn) bool {
	if tlsConn, ok := conn.(*tls.Conn); ok {
		return tlsConn.ConnectionState().NegotiatedProtocol == http2.NextProtoTLS
	}
	return false
}

// probePrefixConn 先吐出 prefix，再读底层连接；Close 只生效一次并通知监听壳。
type probePrefixConn struct {
	net.Conn
	prefix []byte
	once   sync.Once
	closed chan struct{}
}

func newProbePrefixConn(conn net.Conn, prefix []byte) *probePrefixConn {
	return &probePrefixConn{Conn: conn, prefix: append([]byte(nil), prefix...), closed: make(chan struct{})}
}

func (c *probePrefixConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func (c *probePrefixConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		close(c.closed)
	})
	return err
}

// singleConnListener 只交出一条连接；第二次 Accept 等到这条连接被关才返回
// 错误，好让 http.Server.Serve 在会话结束后退出。
type singleConnListener struct {
	conn   *probePrefixConn
	once   sync.Once
	closed chan struct{}
}

func newSingleConnListener(conn *probePrefixConn) *singleConnListener {
	return &singleConnListener{conn: conn, closed: conn.closed}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var first net.Conn
	l.once.Do(func() { first = l.conn })
	if first != nil {
		return first, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error   { return nil }
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// errProbeFallbackCapped 表示回落会话撞上了字节上限。
var errProbeFallbackCapped = errors.New("probe fallback byte limit reached")
