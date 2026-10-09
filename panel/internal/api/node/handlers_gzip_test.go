package node

import (
	"net/http/httptest"
	"testing"
)

// 名单正文直接给压好的 gzip，前提是请求明说接受；q=0 是明确拒绝。
func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                    false,
		"gzip":                true,
		"GZIP":                true,
		"deflate, gzip;q=0.8": true,
		"br, gzip ; q=1":      true,
		"gzip;q=0":            false,
		"gzip; q=0.000":       false,
		"identity":            false,
		"x-gzip":              false,
		"deflate, br":         false,
	} {
		r := httptest.NewRequest("GET", "/api/v1/server/UniProxy/user", nil)
		if header != "" {
			r.Header.Set("Accept-Encoding", header)
		}
		if got := acceptsGzip(r); got != want {
			t.Errorf("Accept-Encoding %q: acceptsGzip=%v want %v", header, got, want)
		}
	}
}
