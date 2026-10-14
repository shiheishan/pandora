package render

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

var fixtureECHList = []byte{0x00, 0x04, 0xfe, 0x0d, 0x00, 0x00}

const fixtureECHB64 = "AAT+DQAA"

func trojanTLSNode() Node {
	return Node{
		Name: "trojan-ech", Type: "trojan", Host: fixtureHost, Port: 7443,
		Config: map[string]any{
			"network": "tcp", "security": "none", "tls": true,
			"cert_path":   "/etc/pandora-native/certs/example.com/fullchain.pem",
			"key_path":    "/etc/pandora-native/certs/example.com/privkey.pem",
			"server_name": "node.example.com",
		},
	}
}

func withECH(n Node) Node {
	n.ECHConfigList = append([]byte(nil), fixtureECHList...)
	return n
}

func TestECH_TrojanTLS_MetaPremiumStashSingbox(t *testing.T) {
	n := withECH(trojanTLSNode())
	before := append([]byte(nil), n.ECHConfigList...)

	meta, reason := nodeToClash(n, fixtureUUID, false)
	if reason != "" || meta == nil {
		t.Fatalf("meta clash: %s", reason)
	}
	ech, ok := meta["ech-opts"].(map[string]any)
	if !ok || ech["enable"] != true || ech["config"] != fixtureECHB64 {
		t.Fatalf("meta ech-opts = %v, want enable+config %s", meta["ech-opts"], fixtureECHB64)
	}

	premium, _ := nodeToClash(n, fixtureUUID, true)
	if premium == nil {
		t.Fatal("premium clash nil")
	}
	if _, has := premium["ech-opts"]; has {
		t.Fatalf("premium must not have ech-opts: %v", premium)
	}

	stash, reason := nodeToStash(n, fixtureUUID)
	if reason != "" || stash == nil {
		t.Fatalf("stash: %s", reason)
	}
	if _, has := stash["ech-opts"]; has {
		t.Fatalf("stash must not have ech-opts: %v", stash)
	}

	sb, reason := nodeToSingbox(n, fixtureUUID)
	if reason != "" || sb == nil {
		t.Fatalf("sing-box: %s", reason)
	}
	tls, _ := sb["tls"].(map[string]any)
	echSB, _ := tls["ech"].(map[string]any)
	cfg, _ := echSB["config"].([]string)
	if len(cfg) < 2 || cfg[0] != "-----BEGIN ECH CONFIGS-----" || cfg[len(cfg)-1] != "-----END ECH CONFIGS-----" {
		t.Fatalf("sing-box tls.ech.config = %v", cfg)
	}

	if !bytes.Equal(n.ECHConfigList, before) {
		t.Fatal("ECHConfigList mutated after render")
	}
}

func TestECH_RealityVlessSkipped(t *testing.T) {
	n := withECH(nodeByName(t, "vless-reality-vision"))

	meta, _ := nodeToClash(n, fixtureUUID, false)
	if meta != nil {
		if _, has := meta["ech-opts"]; has {
			t.Fatalf("REALITY meta must not have ech-opts: %v", meta)
		}
	}

	sb, _ := nodeToSingbox(n, fixtureUUID)
	if sb != nil {
		raw, _ := json.Marshal(sb)
		if strings.Contains(string(raw), `"ech"`) {
			t.Fatalf("REALITY sing-box must not have ech: %s", raw)
		}
	}
}

func TestECH_Hysteria2Skipped(t *testing.T) {
	n := withECH(nodeByName(t, "hy2-obfs"))

	meta, _ := nodeToClash(n, fixtureUUID, false)
	if meta != nil {
		if _, has := meta["ech-opts"]; has {
			t.Fatalf("hysteria2 meta must not have ech-opts")
		}
	}

	sb, _ := nodeToSingbox(n, fixtureUUID)
	if sb != nil {
		tls, _ := sb["tls"].(map[string]any)
		if tls != nil {
			if _, has := tls["ech"]; has {
				t.Fatalf("hysteria2 sing-box must not have tls.ech")
			}
		}
	}
}

func TestECH_AnyTLS_MetaAndSingbox(t *testing.T) {
	n := withECH(nodeByName(t, "anytls"))

	meta, _ := nodeToClash(n, fixtureUUID, false)
	if meta == nil {
		t.Fatal("anytls meta nil")
	}
	ech, _ := meta["ech-opts"].(map[string]any)
	if ech["config"] != fixtureECHB64 {
		t.Fatalf("anytls meta ech config = %v", ech["config"])
	}

	sb, _ := nodeToSingbox(n, fixtureUUID)
	tls, _ := sb["tls"].(map[string]any)
	echSB, _ := tls["ech"].(map[string]any)
	if echSB["enabled"] != true {
		t.Fatalf("anytls sing-box ech = %v", echSB)
	}
}

func TestECH_AbsentLeavesFormatsUnchanged(t *testing.T) {
	n := trojanTLSNode()
	if len(n.ECHConfigList) != 0 {
		t.Fatal("trojan fixture must not carry ECH")
	}

	meta, _ := nodeToClash(n, fixtureUUID, false)
	if _, has := meta["ech-opts"]; has {
		t.Fatal("without ECH, meta must not have ech-opts")
	}
	sb, _ := nodeToSingbox(n, fixtureUUID)
	raw, _ := json.Marshal(sb)
	if strings.Contains(string(raw), `"ech"`) {
		t.Fatal("without ECH, sing-box must not have ech")
	}

	// 与带 ECH 的节点对比：仅多出 ech 相关字段
	with := withECH(trojanTLSNode())
	metaECH, _ := nodeToClash(with, fixtureUUID, false)
	delete(metaECH, "ech-opts")
	if !mapsEqual(meta, metaECH) {
		t.Fatalf("meta without ECH should match meta with ECH minus ech-opts\nno: %v\nwith: %v", meta, metaECH)
	}
}

func TestECH_DoesNotMutateSharedList(t *testing.T) {
	shared := []byte{0x00, 0x04, 0xfe, 0x0d, 0x00, 0x00}
	snapshot := append([]byte(nil), shared...)
	n := trojanTLSNode()
	n.ECHConfigList = shared

	_, _ = nodeToClash(n, fixtureUUID, false)
	_, _ = nodeToStash(n, fixtureUUID)
	_, _ = nodeToSingbox(n, fixtureUUID)
	_, _ = Render(FormatClash, []Node{n}, fixtureUUID, "", Options{})

	if !bytes.Equal(shared, snapshot) {
		t.Fatalf("shared ECHConfigList changed: %v vs %v", shared, snapshot)
	}
	if base64.StdEncoding.EncodeToString(shared) != fixtureECHB64 {
		t.Fatalf("unexpected encoding %s", base64.StdEncoding.EncodeToString(shared))
	}
}

func mapsEqual(a, b map[string]any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}
