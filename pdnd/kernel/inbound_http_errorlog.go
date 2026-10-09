package kernel

import (
	"errors"
	"io"
	"log"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
)

// HTTP 承载入站（WebSocket、HTTP Upgrade、gRPC、XHTTP、Naive）的 http.Server 不能
// 留空 ErrorLog：net/http 与 x/net/http2 会把「http: TLS handshake error from 完整IP:端口: …」
// 之类逐条写进标准库 log，客户端或扫描器每握手失败一次就一行、不限流，还落下完整用户
// IP（节点日志只许出现截到网段的地址，见 maskRemoteAddr）。这里接住它：
//   - TLS 握手失败改走适配器的 OnConnError（限流、地址截网段、原因脱敏），与其他
//     入站的握手失败同一条观测链；对端只发 EOF 的照例不报；
//   - 处理请求时的 panic（h1 的「http: panic serving」与 h2 / h2c 的「http2: panic
//     serving」）是我们自己的缺陷：按 Error 记；
//   - 明确由对端造成的噪声（httpPeerNoisePrefixes 逐条列出）丢弃；
//   - 其余一律按 WARN 记，不丢：里面有本机故障信号（「http: Accept error: … too many
//     open files」即 fd 耗尽、入站停止接客）和我们自己的缺陷（superfluous WriteHeader）。
//
// 记之前把行内的 ip:port（IPv4 与 [IPv6]:port）截到网段，口径同 maskRemoteAddr。

const httpTLSHandshakeErrorPrefix = "http: TLS handshake error from "

// httpPanicPrefixes 是 handler panic 的两种写法：Go net/http（h1）与 x/net/http2、
// 以及 net/http 内置的 h2（h2 / h2c）。
var httpPanicPrefixes = []string{"http: panic serving ", "http2: panic serving "}

// httpPeerNoisePrefixes 是只由对端行为引起、对排障没有价值的行（x/net/http2 server.go
// 的 logf 调用逐条对照）。h1 读请求头出错不写日志（直接回 400），不在此列。
var httpPeerNoisePrefixes = []string{
	"http2: server: error reading preface from client ", // 对端不说 h2 前言（扫描器、错协议）
	"http2: server connection error from ",              // 对端发了违反协议的帧
	"http2: server closing client connection: ",         // 读对端帧出错
	"timeout waiting for SETTINGS frames from ",         // 对端握完不发 SETTINGS
	"timeout waiting for PING response",                 // 对端不回 PING
	"http2: received GOAWAY ",                           // 对端主动 GOAWAY
}

// inboundHTTPErrorLog 返回给 http.Server.ErrorLog 用的 logger。report 为零值时
// TLS 握手失败直接丢弃（不经 TLS 承载、或握手已在适配器里做完的入站）。
func inboundHTTPErrorLog(report connErrorReporter) *log.Logger {
	return log.New(inboundHTTPErrorWriter{report: report}, "", 0)
}

type inboundHTTPErrorWriter struct{ report connErrorReporter }

func (w inboundHTTPErrorWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if rest, ok := strings.CutPrefix(line, httpTLSHandshakeErrorPrefix); ok {
		if remote, reason, ok := strings.Cut(rest, ": "); ok {
			var err error = errors.New(reason)
			if reason == io.EOF.Error() {
				err = io.EOF
			}
			w.report.request(StageTLSHandshake, remote, err)
		}
		return len(p), nil
	}
	for _, prefix := range httpPanicPrefixes {
		if strings.HasPrefix(line, prefix) {
			slog.Error("HTTP 承载处理请求时 panic，已断开该连接", "protocol", w.report.protocol, "detail", maskAddrsInLine(line))
			return len(p), nil
		}
	}
	for _, prefix := range httpPeerNoisePrefixes {
		if strings.HasPrefix(line, prefix) {
			return len(p), nil
		}
	}
	slog.Warn("HTTP 承载服务端日志", "protocol", w.report.protocol, "detail", maskAddrsInLine(line))
	return len(p), nil
}

var (
	// [IPv6]:port（可带 zone）与 IPv4:port，端口可无。
	lineIPv6PortRE = regexp.MustCompile(`\[[0-9A-Fa-f:.]+(?:%[^\]]*)?\](?::\d+)?`)
	lineIPv4PortRE = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
)

// maskAddrsInLine 把一行日志里的 ip:port 换成 maskRemoteAddr 的网段写法
// （IPv4 /24、IPv6 /48，丢掉端口）。不是合法地址的片段原样保留。
func maskAddrsInLine(line string) string {
	line = lineIPv6PortRE.ReplaceAllStringFunc(line, func(s string) string {
		host := s[1:strings.IndexByte(s, ']')]
		if i := strings.IndexByte(host, '%'); i >= 0 {
			host = host[:i]
		}
		return maskHostString(host, s)
	})
	return lineIPv4PortRE.ReplaceAllStringFunc(line, func(s string) string {
		host, _, _ := strings.Cut(s, ":")
		return maskHostString(host, s)
	})
}

func maskHostString(host, original string) string {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return original
	}
	if masked := maskRemoteAddr(stringAddr(netip.AddrPortFrom(ip, 0).String())); masked != "" {
		return masked
	}
	return original
}
