// [INPUT]: 依赖 anytls.go 的 anyTLSAdapter.Validate，core 的 InboundConfig
// [OUTPUT]: 对外提供 AnyTLS 入站配置校验单测
// [POS]: kernel 的 AnyTLS 默认单测；第三方客户端往返在 anytls_client_interop_test.go（-tags interop）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

func TestAnyTLSValidateRequiresConsistentTLSAndPadding(t *testing.T) {
	a := &anyTLSAdapter{}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "anytls", Port: 443, Raw: map[string]any{"tls": true}}}
	if err := a.Validate(spec); err == nil {
		t.Fatal("AnyTLS TLS without certificate accepted")
	}
	spec.Config.Raw = map[string]any{"cert_path": "cert", "tls": false}
	if err := a.Validate(spec); err == nil {
		t.Fatal("one-sided AnyTLS certificate accepted")
	}
	spec.Config.Raw = map[string]any{"padding_scheme": []any{""}}
	if err := a.Validate(spec); err == nil {
		t.Fatal("empty AnyTLS padding line accepted")
	}
}
