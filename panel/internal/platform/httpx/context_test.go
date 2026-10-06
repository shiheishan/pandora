// [INPUT]: 依赖本包 context.go 的 ClientIP，依赖 net/http/httptest 造请求
// [OUTPUT]: 对外提供 TestClientIPTrustsOnlyXRealIP
// [POS]: platform/httpx 的来源地址口径：只信反代覆写的 X-Real-IP，X-Forwarded-For 一律不看，缺省回落到 RemoteAddr 的主机部分

package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustsOnlyXRealIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		realIP     string
		forwarded  string
		want       string
	}{
		{"real ip wins over forwarded", "127.0.0.1:41000", "203.0.113.7", "198.51.100.9", "203.0.113.7"},
		{"forwarded alone is ignored", "127.0.0.1:41000", "", "198.51.100.9, 203.0.113.7", "127.0.0.1"},
		{"no headers falls back to remote host", "192.0.2.4:5555", "", "", "192.0.2.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.realIP != "" {
				r.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := ClientIP(r); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
