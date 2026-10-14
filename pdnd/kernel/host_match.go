package kernel

import (
	"net"
	"strings"
)

// requestHostMatches 判断 HTTP 承载（WebSocket、HTTP Upgrade、gRPC、XHTTP）
// 请求里的 Host 是否就是入站配置要求的那个。
//
// 只比主机名、不比端口：客户端在非默认端口上会按 RFC 9110 把端口带进 Host
// （node.example.com:8443），gRPC 的 :authority 也一样，而面板下发的 host 只是
// 主机名。以前原样相等比较，非 443 端口上的 ws / grpc 节点对所有客户端都 404。
// 安全语义不变：配了 host 就仍然只放行这个主机名（大小写不敏感），只是不再
// 因为端口误杀；没配 host 时照旧不检查。IPv6 字面量带方括号（[2001:db8::1]:443
// 或 [2001:db8::1]），两种都去掉括号再比。
func requestHostMatches(requestHost, want string) bool {
	want = normalizeHostForMatch(want)
	if want == "" {
		return true
	}
	return strings.EqualFold(normalizeHostForMatch(requestHost), want)
}

// requestHostMatchesAny 是 requestHostMatches 的多名字版：任一匹配即放行，
// 名单里有空串（没配 host）或名单为空时不检查。
func requestHostMatchesAny(requestHost string, wants []string) bool {
	if len(wants) == 0 {
		return true
	}
	for _, want := range wants {
		if requestHostMatches(requestHost, want) {
			return true
		}
	}
	return false
}

// normalizeHostForMatch 去掉首尾空白、端口与 IPv6 方括号。不是 host[:port]
// 形状的值原样返回（去空白），交给相等比较去拒。
func normalizeHostForMatch(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return value[1 : len(value)-1]
	}
	return value
}
