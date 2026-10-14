// Package safehttp 提供支付渠道适配器共用的外部 HTTP 客户端：不走环境代理、不跟随跳转、拨号时按真实 IP 拦非公网地址（防 SSRF 与 DNS 重绑定）。
package safehttp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

const (
	// Timeout 是外部 HTTP 请求的最大超时时间。
	Timeout = 10 * time.Second
	// MaxResponseBytes 是响应体允许读取的最大字节数（256 KiB）。
	MaxResponseBytes = 256 * 1024
)

// NewClient 构造防 SSRF 的 HTTP 客户端：不走环境代理、不跟随跳转；
// 不允许内网时在拨号那一刻按真正要连的 IP 拦截（防 DNS 重绑定：解析结果在检查与连接之间被换掉）。
func NewClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			return CheckDialAddr(address)
		}
	}
	transport := &http.Transport{
		Proxy:                 nil, // 走代理时拦截的就成了代理地址，所以不用环境代理
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: Timeout,
	}
	return &http.Client{
		Timeout:   Timeout,
		Transport: transport,
		// 不跟随跳转：支付接口的响应应当直出，跳转多半意味着被劫持或配错
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// blockedPrefixes 是 netip 判断函数之外还要拦的网段。
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // 「本网络」
	netip.MustParsePrefix("100.64.0.0/10"), // 运营商级 NAT，部分云的元数据地址在这里
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF 协议分配
	netip.MustParsePrefix("198.18.0.0/15"), // 基准测试网段
	netip.MustParsePrefix("240.0.0.0/4"),   // 保留与广播
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64，可映射到任意 IPv4（含内网）
}

// CheckDialAddr 拒绝连向非公网地址：环回、私网（含 IPv6 ULA fc00::/7）、链路本地（含 169.254.169.254 元数据、fe80::/10）、
// 未指定、组播，以及 blockedPrefixes。address 是拨号时的 ip:port，已经是解析后的地址。
func CheckDialAddr(address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("safehttp: 无法识别要连接的地址 %q", address)
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("safehttp: 拒绝连接非公网地址 %s", ip)
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("safehttp: 拒绝连接非公网地址 %s", ip)
		}
	}
	return nil
}

// ReadBody 读取 HTTP 响应体，限制最大读取 MaxResponseBytes 字节。
// 超过 MaxResponseBytes 会返回错误。
func ReadBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxResponseBytes {
		return nil, errors.New("safehttp: 响应超过 256 KiB")
	}
	return data, nil
}
