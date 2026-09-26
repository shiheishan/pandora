// [INPUT]: 依赖 node_admin.go 与 protocol_schema.go / protocol_validate*.go 的校验函数，依赖 platform/sourcetest 按名取 Service.PatchAdminNode 与整包源码
// [OUTPUT]: 对外提供 TestPoolMoveBumpsEffectiveReleaseGeneration、TestValidateNewNodeProtocolFailClosed、TestValidateNewNodeProtocolRejectsInvalidHost、TestStableProtocolServingGateMatchesSchemaCatalog、TestStableProtocolReadySQLRejectsDynamicAlias、TestValidServingTransition、TestValidateAdminNodeNameUsesRunes、TestOptionalNullableString、TestLegacyNodeStatusGateSeparatesControlAndLogicalNodes、TestNormalizeCountryCode
// [POS]: nodefabric 后台节点编辑的单元与源码契约：换池重物化有效配置、新写入协议 fail closed、服务状态迁移与名称、国家码规范化
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
		!strings.Contains(src, `configSourceTouched := protocolTouched || poolChanged`) ||
		!strings.Contains(src, `config_source_generation=config_source_generation + CASE WHEN $15 THEN 1 ELSE 0 END`) {
		t.Fatal("PatchAdminNode does not rematerialize effective config after a pool move")
	}
}

func TestValidateNewNodeProtocolFailClosed(t *testing.T) {
	cases := map[string]json.RawMessage{
		"shadowsocks": json.RawMessage(`{"method":"aes-256-gcm"}`),
		"vless":       json.RawMessage(`{}`),
		"vmess":       json.RawMessage(`{}`),
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
	if _, err := validateNewNodeProtocol("trojan", "pandora-native", "edge.example", 443, json.RawMessage(`{"tls":true,"cert_path":"cert","key_path":"key"}`)); err != nil {
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
