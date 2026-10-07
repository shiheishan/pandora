package bindingcontract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// 金样本只在面板这边生成：
//
//	go test ./internal/platform/bindingcontract -run TestGoldenFileMatchesDefinitions -bindingcontract.update
//
// 同时写面板与 pdnd 两份。Ed25519 签名是确定性的，同样的输入总得到同样的文件，
// 所以不带 -update 时本测试要求磁盘上的文件与下面的定义重新生成的结果逐字节相同。
var updateGolden = flag.Bool("bindingcontract.update", false, "rewrite the bindingcontract golden vectors in panel and pdnd")

// 全部是虚构夹具。
const (
	gTenant    = "0193f0b0-1111-7000-8000-0000000000a1"
	gTenant2   = "0193f0b0-1111-7000-8000-0000000000a2"
	gServer    = "0193f0b0-2222-7000-8000-0000000000b1"
	gNodeA     = "0193f0b0-3333-7000-8000-0000000000c1"
	gNodeB     = "0193f0b0-3333-7000-8000-0000000000c2"
	gPool      = "0193f0b0-4444-7000-8000-0000000000d1"
	gOrder     = "0193f0b0-5555-7000-8000-0000000000e1"
	gReport    = "0193f0b0-6666-7000-8000-0000000000f1"
	gIssued    = "2026-10-07T08:00:00.123456Z"
	gExpires   = "2026-10-07T08:10:00.123456Z"
	gTooLate   = "2026-10-07T08:11:00.123456Z"
	gNanoTime  = "2026-10-07T08:00:00.123456789Z"
	gRequestTS = "2026-10-07T08:00:00Z"
	gCommit    = "0123456789abcdef0123456789abcdef01234567"
	gReleased  = "2026-10-01T00:00:00Z"
)

// nonceOf 把一个字节铺满 16 字节，得到确定性的 nonce。
func nonceOf(b byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 16))
}

func shaB64(s string) string { return BodySHA256([]byte(s)) }

func shaHex(s string) string {
	sum := labelSum(s)
	return hex.EncodeToString(sum)
}

func defaultIntervals() manifestIntervalsVector {
	return manifestIntervalsVector{ManifestPullSeconds: 15, UsersPullSeconds: 15, ReportSeconds: 60, StreamOnlinePullSeconds: 60}
}

func goldenDefinitions(k testKeys) goldenFile {
	panelKeyID := KeyID(pub(k.panelConfig))
	panelPin, _ := PanelKeyFingerprint(pub(k.panelConfig))
	reportBody := `{"agent_version":"v1.5.0","applied_manifest_generation":17,"nodes":[]}`
	payload := `{"users":[{"id":1,"uuid":"0193f0b0-7777-4000-8000-000000000001","speed_limit":0,"device_limit":0},` +
		`{"id":2,"uuid":"0193f0b0-7777-4000-8000-000000000002","speed_limit":100,"device_limit":3}]}`

	req := func(name string, valid bool, method, target string, mut func(*requestVector)) requestVector {
		v := requestVector{vectorResult: vectorResult{Name: name, Valid: valid}, Method: method, Target: target,
			TenantID: gTenant, ServerID: gServer, Serial: 1, Timestamp: gRequestTS, Nonce: nonceOf(0x11),
			BodySHA256: shaB64("")}
		if mut != nil {
			mut(&v)
		}
		return v
	}
	nodes := []manifestNodeVector{
		{NodeID: gNodeA, Protocol: "vless", Port: 443, L4: "tcp", PoolID: gPool, EffectiveGeneration: 7,
			ContentSHA256: shaB64("node a release payload")},
		{NodeID: gNodeB, Protocol: "hysteria2", Port: 8443, L4: "udp", PoolID: "", EffectiveGeneration: 3,
			ContentSHA256: shaB64("node b release payload")},
	}
	man := func(name string, valid bool, mut func(*manifestVector)) manifestVector {
		v := manifestVector{vectorResult: vectorResult{Name: name, Valid: valid}, TenantID: gTenant, ServerID: gServer,
			Serial: 1, ManifestGeneration: 17, State: ManifestStateBound, Features: []string{"report-v1", "users-v1"},
			MinAgentVersion: "v1.5.0", TLSPinNext: TLSPin([]byte("next gateway spki for golden vectors")),
			Intervals: defaultIntervals(), Nodes: nodes, RequestNonce: nonceOf(0x22), KeyID: panelKeyID,
			IssuedAt: gIssued, ExpiresAt: gExpires}
		if mut != nil {
			mut(&v)
		}
		return v
	}
	users := func(name string, valid bool, mut func(*usersVector)) usersVector {
		v := usersVector{vectorResult: vectorResult{Name: name, Valid: valid}, TenantID: gTenant, ServerID: gServer,
			Serial: 1, PoolID: gPool, UsersVersion: 42, Kind: UsersKindFull, UserCount: 2, Payload: payload,
			ContentSHA256: shaB64(payload), RequestNonce: nonceOf(0x33), KeyID: panelKeyID,
			IssuedAt: gIssued, ExpiresAt: gExpires}
		if mut != nil {
			mut(&v)
		}
		return v
	}
	artifacts := []releaseArtifactVector{
		{Name: "pandora-native-linux-amd64", Size: 23068672, SHA256: shaHex("fictional amd64 binary")},
		{Name: "pandora-native-linux-arm64", Size: 21495808, SHA256: shaHex("fictional arm64 binary")},
	}
	rel := func(name string, valid bool, mut func(*releaseVector)) releaseVector {
		v := releaseVector{vectorResult: vectorResult{Name: name, Valid: valid}, Product: ReleaseProduct,
			Version: "v1.6.0", Commit: gCommit, ReleasedAt: gReleased, ReleaseKeyID: KeyID(pub(k.release)),
			Artifacts: artifacts}
		if mut != nil {
			mut(&v)
		}
		return v
	}
	order := func(name string, valid bool, mut func(*upgradeOrderVector)) upgradeOrderVector {
		v := upgradeOrderVector{vectorResult: vectorResult{Name: name, Valid: valid}, TenantID: gTenant,
			ServerID: gServer, Serial: 1, OrderID: gOrder, TargetVersion: "v1.6.0", RequestNonce: nonceOf(0x22),
			KeyID: panelKeyID, IssuedAt: gIssued, ExpiresAt: gExpires}
		if mut != nil {
			mut(&v)
		}
		return v
	}
	poss := func(name string, valid bool, mut func(*possessionVector)) possessionVector {
		v := possessionVector{vectorResult: vectorResult{Name: name, Valid: valid}, TenantID: gTenant, NodeID: gNodeA,
			RequestNonce: nonceOf(0x44), ServerPublicKey: b64(pub(k.server)),
			ServerEncPublicKey: b64(k.serverEnc.PublicKey().Bytes())}
		if mut != nil {
			mut(&v)
		}
		return v
	}

	return goldenFile{
		Contracts: map[string]string{
			"server_request": ServerRequestContract, "manifest": ManifestContract, "users": UsersContract,
			"upgrade_order": UpgradeOrderContract, "release_manifest": ReleaseManifestContract,
			"key_possession": KeyPossessionContract,
		},
		Note: "Fictional fixtures for docs/server-binding-contract.md. Every key here is a TEST-ONLY key derived as " +
			"sha256(label) from the labels below; never use them in production. preimage_hex is the exact signed " +
			"byte string; signature is standard base64 of the Ed25519 signature over it.",
		ServerRequests: []requestVector{
			req("report with report id", true, "POST", "/v1/servers/report", func(v *requestVector) {
				v.ReportID, v.Body, v.BodySHA256 = gReport, reportBody, shaB64(reportBody)
			}),
			req("users of one pool", true, "GET", "/v1/servers/users?pool="+gPool, nil),
			req("query keys out of order", false, "GET", "/v1/servers/users?pool="+gPool+"&after=1", nil),
			req("dot-dot segment", false, "GET", "/v1/servers/../nodes/config", nil),
			req("percent-encoded path", false, "GET", "/v1/servers/users%2F..", nil),
			req("not a server path", false, "POST", "/v1/nodes/heartbeat", nil),
			req("offset timestamp", false, "GET", "/v1/servers/manifest", func(v *requestVector) {
				v.Timestamp = "2026-10-07T16:00:00+08:00"
			}),
			req("uppercase server id", false, "GET", "/v1/servers/manifest", func(v *requestVector) {
				v.ServerID = "0193F0B0-2222-7000-8000-0000000000B1"
			}),
			req("lowercase method", false, "get", "/v1/servers/manifest", nil),
		},
		Manifests: []manifestVector{
			man("bound with two nodes", true, nil),
			man("unbound tombstone", true, func(v *manifestVector) {
				v.ManifestGeneration, v.State, v.UnboundReason, v.Nodes = 18, ManifestStateUnbound, "server_unbound", nil
				v.MinAgentVersion, v.TLSPinNext, v.Features = "", "", nil
			}),
			man("delivery window over ten minutes", false, func(v *manifestVector) { v.ExpiresAt = gTooLate }),
			man("nanosecond timestamp", false, func(v *manifestVector) { v.IssuedAt = gNanoTime }),
			man("features out of order", false, func(v *manifestVector) { v.Features = []string{"users-v1", "report-v1"} }),
			man("duplicate node id", false, func(v *manifestVector) {
				v.Nodes = []manifestNodeVector{nodes[0], nodes[0]}
			}),
			man("unbound with nodes", false, func(v *manifestVector) {
				v.State, v.UnboundReason = ManifestStateUnbound, "server_deleted"
			}),
			man("unknown l4", false, func(v *manifestVector) {
				n := nodes[0]
				n.L4 = "tcpudp"
				v.Nodes = []manifestNodeVector{n}
			}),
			man("prerelease min version", false, func(v *manifestVector) { v.MinAgentVersion = "v1.5.0-rc1" }),
		},
		Users: []usersVector{
			users("full list of one pool", true, nil),
			users("delta kind is not in v1", false, func(v *usersVector) { v.Kind = "delta" }),
			users("missing pool", false, func(v *usersVector) { v.PoolID = "" }),
		},
		ReleaseManifests: []releaseVector{
			rel("two architectures", true, nil),
			rel("artifacts out of order", false, func(v *releaseVector) {
				v.Artifacts = []releaseArtifactVector{artifacts[1], artifacts[0]}
			}),
			rel("git describe version", false, func(v *releaseVector) { v.Version = "v1.6.0-3-gabcdef0" }),
			rel("foreign product", false, func(v *releaseVector) { v.Product = "something-else" }),
			rel("uppercase hash", false, func(v *releaseVector) {
				a := artifacts[0]
				a.SHA256 = "ABCDEF" + a.SHA256[6:]
				v.Artifacts = []releaseArtifactVector{a}
			}),
		},
		UpgradeOrders: []upgradeOrderVector{
			order("upgrade to v1.6.0", true, nil),
			order("prerelease target", false, func(v *upgradeOrderVector) { v.TargetVersion = "v1.6.0-rc1" }),
			order("window over ten minutes", false, func(v *upgradeOrderVector) { v.ExpiresAt = gTooLate }),
		},
		KeyPossessions: []possessionVector{
			poss("upgrade to server", true, nil),
			poss("short public key", false, func(v *possessionVector) { v.ServerPublicKey = b64([]byte("short")) }),
		},
		PanelOrigins: []originVector{
			{Input: "https://panel.example.com", Valid: true, Canonical: "https://panel.example.com:443"},
			{Input: "HTTPS://Panel.Example.COM:8443/", Valid: true, Canonical: "https://panel.example.com:8443"},
			{Input: "https://[2001:DB8::1]:8443", Valid: true, Canonical: "https://[2001:db8::1]:8443"},
			{Input: "https://[::ffff:127.0.0.1]", Valid: true, Canonical: "https://127.0.0.1:443"},
			{Input: "http://127.0.0.1:8080", Valid: true, Canonical: "http://127.0.0.1:8080"},
			{Input: "http://localhost", Valid: true, Canonical: "http://localhost:80"},
			{Input: "http://[::1]:8080", Valid: true, Canonical: "http://[::1]:8080"},
			{Input: "http://panel.example.com", Valid: false},
			{Input: "https://user@panel.example.com", Valid: false},
			{Input: "https://panel.example.com/admin", Valid: false},
			{Input: "https://panel.example.com?x=1", Valid: false},
			{Input: "https://panel.example.com#frag", Valid: false},
			{Input: "https://panel.example.com:70000", Valid: false},
			{Input: "https://panel.example.com.", Valid: false},
			{Input: "https://[fe80::1%25eth0]:443", Valid: false},
			{Input: "ftp://panel.example.com", Valid: false},
		},
		AgentVersions: []versionVector{
			{A: "v1.5.0", B: "v1.10.0", Valid: true, Compare: -1},
			{A: "v2.0.0", B: "v1.99.99", Valid: true, Compare: 1},
			{A: "v1.5.0", B: "v1.5.0", Valid: true, Compare: 0},
			{A: "v0.0.1", B: "v0.0.0", Valid: true, Compare: 1},
			{A: "1.5.0", B: "v1.5.0", Valid: false},
			{A: "v1.05.0", B: "v1.5.0", Valid: false},
			{A: "v1.5.0-rc1", B: "v1.5.0", Valid: false},
			{A: "dev", B: "v1.5.0", Valid: false},
			{A: "v1.5", B: "v1.5.0", Valid: false},
			{A: "v1.5.0", B: "v4294967296.0.0", Valid: false},
		},
		SamePanelTenant: []panelTenantVector{
			{A: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant},
				B: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant}, Same: true},
			{A: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant},
				B: PanelTenantJSON{"https://panel.example.com:8443", panelPin, gTenant}, Same: true},
			{A: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant},
				B: PanelTenantJSON{"https://panel.example.com:443", "sha256:" + shaHex("another panel"), gTenant}, Same: true},
			{A: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant},
				B: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant2}, Same: false},
			{A: PanelTenantJSON{"https://panel.example.com:443", panelPin, gTenant},
				B: PanelTenantJSON{"https://other.example.com:443", "sha256:" + shaHex("another panel"), gTenant}, Same: false},
		},
		Enums: goldenEnums{FailureCodes: FailureCodes, WarningOnlyCodes: WarningOnlyCodes, RuntimeStates: RuntimeStates,
			ReceiptResults: ReceiptResults, UpgradeStates: UpgradeStates, UpgradeFailureCodes: UpgradeFailureCodes,
			EventTypes: EventTypes, UnboundReasons: UnboundReasons},
	}
}

// sealVector 给合法向量填上原像与签名；非法向量必须被原像函数拒绝。
func sealVector(t *testing.T, v *vectorResult, signer ed25519.PrivateKey, preimage []byte, err error) {
	t.Helper()
	if !v.Valid {
		if err == nil {
			t.Fatalf("definition %q is marked invalid but the preimage function accepted it", v.Name)
		}
		return
	}
	if err != nil {
		t.Fatalf("definition %q: %v", v.Name, err)
	}
	v.PreimageHex = hex.EncodeToString(preimage)
	v.Signature = b64(ed25519.Sign(signer, preimage))
}

// buildGolden 用面板实现算出完整的金样本字节。
func buildGolden(t *testing.T) []byte {
	t.Helper()
	k := deriveTestKeys(t)
	g := goldenDefinitions(k)
	pin, _ := PanelKeyFingerprint(pub(k.panelConfig))
	g.Keys = goldenKeys{
		PanelConfigSeedLabel: labelPanelConfig, PanelConfigPublicKey: b64(pub(k.panelConfig)),
		PanelConfigKeyID: KeyID(pub(k.panelConfig)), PanelKeyPin: pin,
		ServerSeedLabel: labelServer, ServerPublicKey: b64(pub(k.server)), ServerKeyID: KeyID(pub(k.server)),
		ServerEncLabel: labelServerEnc, ServerEncPublicKey: b64(k.serverEnc.PublicKey().Bytes()),
		ServerEncKeyID:   KeyID(k.serverEnc.PublicKey().Bytes()),
		ReleaseSeedLabel: labelRelease, ReleasePublicKey: b64(pub(k.release)), ReleaseKeyID: KeyID(pub(k.release)),
		GatewayScalarLabel: labelGateway, GatewaySPKIDER: b64(k.gatewaySPKI), GatewayTLSPin: TLSPin(k.gatewaySPKI),
	}
	for i := range g.ServerRequests {
		v := &g.ServerRequests[i]
		p, err := ServerRequestPreimage(v.fields())
		sealVector(t, &v.vectorResult, k.server, p, err)
	}
	for i := range g.Manifests {
		v := &g.Manifests[i]
		p, err := ManifestPreimage(v.fields())
		sealVector(t, &v.vectorResult, k.panelConfig, p, err)
	}
	for i := range g.Users {
		v := &g.Users[i]
		p, err := UsersPreimage(v.fields())
		sealVector(t, &v.vectorResult, k.panelConfig, p, err)
	}
	var releaseDocSHA string
	for i := range g.ReleaseManifests {
		v := &g.ReleaseManifests[i]
		p, err := ReleaseManifestPreimage(v.fields())
		sealVector(t, &v.vectorResult, k.release, p, err)
		if !v.Valid {
			continue
		}
		doc, err := json.Marshal(releaseDocument{Contract: ReleaseManifestContract, Product: v.Product,
			Version: v.Version, Commit: v.Commit, ReleasedAt: v.ReleasedAt, ReleaseKeyID: v.ReleaseKeyID,
			Artifacts: v.Artifacts, Signature: v.Signature})
		if err != nil {
			t.Fatal(err)
		}
		v.Document = string(doc)
		if releaseDocSHA == "" {
			releaseDocSHA = shaB64(v.Document)
		}
	}
	for i := range g.UpgradeOrders {
		v := &g.UpgradeOrders[i]
		v.ReleaseManifestSHA256 = releaseDocSHA
		p, err := UpgradeOrderPreimage(v.fields())
		sealVector(t, &v.vectorResult, k.panelConfig, p, err)
	}
	for i := range g.KeyPossessions {
		v := &g.KeyPossessions[i]
		p, err := KeyPossessionPreimage(v.fields())
		sealVector(t, &v.vectorResult, k.server, p, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGoldenFileMatchesDefinitions(t *testing.T) {
	want := buildGolden(t)
	ownPath, otherPath := goldenPaths(t)
	if *updateGolden {
		for _, p := range []string{ownPath, otherPath} {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := os.ReadFile(ownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with -bindingcontract.update and commit both copies", ownPath)
	}
}
