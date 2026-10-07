package kernel

import (
	"errors"
	"io"
)

// VLESS 认证判定之前的抗探测（与 trojan_fallback.go 同一套）。
//
// 原先 VLESS 的 TCP 直连承载（明文、TLS、REALITY、mKCP）在版本字节不对或 UUID
// 不在名单时立刻断开：0 秒断连本身就是代理指纹，而 Trojan / AnyTLS / Naive 已经
// 改成交给回落。现在一样：认证判定失败时把已读到的字节补发给 raw `fallback`
// 指定的回落目标，没配就由中性 404 页面接住（probe_fallback*.go）。

var (
	// errVLESSVersionRejected：首字节不是 VLESS 版本号（HTTP 探测、随机字节）。
	errVLESSVersionRejected = errors.New("vless 版本字节不对")
	// errVLESSUserRejected：格式对，但 UUID 不在名单里。
	errVLESSUserRejected = errors.New("vless 用户未授权")
)

// vlessPreAuthRecorder 记下认证判定之前读走的字节（最多 17 字节），认证失败时
// 原样补发给回落目标。认证通过后不再记录。
type vlessPreAuthRecorder struct {
	r      io.Reader
	buf    []byte
	authed bool
}

func (v *vlessPreAuthRecorder) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if !v.authed && n > 0 {
		v.buf = append(v.buf, p[:n]...)
	}
	return n, err
}

func (v *vlessPreAuthRecorder) authenticated() {
	v.authed = true
	v.buf = nil
}

// rejected 报告 err 是否是「认证判定为失败」：只有这种才交给回落；读超时、对端
// 断开、认证之后的协议错误照旧断开。
func (v *vlessPreAuthRecorder) rejected(err error) bool {
	if v.authed {
		return false
	}
	return errors.Is(err, errVLESSVersionRejected) || errors.Is(err, errVLESSUserRejected)
}
