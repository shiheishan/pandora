package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/url"
	"strings"
	"testing"
)

func TestValidKey(t *testing.T) {
	key255 := strings.Repeat("a", 255)
	key256 := strings.Repeat("a", 256)
	cases := []struct {
		name string
		key  string
		want bool
	}{
		{"空串", "", false},
		{"255 字节", key255, true},
		{"256 字节", key256, false},
		{"含 NUL", "a\x00b", false},
		{"非法 UTF-8", "a\xffb", false},
		{"含 0x1f", "a\x1fb", false},
		{"含 0x7f", "a\x7fb", false},
		{"含中文", "幂等键-测试", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidKey(tc.key); got != tc.want {
				t.Fatalf("ValidKey(%q) = %v，期望 %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestValidScope(t *testing.T) {
	scope65 := strings.Repeat("a", 65)
	cases := []struct {
		name  string
		scope string
		want  bool
	}{
		{"order_create", "order_create", true},
		{"空", "", false},
		{"65 字符", scope65, false},
		{"大写", "Order", false},
		{"连字符", "order-create", false},
		{"冒号", "order:create", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidScope(tc.scope); got != tc.want {
				t.Fatalf("ValidScope(%q) = %v，期望 %v", tc.scope, got, tc.want)
			}
		})
	}
}

func TestTarget(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"仅路径", "/v1/orders", "/v1/orders"},
		{"路径与查询", "/v1/orders?a=1&b=2", "/v1/orders?a=1&b=2"},
		{"空路径", "", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			if got := Target(u); got != tc.want {
				t.Fatalf("Target = %q，期望 %q", got, tc.want)
			}
		})
	}
	t.Run("保留转义", func(t *testing.T) {
		u, err := url.Parse("/v1/a%20b")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		want := u.EscapedPath()
		if got := Target(u); got != want {
			t.Fatalf("Target = %q，期望 EscapedPath %q", got, want)
		}
	})
}

func TestNewFingerprint(t *testing.T) {
	key := []byte("test-idempotency-fingerprint-key")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("POST\n/v1/orders\n{\"a\":1}"))
	var want Fingerprint
	copy(want[:], mac.Sum(nil))
	got := NewFingerprint(key, "POST", "/v1/orders", []byte("{\"a\":1}"))
	if got != want {
		t.Fatalf("指纹与预期 HMAC-SHA256 不一致")
	}
	if NewFingerprint([]byte("another-key"), "POST", "/v1/orders", []byte("{\"a\":1}")) == got {
		t.Fatal("密钥不同应得到不同指纹")
	}

	base := NewFingerprint(key, "POST", "/v1/orders", []byte("{\"a\":1}"))
	if NewFingerprint(key, "GET", "/v1/orders", []byte("{\"a\":1}")) == base {
		t.Fatal("方法不同应得到不同指纹")
	}
	if NewFingerprint(key, "POST", "/v1/other", []byte("{\"a\":1}")) == base {
		t.Fatal("target 不同应得到不同指纹")
	}
	if NewFingerprint(key, "POST", "/v1/orders", []byte("{\"a\":2}")) == base {
		t.Fatal("body 不同应得到不同指纹")
	}

	// 口径照旧：method + "\n" + target + "\n" + body，不另加分隔；target 是转义后的路径，不含换行。
}
