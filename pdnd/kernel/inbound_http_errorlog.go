package kernel

import (
	"errors"
	"io"
	"log"
	"log/slog"
	"strings"
)

// HTTP 承载入站（WebSocket、HTTP Upgrade、gRPC、XHTTP、Naive）的 http.Server 不能
// 留空 ErrorLog：net/http 会把「http: TLS handshake error from 完整IP:端口: …」之类
// 逐条写进标准库 log，客户端或扫描器每握手失败一次就一行、不限流，还落下完整用户
// IP（节点日志只许出现截到网段的地址，见 maskRemoteAddr）。这里接住它：
//   - TLS 握手失败改走适配器的 OnConnError（限流、地址截网段、原因脱敏），与其他
//     入站的握手失败同一条观测链；对端只发 EOF 的照例不报；
//   - 处理请求时的 panic 是我们自己的缺陷，不能吞：地址截网段后按 Error 记；
//   - 其余（读请求头出错、h2 前言不对等）都是对端造成的噪声，丢弃。

const (
	httpTLSHandshakeErrorPrefix = "http: TLS handshake error from "
	httpPanicServingPrefix      = "http: panic serving "
)

// inboundHTTPErrorLog 返回给 http.Server.ErrorLog 用的 logger。report 为零值时
// TLS 握手失败直接丢弃（不经 TLS 承载、或握手已在适配器里做完的入站）。
func inboundHTTPErrorLog(report connErrorReporter) *log.Logger {
	return log.New(inboundHTTPErrorWriter{report: report}, "", 0)
}

type inboundHTTPErrorWriter struct{ report connErrorReporter }

func (w inboundHTTPErrorWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	switch {
	case strings.HasPrefix(line, httpTLSHandshakeErrorPrefix):
		remote, reason, ok := strings.Cut(strings.TrimPrefix(line, httpTLSHandshakeErrorPrefix), ": ")
		if !ok {
			break
		}
		var err error
		if reason == io.EOF.Error() {
			err = io.EOF
		} else {
			err = errors.New(reason)
		}
		w.report.request(StageTLSHandshake, remote, err)
	case strings.HasPrefix(line, httpPanicServingPrefix):
		remote, detail, _ := strings.Cut(strings.TrimPrefix(line, httpPanicServingPrefix), ": ")
		slog.Error("HTTP 承载处理请求时 panic，已断开该连接", "protocol", w.report.protocol,
			"remote", maskRemoteAddr(stringAddr(remote)), "detail", detail)
	}
	return len(p), nil
}
