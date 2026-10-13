package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// blockedPrefixes 是 netip 判断函数之外还要拦的网段。
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 熟知前缀，内嵌 IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"), // 本地 NAT64
	netip.MustParsePrefix("::/96"),          // 已废弃的 IPv4 兼容地址，内嵌 IPv4
	netip.MustParsePrefix("2002::/16"),      // 6to4，内嵌 IPv4
	netip.MustParsePrefix("2001::/32"),      // Teredo，内嵌（取反的）IPv4
	netip.MustParsePrefix("fec0::/10"),      // 已废弃的站点本地地址
}

// blockedAddrError 是拨号或保存时拦下非公网地址的错误。Error() 不带地址：错误文案会进投递记录与接口响应，
// 地址只经 addr 字段进日志（errors.As 取出）。
type blockedAddrError struct{ addr string }

func (e *blockedAddrError) Error() string { return "拒绝连接非公网地址" }

func checkIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return &blockedAddrError{addr: ip.String()}
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return &blockedAddrError{addr: ip.String()}
		}
	}
	return nil
}

func checkDialAddr(address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &blockedAddrError{addr: address}
	}
	return checkIP(ap.Addr())
}

// blockedAddrField 在 err 是被拦下的非公网地址时给出日志字段，否则是空字段。
func blockedAddrField(err error) zap.Field {
	var be *blockedAddrError
	if errors.As(err, &be) {
		return zap.String("blocked_addr", be.addr)
	}
	return zap.Skip()
}

func parseEndpoint(raw string, allowPrivate bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("回调地址必填")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("不是合法的地址")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("只支持 http/https")
	}
	if u.Scheme == "http" && !allowPrivate {
		return nil, errors.New("必须使用 https")
	}
	if u.User != nil {
		return nil, errors.New("地址里不能带用户名或口令")
	}
	if u.Hostname() == "" {
		return nil, errors.New("地址里没有主机名")
	}
	return u, nil
}

// validateEndpoint 保存钩子时用。allowPrivate 时只做静态检查；否则还会解析域名查内网。
// 真正的防线是拨号时的 checkDialAddr（防 DNS 重绑定）；这里只是保存时的提前提示。
func validateEndpoint(ctx context.Context, raw string, allowPrivate bool) error {
	u, err := parseEndpoint(raw, allowPrivate)
	if err != nil {
		return err
	}
	if allowPrivate {
		return nil
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if checkIP(ip) != nil {
			return errors.New("不允许指向内网或本机地址")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return errors.New("域名解析不了，请检查地址")
	}
	for _, ip := range ips {
		if checkIP(ip) != nil {
			return errors.New("该域名解析到内网地址，不允许")
		}
	}
	return nil
}

func newHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			return checkDialAddr(address)
		}
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Sign 计算签名头的值："sha256=" + hex(HMAC-SHA256(secret, ts + "." + body))。签名覆盖时间戳，录下的请求无法无限重放。
func Sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// postSigned 发一次签名 POST，返回状态码与往返耗时（毫秒）。请求没能构造出来时 durationMS 为 nil；
// 已发起的尝试（含连接失败、超时）都记耗时。返回的错误已去掉 URL 的路径与查询串（plog.StripURL）。
func postSigned(ctx context.Context, client *http.Client, endpoint, event, deliveryID string, body []byte,
	secret string, timeout time.Duration, now time.Time) (code int, durationMS *int, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return 0, nil, plog.StripURL(err)
		}
		return 0, nil, errors.New("不是合法的地址")
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PandoraPanel-Hook/1")
	req.Header.Set("X-Pandora-Event", event)
	req.Header.Set("X-Pandora-Delivery", deliveryID)
	req.Header.Set("X-Pandora-Timestamp", ts)
	if secret != "" {
		req.Header.Set("X-Pandora-Signature", Sign(secret, ts, body))
	}

	start := time.Now()
	resp, err := client.Do(req)
	ms := int(time.Since(start).Milliseconds())
	if err != nil {
		// 被拨号防护拦下时只回不带地址的文案（net.OpError 的文案里有 IP）；地址留在错误里给日志用
		var be *blockedAddrError
		if errors.As(err, &be) {
			return 0, &ms, be
		}
		return 0, &ms, plog.StripURL(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, &ms, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
