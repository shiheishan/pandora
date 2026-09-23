package kernel

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNativeCapabilitiesAreDeterministicAndCopySafe(t *testing.T) {
	first := NativeCapabilities()
	second := NativeCapabilities()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("native capability matrix is not deterministic")
	}
	if len(first) == 0 {
		t.Fatal("native capability matrix is empty")
	}
	first[0].Networks[0] = "mutated"
	if NativeCapabilities()[0].Networks[0] == "mutated" {
		t.Fatal("native capability matrix leaked mutable slice")
	}
}

func TestNativeCapabilityAliasesAndFeatures(t *testing.T) {
	ss, ok := NativeCapabilityFor(" SS ")
	if !ok || ss.Protocol != "shadowsocks" {
		t.Fatalf("ss alias lookup = %#v, %v", ss, ok)
	}
	vless, ok := NativeCapabilityFor("vless")
	if !ok {
		t.Fatal("vless capability missing")
	}
	foundRealityH3 := false
	for _, feature := range vless.Features {
		if feature == "reality-h3-experimental" {
			foundRealityH3 = true
		}
	}
	if !foundRealityH3 {
		t.Fatal("vless native REALITY H3 feature is not published")
	}
	if vless.Status != "stable-with-experimental-features" || !hasCapabilityBoundary(vless, "reality-xhttp-h3-requires-explicit-opt-in") {
		t.Fatalf("vless experimental lifecycle is not explicit: %#v", vless)
	}
	foundUDP := false
	for _, feature := range vless.Features {
		if feature == "udp-forward" {
			foundUDP = true
		}
	}
	if !foundUDP {
		t.Fatal("vless native UDP forwarding feature is not published")
	}
	foundGRPCGzip := false
	for _, feature := range vless.Features {
		if feature == "grpc-gzip" {
			foundGRPCGzip = true
		}
	}
	if !foundGRPCGzip {
		t.Fatal("vless native gRPC gzip feature is not published")
	}
	if !hasCapabilityBoundary(vless, "external-reality-xhttp-h3-unverified") {
		t.Fatal("vless external REALITY XHTTP H3 gate must remain explicit")
	}
	if hasCapabilityBoundary(vless, "vision-external-client-unverified") {
		t.Fatal("verified Mihomo Vision interoperability is still reported as unverified")
	}
	if _, ok := NativeCapabilityFor("unknown"); ok {
		t.Fatal("unknown protocol unexpectedly reported as native")
	}
	trojan, ok := NativeCapabilityFor("trojan")
	if !ok {
		t.Fatal("trojan capability missing")
	}
	foundTrojanUDP := false
	for _, feature := range trojan.Features {
		if feature == "udp-forward" {
			foundTrojanUDP = true
		}
	}
	if !foundTrojanUDP {
		t.Fatal("trojan native UDP forwarding feature is not published")
	}
	socks, ok := NativeCapabilityFor("socks")
	if !ok || !hasCapabilityFeature(socks, "socks4") || !hasCapabilityFeature(socks, "socks4a") || !hasCapabilityFeature(socks, "socks5") {
		t.Fatalf("socks capability missing native protocol variants: %#v", socks)
	}
	httpProxy, ok := NativeCapabilityFor("http")
	if !ok || !hasCapabilityFeature(httpProxy, "http-connect") || !hasCapabilityFeature(httpProxy, "http-forward") {
		t.Fatalf("http proxy capability missing forward features: %#v", httpProxy)
	}
}

func hasCapabilityFeature(capability Capability, want string) bool {
	for _, feature := range capability.Features {
		if feature == want {
			return true
		}
	}
	return false
}

func hasCapabilityBoundary(capability Capability, want string) bool {
	for _, boundary := range capability.Boundaries {
		if boundary == want {
			return true
		}
	}
	return false
}

func TestNativeCapabilityReportJSON(t *testing.T) {
	report := NativeCapabilityReportFor(true)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded NativeCapabilityReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kernel != "pandora-native" || !decoded.NativeOnly || len(decoded.Capabilities) == 0 {
		t.Fatalf("unexpected capability report: %#v", decoded)
	}
	for _, capability := range decoded.Capabilities {
		if capability.Status == "" {
			t.Fatalf("capability %q has no lifecycle status", capability.Protocol)
		}
	}
}

func TestNativeCoreCapabilityReportIsAvailableBeforeStart(t *testing.T) {
	core := NewNativeCore(nil)
	report := core.CapabilityReport()
	if report.Kernel != core.Type() || !report.NativeOnly || len(report.Capabilities) != 13 {
		t.Fatalf("unexpected native core report: %#v", report)
	}
}

func TestDefaultAdapterRegistryCoversNativeCapabilityMatrix(t *testing.T) {
	registry := NewDefaultAdapterRegistry()
	types := make(map[string]bool)
	for _, protocol := range registry.Types() {
		types[protocol] = true
	}
	for _, protocol := range NativeProtocolNames() {
		if !types[protocol] {
			t.Fatalf("default adapter registry is missing advertised native protocol %q", protocol)
		}
	}
	if !types["ss"] {
		t.Fatal("default adapter registry is missing the stable shadowsocks alias ss")
	}
}
