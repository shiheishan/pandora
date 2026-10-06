package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/sagernet/quic-go"
)

func TestClassifyConnError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"标记优先", fmt.Errorf("wrapped: %w", markConnError(connErrAuth, io.ErrUnexpectedEOF)), connErrAuth},
		{"设备上限", deviceLimitError("vless"), connErrLimit},
		{"上游拨号", markConnError(connErrUpstream, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), connErrUpstream},
		{"读超时", &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, connErrTimeout},
		{"context 超时", context.DeadlineExceeded, connErrTimeout},
		{"半截断开", fmt.Errorf("header: %w", io.ErrUnexpectedEOF), connErrTruncated},
		{"对端 RST", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, connErrReset},
		{"TLS 记录头", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, connErrTLS},
		{"TLS 文本错误", errors.New("tls: client offered only unsupported versions"), connErrTLS},
		{"其余", errors.New("vless version 无效"), connErrProtocol},
	}
	for _, tc := range cases {
		if got := classifyConnError(tc.err); got != tc.want {
			t.Errorf("%s: classify(%v) = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestMarkConnErrorKeepsTextAndChain(t *testing.T) {
	inner := errors.New("trojan user proof rejected")
	marked := markConnError(connErrAuth, inner)
	if marked.Error() != inner.Error() || !errors.Is(marked, inner) {
		t.Fatalf("标记改动了错误文本或断了 errors.Is 链: %v", marked)
	}
	if markConnError(connErrAuth, nil) != nil {
		t.Fatal("nil 错误被包成了非 nil")
	}
}

func TestSanitizeConnErrorReasonRedactsSecretsAndTargets(t *testing.T) {
	const fakeUUID = "0f3c2a1b-4d5e-4f60-8a9b-c0d1e2f3a4b5"
	// 虚构的长串，运行时拼出来，免得泄露扫描把夹具当成真密钥。
	fakeToken := strings.Repeat("Zm9vYmFy", 4)
	err := fmt.Errorf("connection failed: authentication: unknown user %s; dial tcp 198.51.100.7:443 via [2001:db8::1]:8443 for upstream.example.test:80 key %s",
		fakeUUID, fakeToken)
	got := sanitizeConnErrorReason(err)
	for _, leak := range []string{fakeUUID, "198.51.100.7", "2001:db8", "upstream.example.test", fakeToken} {
		if strings.Contains(got, leak) {
			t.Fatalf("脱敏后仍含 %q: %s", leak, got)
		}
	}
	for _, keep := range []string{"authentication", "unknown user", "dial tcp"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("脱敏把错误骨架 %q 也抹了: %s", keep, got)
		}
	}
	long := sanitizeConnErrorReason(errors.New(strings.Repeat("坏 ", 200) + "\n尾巴"))
	if len(long) > connReasonMaxBytes+len("…") || strings.ContainsAny(long, "\n\r") {
		t.Fatalf("超长或含控制字符的原因没被截断清理: %q", long)
	}
}

func TestMaskRemoteAddr(t *testing.T) {
	cases := []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.ParseIP("203.0.113.77"), Port: 51000}, "203.0.113.0/24"},
		{&net.UDPAddr{IP: net.ParseIP("2001:db8:abcd:12::9"), Port: 443}, "2001:db8:abcd::/48"},
		{&net.TCPAddr{IP: net.ParseIP("::ffff:192.0.2.9"), Port: 1}, "192.0.2.0/24"},
		{stringAddr("198.51.100.200:8080"), "198.51.100.0/24"},
		{stringAddr("not-an-address"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := maskRemoteAddr(tc.addr); got != tc.want {
			t.Errorf("maskRemoteAddr(%v) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestReportAdapterConnErrorSkipsNormalEndings(t *testing.T) {
	var got []ConnError
	reporter := connErrorReporter{hook: func(ev ConnError) { got = append(got, ev) }, tag: "t", protocol: "vless"}
	for _, err := range []error{nil, io.EOF, net.ErrClosed, fmt.Errorf("x: %w", context.Canceled)} {
		reporter.addr(StageSession, nil, err)
	}
	if len(got) != 0 {
		t.Fatalf("正常收尾被当成失败上报: %+v", got)
	}
	reporter.addr(StageSession, nil, errors.New("vless 用户未授权"))
	if len(got) != 1 || got[0].Tag != "t" || got[0].Protocol != "vless" || got[0].Stage != StageSession {
		t.Fatalf("上报内容不对: %+v", got)
	}
	// 零值 reporter（测试里直接构造的适配器）必须是空操作。
	connErrorReporter{}.conn(StageSession, nil, errors.New("boom"))
}

func TestSingConnErrorLoggerFiltersQUICNormalClose(t *testing.T) {
	var got []ConnError
	reporter := connErrorReporter{hook: func(ev ConnError) { got = append(got, ev) }, tag: "t", protocol: "tuic"}
	bridge := newSingConnErrorLogger(reporter, markTUICAuthError)
	bridge.Debug("debug 级别不该上报")
	bridge.Warn("warn 级别不该上报")
	bridge.Error(fmt.Errorf("send heartbeat: %w", &quic.ApplicationError{Remote: true, ErrorCode: 0}))
	bridge.Error(fmt.Errorf("idle: %w", &quic.IdleTimeoutError{}))
	if len(got) != 0 {
		t.Fatalf("正常的 QUIC 收尾被当成失败上报: %+v", got)
	}
	bridge.Error(errors.New("connection failed: handle uni stream: authentication: token mismatch"))
	bridge.Error("seek frame type", ": ", "bad")
	if len(got) != 2 {
		t.Fatalf("Error 级日志没有上报: %+v", got)
	}
	if classifyConnError(got[0].Err) != connErrAuth || classifyConnError(got[1].Err) != connErrProtocol {
		t.Fatalf("分类不对: %v / %v", got[0].Err, got[1].Err)
	}
}
