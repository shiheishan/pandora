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
