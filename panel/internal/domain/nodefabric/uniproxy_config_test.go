package nodefabric

import (
	"encoding/json"
	"testing"
)

func TestBuildNodeConfigMatchesQNodeContract(t *testing.T) {
	svc := &Service{}
	body, _, err := svc.BuildNodeConfig(&ServingNode{
		NodeType:   "VLESS",
		ServerPort: 443,
		Kernel:     "sing-box",
		Protocol:   json.RawMessage(`{"network":"tcp","tls":false}`),
	})
	if err != nil {
		t.Fatalf("BuildNodeConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got["protocol"] != "vless" {
		t.Fatalf("protocol = %#v, want vless", got["protocol"])
	}
	if got["kernel_type"] != "singbox" {
		t.Fatalf("kernel_type = %#v, want singbox", got["kernel_type"])
	}
	if got["kernel"] != "sing-box" {
		t.Fatalf("legacy kernel = %#v, want sing-box", got["kernel"])
	}
}

func TestBuildNodeConfigReservedContractFieldsCannotBeOverridden(t *testing.T) {
	svc := &Service{}
	body, _, err := svc.BuildNodeConfig(&ServingNode{
		NodeType:   "shadowsocks",
		ServerPort: 8388,
		Kernel:     "xray-core",
		Protocol: json.RawMessage(`{
			"protocol":"trojan",
			"server_port":1,
			"kernel":"sing-box",
			"kernel_type":"singbox",
			"method":"aes-128-gcm"
		}`),
	})
	if err != nil {
		t.Fatalf("BuildNodeConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got["protocol"] != "shadowsocks" || got["server_port"] != float64(8388) {
		t.Fatalf("reserved node identity was overridden: %#v", got)
	}
	if got["kernel"] != "xray-core" || got["kernel_type"] != "xray" {
		t.Fatalf("reserved kernel selection was overridden: %#v", got)
	}
	if got["method"] != "aes-128-gcm" {
		t.Fatalf("protocol-specific field missing: %#v", got)
	}
	if got["cipher"] != "aes-128-gcm" {
		t.Fatalf("QNode Shadowsocks alias missing: %#v", got)
	}
}

func TestBuildNodeConfigRejectsConflictingShadowsocksAlias(t *testing.T) {
	svc := &Service{}
	_, _, err := svc.BuildNodeConfig(&ServingNode{
		Name:       "edge-1",
		NodeType:   "shadowsocks",
		ServerPort: 8388,
		Protocol:   json.RawMessage(`{"method":"aes-128-gcm","cipher":"aes-256-gcm"}`),
	})
	if err == nil {
		t.Fatal("conflicting method/cipher aliases were accepted")
	}
}

func TestBuildNodeConfigAutoKernelDefersToAgent(t *testing.T) {
	svc := &Service{}
	body, _, err := svc.BuildNodeConfig(&ServingNode{
		NodeType:   "shadowsocks",
		ServerPort: 8388,
		Kernel:     "auto",
		Protocol:   json.RawMessage(`{"method":"aes-128-gcm"}`),
	})
	if err != nil {
		t.Fatalf("BuildNodeConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if _, exists := got["kernel_type"]; exists {
		t.Fatalf("auto must defer to the agent's configured kernel: %#v", got)
	}
}

func TestBuildNodeConfigPreservesPandoraNativeKernel(t *testing.T) {
	svc := &Service{}
	body, _, err := svc.BuildNodeConfig(&ServingNode{
		NodeType: "vless", ServerPort: 443, Kernel: "pandora-native",
		Protocol: json.RawMessage(`{"network":"tcp","tls":false}`),
	})
	if err != nil {
		t.Fatalf("BuildNodeConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got["kernel"] != "pandora-native" {
		t.Fatalf("kernel = %#v, want pandora-native", got["kernel"])
	}
	if _, exists := got["kernel_type"]; exists {
		t.Fatalf("NativeCore must not receive a legacy kernel_type: %#v", got)
	}
}

func TestBuildNodeConfigTranslatesRoutingForQNode(t *testing.T) {
	svc := &Service{}
	body, _, err := svc.BuildNodeConfig(&ServingNode{
		Name:       "edge-1",
		NodeType:   "shadowsocks",
		ServerPort: 8388,
		Kernel:     "auto",
		Protocol:   json.RawMessage(`{"method":"aes-128-gcm"}`),
		Outbounds: []NodeOutbound{
			{Tag: "direct", Type: "direct", Settings: json.RawMessage(`{}`)},
			{Tag: "exit", Type: "socks", Settings: json.RawMessage(`{"server":"203.0.113.8","server_port":1080}`)},
		},
		Routes: []NodeRoute{
			{Matcher: json.RawMessage(`{"domain_suffix":["example.com"],"port":[443]}`), OutboundTag: "exit"},
			{Matcher: json.RawMessage(`{}`), OutboundTag: "direct"},
		},
	})
	if err != nil {
		t.Fatalf("BuildNodeConfig: %v", err)
	}
	var got struct {
		CustomOutbounds []qnodeOutbound  `json:"custom_outbounds"`
		CustomRoutes    []qnodeRouteRule `json:"custom_route_rules"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(got.CustomOutbounds) != 1 || got.CustomOutbounds[0].Protocol != "socks" {
		t.Fatalf("unexpected QNode outbounds: %#v", got.CustomOutbounds)
	}
	if len(got.CustomRoutes) != 2 || got.CustomRoutes[0].Action.Target != "exit" {
		t.Fatalf("unexpected QNode routes: %#v", got.CustomRoutes)
	}
	if got.CustomRoutes[0].Match.Ports[0] != "443" {
		t.Fatalf("numeric port was not normalized: %#v", got.CustomRoutes[0].Match)
	}
	if got.CustomRoutes[1].Action.Type != "direct" {
		t.Fatalf("fallback action was not preserved: %#v", got.CustomRoutes[1])
	}
}

func TestBuildNodeConfigRejectsUntranslatableRouting(t *testing.T) {
	svc := &Service{}
	_, _, err := svc.BuildNodeConfig(&ServingNode{
		Name:       "edge-1",
		NodeType:   "shadowsocks",
		ServerPort: 8388,
		Protocol:   json.RawMessage(`{"method":"aes-128-gcm"}`),
		Routes: []NodeRoute{{
			Matcher: json.RawMessage(`{"geoip":["cn"]}`), OutboundTag: "direct",
		}},
	})
	if err == nil {
		t.Fatal("unsupported matcher silently reached the agent")
	}
}

func TestValidateRoutingMatcherDetectsCatchAll(t *testing.T) {
	empty, err := ValidateRoutingMatcher(json.RawMessage(`{}`))
	if err != nil || !empty {
		t.Fatalf("empty matcher: empty=%v err=%v", empty, err)
	}
	empty, err = ValidateRoutingMatcher(json.RawMessage(`{"network":["tcp","udp"]}`))
	if err != nil || empty {
		t.Fatalf("network matcher: empty=%v err=%v", empty, err)
	}
}
