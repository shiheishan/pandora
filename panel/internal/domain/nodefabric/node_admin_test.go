package nodefabric

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestPoolMoveBumpsEffectiveReleaseGeneration(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	src := pkg.Decl("Service.PatchAdminNode")
	if strings.Contains(pkg.Source(), `配置发布身份升级完成前暂不允许移动节点分组`) ||
		!strings.Contains(src, `poolChanged := false`) ||
		!strings.Contains(src, `configSourceTouched := protocolChanged || poolChanged`) ||
		!strings.Contains(src, `config_source_generation=config_source_generation + CASE WHEN $15 THEN 1 ELSE 0 END`) {
		t.Fatal("PatchAdminNode does not rematerialize effective config after a pool move")
	}
}

// 协议字段只是原样带回（API 直调常见）时不推进代际：节点为每次推进重建入站、断开全部在线连接。
// 推进只看「内容真变了」的 protocolChanged，它必须比较全部生效输入（类型、地址、端口、内核、
// schema 版本、协议配置），少比一项就会漏推。
func TestPatchBumpsGenerationOnlyWhenProtocolChanges(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("Service.PatchAdminNode")
	for _, want := range []string{
		`nodeType != value(before.NodeType)`, `host != value(before.ServerHost)`,
		`port != intValue(before.ServerPort)`, `kernel != before.Kernel`,
		`version != before.ProtocolSchemaVersion`, `!sameProtocolJSON(raw, before.ProtocolConfig)`,
		`protocolChanged, configSourceTouched,`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("PatchAdminNode lost %q from the protocol change check", want)
		}
	}
}

func TestValidateNewNodeProtocolFailClosed(t *testing.T) {
	cases := map[string]json.RawMessage{
		"shadowsocks": json.RawMessage(`{"method":"aes-256-gcm"}`),
		// 不加密的 vless / vmess 只许挂 CDN 的传输，裸 tcp 明文被拒（w4proto）
		"vless": json.RawMessage(`{"network":"ws"}`),
		"vmess": json.RawMessage(`{"network":"ws"}`),
	}
	for nodeType, raw := range cases {
		t.Run(nodeType, func(t *testing.T) {
			version, err := validateNewNodeProtocol(nodeType, "auto", "edge.example", 443, raw)
			if err != nil || version != StableProtocolSchemaVersion {
				t.Fatalf("valid %s rejected: version=%d err=%v", nodeType, version, err)
			}
		})
	}
	if _, err := validateNewNodeProtocol("v2ray", "auto", "edge.example", 443, json.RawMessage(`{}`)); err == nil {
		t.Fatal("legacy v0 protocol accepted for new write")
	}
	if _, err := validateNewNodeProtocol("trojan", "pandora-native", "edge.example", 443, json.RawMessage(`{"tls":true,"cert_path":"/etc/pandora-native/certs/edge.example/fullchain.pem","key_path":"/etc/pandora-native/certs/edge.example/privkey.pem"}`)); err != nil {
		t.Fatalf("native trojan rejected for new write: %v", err)
	}
	if _, err := validateNewNodeProtocol("future-protocol", "auto", "edge.example", 443, json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown protocol accepted for new write")
	}
}

func TestValidateNewNodeProtocolRejectsInvalidHost(t *testing.T) {
	if _, err := validateNewNodeProtocol("shadowsocks", "auto", "https://edge.example.com/path", 443,
		json.RawMessage(`{"method":"aes-256-gcm"}`)); err == nil {
		t.Fatal("URL accepted where a host name was required")
	}
}

func TestStableProtocolServingGateMatchesSchemaCatalog(t *testing.T) {
	stableSchemas := map[string]bool{}
	for _, schema := range ProtocolSchemas() {
		if schema.Status == "stable" && schema.Version == StableProtocolSchemaVersion {
			stableSchemas[schema.NodeType] = true
		}
	}
	got := StableProtocolTypes()
	if len(got) != len(stableSchemas) {
		t.Fatalf("serving allowlist/schema catalog drift: allowlist=%v catalog=%v", got, stableSchemas)
	}
	for _, nodeType := range got {
		if !stableSchemas[nodeType] || !IsStableProtocolType(nodeType) {
			t.Fatalf("serving allowlist contains non-stable protocol %q", nodeType)
		}
	}
	if IsStableProtocolType("v2ray") || IsStableProtocolType("future-protocol") {
		t.Fatal("legacy or unknown protocol passed the stable protocol gate")
	}

	gate := StableProtocolReadySQL("n")
	for _, nodeType := range got {
		if !strings.Contains(gate, "'"+nodeType+"'") {
			t.Fatalf("SQL serving gate is missing %q: %s", nodeType, gate)
		}
	}
	for _, required := range []string{
		"n.protocol_schema_version = 1",
		"n.config_validated_at IS NOT NULL",
	} {
		if !strings.Contains(gate, required) {
			t.Fatalf("SQL serving gate is missing %q: %s", required, gate)
		}
	}
	if strings.Contains(gate, "protocol_schema_version = 0") {
		t.Fatalf("legacy v0 must remain read-only and never enter a serving gate: %s", gate)
	}

	copyOfTypes := StableProtocolTypes()
	copyOfTypes[0] = "mutated"
	if StableProtocolTypes()[0] == "mutated" {
		t.Fatal("StableProtocolTypes exposed mutable process-wide policy")
	}
}

func TestStableProtocolReadySQLRejectsDynamicAlias(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unexpected SQL alias did not fail closed")
		}
	}()
	_ = StableProtocolReadySQL("n; DROP TABLE nodes")
}

func TestValidServingTransition(t *testing.T) {
	if !ValidServingTransition("draft", "active") || !ValidServingTransition("active", "draining") {
		t.Fatal("expected lifecycle edge missing")
	}
	if ValidServingTransition("active", "retired") || ValidServingTransition("retired", "active") {
		t.Fatal("unsafe direct retirement/reactivation edge accepted")
	}
}

func TestValidateAdminNodeNameUsesRunes(t *testing.T) {
	if err := validateAdminNodeName("香港节点"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if err := validateAdminNodeName(""); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestOptionalNullableString(t *testing.T) {
	var in PatchAdminNodeInput
	if err := json.Unmarshal([]byte(`{"row_version":1}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.PoolID.Set {
		t.Fatal("omitted pool_id must keep the current value")
	}
	if err := json.Unmarshal([]byte(`{"row_version":1,"pool_id":null}`), &in); err != nil {
		t.Fatal(err)
	}
	if !in.PoolID.Set || in.PoolID.Value != nil {
		t.Fatal("null pool_id must explicitly clear")
	}
	if err := json.Unmarshal([]byte(`{"row_version":1,"pool_id":""}`), &in); err != nil {
		t.Fatal(err)
	}
	if !in.PoolID.Set || in.PoolID.Value == nil || *in.PoolID.Value != "" {
		t.Fatal("empty pool_id must explicitly clear")
	}
}

func TestLegacyNodeStatusGateSeparatesControlAndLogicalNodes(t *testing.T) {
	if legacyNodeStatusAllowsServing(true, "draft") {
		t.Fatal("draft control Node must not authenticate or receive config")
	}
	if !legacyNodeStatusAllowsServing(false, "draft") {
		t.Fatal("logical Node must be governed by serving_status, not legacy Agent status")
	}
	if !legacyNodeStatusAllowsServing(true, "active") {
		t.Fatal("active control Node should pass the legacy status gate")
	}
}

func TestNormalizeCountryCode(t *testing.T) {
	for raw, want := range map[string]string{"": "", "  ": "", "jp": "JP", " Us ": "US", "HK": "HK"} {
		got, err := normalizeCountryCode(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %q err=%v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"J", "JPN", "J1", "日本", "é1"} {
		_, err := normalizeCountryCode(raw)
		var httpErr *httpx.Error
		if !errors.As(err, &httpErr) || httpErr.Fields["country_code"] == "" {
			t.Errorf("%q: want 422 fields.country_code, got %v", raw, err)
		}
	}
}
