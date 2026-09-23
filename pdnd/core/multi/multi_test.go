package multi

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

func TestPickPrefersNativeForVerifiedProtocols(t *testing.T) {
	// Exercise the transitional dispatcher explicitly. Production New() is
	// NativeCore-only and intentionally routes unknown combinations to the
	// native validator so they fail closed instead of falling back.
	c := NewWithOptions(slog.Default(), Options{NativeOnly: false})
	for _, protocol := range []string{"vless", "trojan", "shadowsocks", "ss", "mieru", "juicity"} {
		cfg := &core.InboundConfig{Protocol: protocol, Port: 443}
		if got := c.pick(cfg); got != c.native {
			t.Fatalf("pick(%s) = %T, want NativeCore", protocol, got)
		}
	}
	if got := c.pick(&core.InboundConfig{Protocol: "mieru", Port: 443, Raw: map[string]any{"transport": "udp"}}); got != c.native {
		t.Fatal("native mieru UDP did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vmess", Port: 443, Raw: map[string]any{"security": "none"}}); got != c.native {
		t.Fatal("verified vmess none did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vmess", Port: 443}); got != c.native {
		t.Fatal("default vmess did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vmess", Port: 443, Raw: map[string]any{"network": "grpc", "security": "aes-128-gcm"}}); got != c.native {
		t.Fatal("native vmess grpc did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vmess", Port: 443, Raw: map[string]any{"network": "xhttp", "security": "none"}}); got != c.native {
		t.Fatal("native vmess xhttp did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vmess", Port: 443, Raw: map[string]any{"network": "xhttp-h3", "security": "none"}}); got != c.native {
		t.Fatal("native vmess xhttp-h3 did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "socks", Port: 1080, Raw: map[string]any{"network": "udp"}}); got != c.native {
		t.Fatal("native socks UDP did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vless", Port: 443, Raw: map[string]any{"network": "quic"}}); got == c.native {
		t.Fatal("unsupported native vless transport unexpectedly selected NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vless", Port: 443, Raw: map[string]any{"network": "xhttp-h3"}}); got != c.native {
		t.Fatal("native vless xhttp-h3 did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vless", Port: 443, Raw: map[string]any{"network": "xhttp-h3", "security": "reality"}}); got != c.native {
		t.Fatal("native boundary must not silently select a third-party core")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "shadowsocks", Port: 443, Raw: map[string]any{"method": "2022-blake3-aes-128-gcm", "network": "udp"}}); got != c.native {
		t.Fatal("supported native shadowsocks 2022 UDP did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "shadowsocks", Port: 443, Raw: map[string]any{"method": "2022-blake3-aes-128-gcm", "network": "udp", "password": "cHNr:cHNr"}}); got == c.native {
		t.Fatal("unsupported native shadowsocks 2022 UDP multi-PSK unexpectedly selected NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "hysteria2", Port: 443, Raw: map[string]any{"network": "udp"}}); got != c.native {
		t.Fatal("hysteria2 did not select NativeCore for strict validation")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "hysteria2", Port: 443, Raw: map[string]any{"network": "tcp"}}); got == c.native {
		t.Fatal("invalid hysteria2 TCP transport unexpectedly selected NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "tuic", Port: 443, Raw: map[string]any{"network": "udp"}}); got != c.native {
		t.Fatal("tuic did not select NativeCore for strict validation")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "tuic", Port: 443, Raw: map[string]any{"network": "tcp"}}); got == c.native {
		t.Fatal("invalid tuic TCP transport unexpectedly selected NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "anytls", Port: 443, Raw: map[string]any{"network": "tcp"}}); got != c.native {
		t.Fatal("anytls did not select NativeCore for strict validation")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "anytls", Port: 443, Raw: map[string]any{"network": "udp"}}); got == c.native {
		t.Fatal("invalid anytls UDP outer transport unexpectedly selected NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "juicity", Port: 443, Raw: map[string]any{"network": "udp"}}); got != c.native {
		t.Fatal("juicity did not select NativeCore")
	}
	if got := c.pick(&core.InboundConfig{Protocol: "juicity", Port: 443, Raw: map[string]any{"network": "tcp"}}); got != c.native {
		t.Fatal("invalid juicity TCP outer transport must fail through NativeCore, not a compatibility core")
	}
}

func TestCompatibilityCoresStartLazily(t *testing.T) {
	c := New(slog.Default())
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.RLock()
	singStarted, xrayStarted := c.singStarted, c.xrayStarted
	c.mu.RUnlock()
	if singStarted || xrayStarted {
		t.Fatalf("compatibility cores started during native-only startup: sing=%v xray=%v", singStarted, xrayStarted)
	}
	if err := c.ensureStarted(c.xray); err != nil {
		t.Fatal(err)
	}
	c.mu.RLock()
	singStarted, xrayStarted = c.singStarted, c.xrayStarted
	c.mu.RUnlock()
	if singStarted || !xrayStarted {
		t.Fatalf("lazy xray startup state incorrect: sing=%v xray=%v", singStarted, xrayStarted)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBoundaryDoesNotFallBack(t *testing.T) {
	c := New(slog.Default())
	cases := []struct {
		name string
		cfg  *core.InboundConfig
		want string
	}{
		{name: "mieru-unknown", cfg: &core.InboundConfig{Tag: "mieru-unknown", Protocol: "mieru", Port: 443, Raw: map[string]any{"transport": "quic"}}, want: "mieru native transport"},
		{name: "juicity-tcp", cfg: &core.InboundConfig{Tag: "juicity-tcp", Protocol: "juicity", Port: 443, Raw: map[string]any{"network": "tcp"}}, want: "juicity native transport"},
		{name: "reality-h3-default-deny", cfg: &core.InboundConfig{Tag: "reality-h3", Protocol: "vless", Port: 443, Raw: map[string]any{"network": "xhttp-h3", "security": "reality"}}, want: "requires allow_experimental=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.AddInbound(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("boundary was not rejected explicitly: %v", err)
			}
		})
	}
}

func TestRealityH3RequiresExplicitExperimentalOptIn(t *testing.T) {
	cfg := &core.InboundConfig{Tag: "reality-h3-opt-in", Protocol: "vless", Port: 443, Raw: map[string]any{
		"network": "xhttp-h3", "security": "reality", "allow_experimental": true,
	}}
	if reason := nativeBoundaryReason(cfg); reason != "" {
		t.Fatalf("explicit experimental opt-in was rejected: %s", reason)
	}
}

func TestNativeOnlyRejectsUnverifiedMatrix(t *testing.T) {
	c := NewWithOptions(slog.Default(), Options{NativeOnly: true})
	if !c.NativeOnly() {
		t.Fatal("native-only option was not retained")
	}
	err := c.AddInbound(&core.InboundConfig{
		Tag:      "unknown-protocol",
		Protocol: "unknown",
		Port:     443,
	})
	if err == nil || !strings.Contains(err.Error(), "native-only mode") {
		t.Fatalf("native-only mode allowed an unverified matrix: %v", err)
	}
}

func TestDefaultCoreIsNativeOnly(t *testing.T) {
	c := New(slog.Default())
	if !c.NativeOnly() {
		t.Fatal("New must default to NativeCore-only mode")
	}
	if got := c.Type(); got != "pandora-native" {
		t.Fatalf("default runtime type = %q, want pandora-native", got)
	}
	if got := c.pick(&core.InboundConfig{Protocol: "vless", Port: 443, Raw: map[string]any{"network": "quic"}}); got != c.native {
		t.Fatal("default NativeCore-only mode must route an unverified combination to the native validator")
	}
	err := c.AddInbound(&core.InboundConfig{Tag: "unknown-default", Protocol: "unknown", Port: 443})
	if err == nil || !strings.Contains(err.Error(), "native-only mode") {
		t.Fatalf("default NativeCore-only mode allowed unknown protocol: %v", err)
	}
	for _, kernel := range []string{"xray-core", "sing-box"} {
		err := c.AddInbound(&core.InboundConfig{Tag: "explicit-" + kernel, Protocol: "vless", Kernel: kernel, Port: 443})
		if err == nil || !strings.Contains(err.Error(), "explicit compatibility kernel") {
			t.Fatalf("default NativeCore-only mode allowed explicit %s: %v", kernel, err)
		}
	}
}

func TestCompatibilityModeIsObservable(t *testing.T) {
	c := NewWithOptions(slog.Default(), Options{NativeOnly: false})
	if got := c.Type(); got != "pandora-native+compat" {
		t.Fatalf("compatibility runtime type = %q, want pandora-native+compat", got)
	}
}
