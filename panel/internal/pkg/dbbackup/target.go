package dbbackup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Resolver is deliberately small so endpoint policy can be tested without
// touching the network. Production uses net.DefaultResolver.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Target struct {
	Origin       *url.URL
	BasePath     string
	AllowPrivate bool
	username     string
	password     string
}

func (t Target) String() string {
	return fmt.Sprintf("Target{Origin:%s BasePath:%s AllowPrivate:%t credentials:redacted}", t.Origin, t.BasePath, t.AllowPrivate)
}

func (t Target) GoString() string { return t.String() }

func ParseTarget(ctx context.Context, resolver Resolver, endpoint, basePath, username, password string,
	allowPrivate bool) (Target, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return Target{}, errors.New("WebDAV 地址格式不正确")
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Target{}, errors.New("WebDAV 地址必须是无凭据、查询参数和片段的 HTTPS 地址")
	}
	if u.Path != "" && u.Path != "/" {
		return Target{}, errors.New("WebDAV 地址只填写站点，目录请填写到远端路径")
	}
	if u.RawPath != "" || strings.HasSuffix(u.Hostname(), ".") {
		return Target{}, errors.New("WebDAV 地址包含不允许的编码或主机名")
	}
	if port := u.Port(); port != "" {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return Target{}, errors.New("WebDAV 端口必须在 1 到 65535 之间")
		}
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return Target{}, errors.New("WebDAV 地址不能指向本机")
	}
	if net.ParseIP(host) == nil && !validASCIIHostname(host) {
		return Target{}, errors.New("WebDAV 主机名必须是规范的 ASCII DNS 名称")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if err := validateResolvedHost(ctx, resolver, host, allowPrivate); err != nil {
		return Target{}, err
	}
	basePath, err = NormalizeBasePath(basePath)
	if err != nil {
		return Target{}, err
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return Target{Origin: u, BasePath: basePath, username: username, password: password, AllowPrivate: allowPrivate}, nil
}

func validASCIIHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
	}
	return true
}

func NormalizeBasePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "/pandora-backups"
	}
	if !strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\\%?#") {
		return "", errors.New("远端路径必须是未编码的绝对路径")
	}
	parts := strings.Split(raw, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if part == "." || part == ".." {
			return "", errors.New("远端路径不能包含 . 或 ..")
		}
		for _, r := range part {
			if unicode.IsControl(r) || r == 0 {
				return "", errors.New("远端路径包含控制字符")
			}
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "", errors.New("远端路径不能是根目录")
	}
	return "/" + strings.Join(out, "/"), nil
}

func validateResolvedHost(ctx context.Context, resolver Resolver, host string, allowPrivate bool) error {
	if literal, err := netip.ParseAddr(host); err == nil {
		if !allowedAddress(literal, allowPrivate) {
			return errors.New("WebDAV 地址指向受保护的网络")
		}
		return nil
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return errors.New("无法解析 WebDAV 主机")
	}
	for _, addr := range addrs {
		if !allowedAddress(addr, allowPrivate) {
			return fmt.Errorf("WebDAV 主机解析到受保护的网络")
		}
	}
	return nil
}

var alwaysBlockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001:db8::/32",
	"fe80::/10", "ff00::/8", "64:ff9b:1::/48", "fd00:ec2::254/128",
)

var privatePrefixes = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")

func mustPrefixes(values ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}

func allowedAddress(addr netip.Addr, allowPrivate bool) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range alwaysBlockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	if !allowPrivate {
		for _, prefix := range privatePrefixes {
			if prefix.Contains(addr) {
				return false
			}
		}
	}
	return true
}
