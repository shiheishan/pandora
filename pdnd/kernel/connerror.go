package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// ============================================================
//  上报出口
// ============================================================

// connErrorReporter 把 hook、入站 tag、协议名在 Start 时绑一次。
//
// 十几个协议、几十个失败点，每处都手写 reportAdapterConnError(a.hook,
// a.spec.Config.Tag, "xxx", ...) 的话，协议名迟早会有一处抄错。零值可用：
// hook 为 nil 时所有调用都是空操作，测试里直接构造的适配器不用关心它。
type connErrorReporter struct {
	hook     func(ConnError)
	tag      string
	protocol string
}

func newConnErrorReporter(hooks AdapterHooks, spec InboundSpec, protocol string) connErrorReporter {
	return connErrorReporter{hook: hooks.OnConnError, tag: spec.Config.Tag, protocol: protocol}
}

// conn 上报一条手上有 net.Conn 的失败，对端地址从连接上取。
func (r connErrorReporter) conn(stage string, conn net.Conn, err error) {
	reportAdapterConnError(r.hook, r.tag, r.protocol, stage, conn, err)
}

// addr 上报一条只有对端地址（或连地址都没有）的失败。
func (r connErrorReporter) addr(stage string, remote net.Addr, err error) {
	reportAdapterConnErrorAddr(r.hook, r.tag, r.protocol, stage, remote, err)
}

// request 上报一条 HTTP 承载上的失败，对端地址只有 http.Request.RemoteAddr 那个字符串。
func (r connErrorReporter) request(stage string, remoteAddr string, err error) {
	var remote net.Addr
	if remoteAddr != "" {
		remote = stringAddr(remoteAddr)
	}
	r.addr(stage, remote, err)
}

// stringAddr 把 "ip:port" 字符串包成 net.Addr，只为交给 maskRemoteAddr 截网段。
type stringAddr string

func (a stringAddr) Network() string { return "" }
func (a stringAddr) String() string  { return string(a) }

// realityHandshake 的签名正好是 RealityListener.SetHandshakeErrorHandler 要的：
// REALITY 握手在 listener 内部完成，失败的连接根本到不了适配器的 acceptLoop，
// 不在 listener 上接这一道，握手层的失败永远看不到。
func (r connErrorReporter) realityHandshake(remote net.Addr, err error) {
	r.addr(StageTLSHandshake, remote, err)
}

// ============================================================
//  错误分类
// ============================================================

// 连接失败的分类。Stage 回答「卡在哪一步」，分类回答「为什么」：同样卡在
// session，auth 说明客户端凭据不对，upstream 说明节点出站有问题，protocol
// 多半是扫描器或客户端协议配错。日志与限流都按这一维聚合。
const (
	// connErrAuth：凭据不对——UUID、密码、PSK、token 校验不过。
	connErrAuth = "auth"
	// connErrLimit：凭据对，但用户的设备数（或 hy2 / TUIC 的 UDP 会话数）到上限了。
	connErrLimit = "limit"
	// connErrUpstream：鉴权已过，经 DataPlane 拨目标或开 UDP 失败（含路由拒绝）。
	connErrUpstream = "upstream"
	// connErrTLS：TLS 记录层或握手层的错误。
	connErrTLS = "tls"
	// connErrTimeout：读写超时，最常见的是握手阶段对端不说话。
	connErrTimeout = "timeout"
	// connErrTruncated：握手读到一半对端断开。
	connErrTruncated = "truncated"
	// connErrReset：对端 RST / 管道断开。
	connErrReset = "reset"
	// connErrProtocol：其余一切——请求头非法、版本或命令不支持、首包不是本协议。
	connErrProtocol = "protocol"
)

// classifiedConnError 给错误挂一个分类，错误文本原样不变。
//
// 用包装而不是改错误文本，是因为各协议的错误信息已经被测试和运维文档引用，
// 分类只是附加在它身上的一维，不该反过来改动它。
type classifiedConnError struct {
	category string
	err      error
}

func (e *classifiedConnError) Error() string { return e.err.Error() }
func (e *classifiedConnError) Unwrap() error { return e.err }

// markConnError 在失败点把分类钉在错误上；err 为 nil 时返回 nil。
func markConnError(category string, err error) error {
	if err == nil {
		return nil
	}
	return &classifiedConnError{category: category, err: err}
}

// deviceLimitError 是各协议「设备数到上限」的统一写法，文本保持 "<协议> device limit"。
func deviceLimitError(protocol string) error {
	return markConnError(connErrLimit, fmt.Errorf("%s device limit", protocol))
}

// classifyConnError 把错误归到上面的分类之一。
//
// 失败点打过标记的优先；否则按标准库的错误类型判断；都不像的归 protocol。
// 这里只看类型不看文本，唯一的例外是 TLS：crypto/tls 的大部分握手错误
// 只是 "tls: ..." 前缀的字符串，没有可供 errors.As 的类型。
func classifyConnError(err error) string {
	var marked *classifiedConnError
	if errors.As(err, &marked) {
		return marked.category
	}
	var netErr net.Error
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return connErrTimeout
	case errors.Is(err, io.ErrUnexpectedEOF):
		return connErrTruncated
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNABORTED):
		return connErrReset
	case isTLSError(err):
		return connErrTLS
	}
	return connErrProtocol
}

func isTLSError(err error) bool {
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var verify *tls.CertificateVerificationError
	if errors.As(err, &record) || errors.As(err, &alert) || errors.As(err, &verify) {
		return true
	}
	return strings.Contains(err.Error(), "tls: ")
}

// admissionError 是 QUIC 系协议（TUIC / Hysteria2）子流入场的两道检查：
// 上下文里没有已认证用户归 auth，设备数到上限归 limit，都过了返回 nil。
func admissionError(protocol string, authorized, admitted bool) error {
	if !authorized {
		return markConnError(connErrAuth, fmt.Errorf("%s user is not authorized", protocol))
	}
	if !admitted {
		return deviceLimitError(protocol)
	}
	return nil
}

// ============================================================
//  脱敏
// ============================================================

// 错误文本会带上不该落盘的东西：上游库把 UUID 拼进 "unknown user"，拨号
// 错误带着目标 IP:port 与域名，HTTP 代理解析失败会回显整段 URL。这里按
// 「宁可多抹」的原则把它们换成占位符，留下的只是错误的骨架。
var connReasonScrubbers = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), "<uuid>"},
	{regexp.MustCompile(`(?i)\[?[0-9a-f]{0,4}(:[0-9a-f]{0,4}){2,7}(%[0-9a-z]+)?\]?(:[0-9]+)?`), "<addr>"},
	{regexp.MustCompile(`[0-9]{1,3}(\.[0-9]{1,3}){3}(:[0-9]+)?`), "<addr>"},
	{regexp.MustCompile(`(?i)[a-z0-9_-]+(\.[a-z0-9_-]+)*\.[a-z][a-z0-9-]+(:[0-9]+)?`), "<host>"},
	// 长串：hex 哈希、base64 密钥。不含 '-'，免得把 xtls-rprx-vision-udp443 这类 flow 名也抹掉。
	{regexp.MustCompile(`[A-Za-z0-9+/=_]{20,}`), "<redacted>"},
}

// connReasonMaxBytes 限制单条原因的长度，挡住对端塞进来的超长回显。
const connReasonMaxBytes = 160

// sanitizeConnErrorReason 返回可以落盘的错误原因：抹掉凭据、地址、域名与长串，
// 去掉控制字符，再截断。
func sanitizeConnErrorReason(err error) string {
	if err == nil {
		return ""
	}
	reason := err.Error()
	for _, scrub := range connReasonScrubbers {
		reason = scrub.pattern.ReplaceAllString(reason, scrub.replace)
	}
	reason = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, reason)
	if len(reason) > connReasonMaxBytes {
		cut := connReasonMaxBytes
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut] + "…"
	}
	return reason
}

// maskRemoteAddr 把对端地址截成网段：IPv4 留 /24，IPv6 留 /48，丢掉端口。
//
// 与面板 middleware.ByIPPrefix 的聚合粒度一致。网段足够判断「是同一批
// 扫描器」还是「某个用户所在的运营商」，又不在节点日志里留下用户的完整
// IP——完整在线 IP 只走签名通道交给面板，不该在本机日志里再存一份。
func maskRemoteAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	var ip netip.Addr
	switch a := addr.(type) {
	case *net.TCPAddr:
		ip, _ = netip.AddrFromSlice(a.IP)
	case *net.UDPAddr:
		ip, _ = netip.AddrFromSlice(a.IP)
	default:
		if ap, err := netip.ParseAddrPort(addr.String()); err == nil {
			ip = ap.Addr()
		} else if parsed, err := netip.ParseAddr(addr.String()); err == nil {
			ip = parsed
		}
	}
	if !ip.IsValid() {
		return ""
	}
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	prefix, err := ip.WithZone("").Prefix(bits)
	if err != nil {
		return ""
	}
	return prefix.String()
}
