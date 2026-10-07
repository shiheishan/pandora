package bindingcontract

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 金样本的结构。panel/internal/platform/bindingcontract 里有一份同样的定义，两份 testdata 逐字节相同。
// 改结构要两边同改，并在面板侧重新生成（见面板的 golden_gen_test.go）。

const goldenName = "bindingcontract-v1-golden.json"

// goldenPaths 返回本端与另一端两份金样本的绝对路径（按本文件位置定位，不依赖工作目录）。
// pdnd 这边：本端是 pdnd，另一端是面板。
func goldenPaths(t *testing.T) (own, other string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	return filepath.Join(dir, "testdata", goldenName),
		filepath.Join(dir, "..", "..", "panel", "internal", "platform", "bindingcontract", "testdata", goldenName)
}

// 测试密钥一律由固定标签派生：种子 / 私钥 / 标量 = sha256(标签)。它们只用于金样本，绝不能用于生产。
const (
	labelPanelConfig = "pandora bindingcontract v1 TEST ONLY panel config ed25519 seed"
	labelServer      = "pandora bindingcontract v1 TEST ONLY server identity ed25519 seed"
	labelServerEnc   = "pandora bindingcontract v1 TEST ONLY server x25519 private key"
	labelRelease     = "pandora bindingcontract v1 TEST ONLY release ed25519 seed"
	labelGateway     = "pandora bindingcontract v1 TEST ONLY gateway p256 scalar"
)

type goldenKeys struct {
	PanelConfigSeedLabel string `json:"panel_config_seed_label"`
	PanelConfigPublicKey string `json:"panel_config_public_key"`
	PanelConfigKeyID     string `json:"panel_config_key_id"`
	PanelKeyPin          string `json:"panel_key_pin"`
	ServerSeedLabel      string `json:"server_seed_label"`
	ServerPublicKey      string `json:"server_public_key"`
	ServerKeyID          string `json:"server_key_id"`
	ServerEncLabel       string `json:"server_enc_private_label"`
	ServerEncPublicKey   string `json:"server_enc_public_key"`
	ServerEncKeyID       string `json:"server_enc_key_id"`
	ReleaseSeedLabel     string `json:"release_seed_label"`
	ReleasePublicKey     string `json:"release_public_key"`
	ReleaseKeyID         string `json:"release_key_id"`
	GatewayScalarLabel   string `json:"gateway_p256_scalar_label"`
	GatewaySPKIDER       string `json:"gateway_spki_der"`
	GatewayTLSPin        string `json:"gateway_tls_pin"`
}

// 签名向量的公共尾部：valid 为 false 时原像函数必须报错，preimage_hex 与 signature 为空。
type vectorResult struct {
	Name        string `json:"name"`
	Valid       bool   `json:"valid"`
	PreimageHex string `json:"preimage_hex,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

type requestVector struct {
	vectorResult
	Method     string `json:"method"`
	Target     string `json:"target"`
	TenantID   string `json:"tenant_id"`
	ServerID   string `json:"server_id"`
	Serial     int64  `json:"serial"`
	Timestamp  string `json:"ts"`
	Nonce      string `json:"nonce"`
	ReportID   string `json:"report_id"`
	Body       string `json:"body"`
	BodySHA256 string `json:"body_sha256"`
}

type manifestNodeVector struct {
	NodeID              string `json:"node_id"`
	Protocol            string `json:"protocol"`
	Port                int    `json:"port"`
	L4                  string `json:"l4"`
	PoolID              string `json:"pool_id"`
	EffectiveGeneration uint64 `json:"effective_generation"`
	ContentSHA256       string `json:"content_sha256"`
}

type manifestIntervalsVector struct {
	ManifestPullSeconds     int `json:"manifest_pull_seconds"`
	UsersPullSeconds        int `json:"users_pull_seconds"`
	ReportSeconds           int `json:"report_seconds"`
	StreamOnlinePullSeconds int `json:"stream_online_pull_seconds"`
}

type manifestVector struct {
	vectorResult
	TenantID           string                  `json:"tenant_id"`
	ServerID           string                  `json:"server_id"`
	Serial             int64                   `json:"serial"`
	ManifestGeneration uint64                  `json:"manifest_generation"`
	State              string                  `json:"state"`
	UnboundReason      string                  `json:"unbound_reason"`
	Features           []string                `json:"features"`
	MinAgentVersion    string                  `json:"min_agent_version"`
	TLSPinNext         string                  `json:"tls_pin_next"`
	Intervals          manifestIntervalsVector `json:"intervals"`
	Nodes              []manifestNodeVector    `json:"nodes"`
	RequestNonce       string                  `json:"request_nonce"`
	KeyID              string                  `json:"key_id"`
	IssuedAt           string                  `json:"issued_at"`
	ExpiresAt          string                  `json:"expires_at"`
}

type usersVector struct {
	vectorResult
	TenantID      string `json:"tenant_id"`
	ServerID      string `json:"server_id"`
	Serial        int64  `json:"serial"`
	PoolID        string `json:"pool_id"`
	UsersVersion  uint64 `json:"users_version"`
	Kind          string `json:"kind"`
	UserCount     int    `json:"user_count"`
	Payload       string `json:"payload"`
	ContentSHA256 string `json:"content_sha256"`
	RequestNonce  string `json:"request_nonce"`
	KeyID         string `json:"key_id"`
	IssuedAt      string `json:"issued_at"`
	ExpiresAt     string `json:"expires_at"`
}

type upgradeOrderVector struct {
	vectorResult
	TenantID              string `json:"tenant_id"`
	ServerID              string `json:"server_id"`
	Serial                int64  `json:"serial"`
	OrderID               string `json:"order_id"`
	TargetVersion         string `json:"target_version"`
	ReleaseManifestSHA256 string `json:"release_manifest_sha256"`
	RequestNonce          string `json:"request_nonce"`
	KeyID                 string `json:"key_id"`
	IssuedAt              string `json:"issued_at"`
	ExpiresAt             string `json:"expires_at"`
}

type releaseArtifactVector struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type releaseVector struct {
	vectorResult
	Product      string                  `json:"product"`
	Version      string                  `json:"version"`
	Commit       string                  `json:"commit"`
	ReleasedAt   string                  `json:"released_at"`
	ReleaseKeyID string                  `json:"release_key_id"`
	Artifacts    []releaseArtifactVector `json:"artifacts"`
	// Document 是线上发布清单 JSON 的完整字节（含签名），升级指令的 release_manifest_sha256 对它取哈希。
	Document string `json:"document,omitempty"`
}

type possessionVector struct {
	vectorResult
	TenantID           string `json:"tenant_id"`
	NodeID             string `json:"node_id"`
	RequestNonce       string `json:"request_nonce"`
	ServerPublicKey    string `json:"server_public_key"`
	ServerEncPublicKey string `json:"server_enc_public_key"`
}

type originVector struct {
	Input     string `json:"input"`
	Valid     bool   `json:"valid"`
	Canonical string `json:"canonical,omitempty"`
}

type versionVector struct {
	A       string `json:"a"`
	B       string `json:"b"`
	Valid   bool   `json:"valid"`
	Compare int    `json:"compare"`
}

type panelTenantVector struct {
	A    PanelTenantJSON `json:"a"`
	B    PanelTenantJSON `json:"b"`
	Same bool            `json:"same"`
}

// PanelTenantJSON 只是 PanelTenant 带 JSON 标签的影子（测试用）。
type PanelTenantJSON struct {
	Origin   string `json:"origin"`
	PanelKey string `json:"panel_key"`
	TenantID string `json:"tenant_id"`
}

type goldenEnums struct {
	FailureCodes        []string `json:"failure_codes"`
	WarningOnlyCodes    []string `json:"warning_only_codes"`
	RuntimeStates       []string `json:"runtime_states"`
	ReceiptResults      []string `json:"receipt_results"`
	UpgradeStates       []string `json:"upgrade_states"`
	UpgradeFailureCodes []string `json:"upgrade_failure_codes"`
	EventTypes          []string `json:"event_types"`
	UnboundReasons      []string `json:"unbound_reasons"`
}

type goldenFile struct {
	Contracts        map[string]string    `json:"contracts"`
	Note             string               `json:"note"`
	Keys             goldenKeys           `json:"keys"`
	ServerRequests   []requestVector      `json:"server_requests"`
	Manifests        []manifestVector     `json:"manifests"`
	Users            []usersVector        `json:"users"`
	UpgradeOrders    []upgradeOrderVector `json:"upgrade_orders"`
	ReleaseManifests []releaseVector      `json:"release_manifests"`
	KeyPossessions   []possessionVector   `json:"key_possessions"`
	PanelOrigins     []originVector       `json:"panel_origins"`
	AgentVersions    []versionVector      `json:"agent_versions"`
	SamePanelTenant  []panelTenantVector  `json:"same_panel_tenant"`
	Enums            goldenEnums          `json:"enums"`
}

func readGolden(t *testing.T, path string) (goldenFile, []byte) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden vectors: %v", err)
	}
	var g goldenFile
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return g, body
}

func labelSum(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

// testKeys 是由标签派生出的测试私钥。
type testKeys struct {
	panelConfig ed25519.PrivateKey
	server      ed25519.PrivateKey
	serverEnc   *ecdh.PrivateKey
	release     ed25519.PrivateKey
	gatewaySPKI []byte
}

func deriveTestKeys(t *testing.T) testKeys {
	t.Helper()
	enc, err := ecdh.X25519().NewPrivateKey(labelSum(labelServerEnc))
	if err != nil {
		t.Fatal(err)
	}
	gw, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), labelSum(labelGateway))
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&gw.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return testKeys{
		panelConfig: ed25519.NewKeyFromSeed(labelSum(labelPanelConfig)),
		server:      ed25519.NewKeyFromSeed(labelSum(labelServer)),
		serverEnc:   enc,
		release:     ed25519.NewKeyFromSeed(labelSum(labelRelease)),
		gatewaySPKI: spki,
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func pub(k ed25519.PrivateKey) []byte { return k.Public().(ed25519.PublicKey) }

func (r requestVector) fields() ServerRequestFields {
	return ServerRequestFields{Method: r.Method, Target: r.Target, TenantID: r.TenantID, ServerID: r.ServerID,
		Serial: r.Serial, Timestamp: r.Timestamp, Nonce: r.Nonce, ReportID: r.ReportID, BodySHA256: r.BodySHA256}
}

func (m manifestVector) fields() ManifestFields {
	nodes := make([]ManifestNode, 0, len(m.Nodes))
	for _, n := range m.Nodes {
		nodes = append(nodes, ManifestNode{NodeID: n.NodeID, Protocol: n.Protocol, Port: n.Port, L4: n.L4,
			PoolID: n.PoolID, EffectiveGeneration: n.EffectiveGeneration, ContentSHA256: n.ContentSHA256})
	}
	return ManifestFields{TenantID: m.TenantID, ServerID: m.ServerID, Serial: m.Serial,
		ManifestGeneration: m.ManifestGeneration, State: m.State, UnboundReason: m.UnboundReason,
		Features: m.Features, MinAgentVersion: m.MinAgentVersion, TLSPinNext: m.TLSPinNext,
		Intervals: ManifestIntervals{ManifestPullSeconds: m.Intervals.ManifestPullSeconds,
			UsersPullSeconds: m.Intervals.UsersPullSeconds, ReportSeconds: m.Intervals.ReportSeconds,
			StreamOnlinePullSeconds: m.Intervals.StreamOnlinePullSeconds},
		Nodes: nodes, RequestNonce: m.RequestNonce, KeyID: m.KeyID, IssuedAt: m.IssuedAt, ExpiresAt: m.ExpiresAt}
}

func (u usersVector) fields() UsersFields {
	return UsersFields{TenantID: u.TenantID, ServerID: u.ServerID, Serial: u.Serial, PoolID: u.PoolID,
		UsersVersion: u.UsersVersion, Kind: u.Kind, UserCount: u.UserCount, ContentSHA256: u.ContentSHA256,
		RequestNonce: u.RequestNonce, KeyID: u.KeyID, IssuedAt: u.IssuedAt, ExpiresAt: u.ExpiresAt}
}

func (o upgradeOrderVector) fields() UpgradeOrderFields {
	return UpgradeOrderFields{TenantID: o.TenantID, ServerID: o.ServerID, Serial: o.Serial, OrderID: o.OrderID,
		TargetVersion: o.TargetVersion, ReleaseManifestSHA256: o.ReleaseManifestSHA256,
		RequestNonce: o.RequestNonce, KeyID: o.KeyID, IssuedAt: o.IssuedAt, ExpiresAt: o.ExpiresAt}
}

func (r releaseVector) fields() ReleaseManifestFields {
	arts := make([]ReleaseArtifact, 0, len(r.Artifacts))
	for _, a := range r.Artifacts {
		arts = append(arts, ReleaseArtifact{Name: a.Name, Size: a.Size, SHA256: a.SHA256})
	}
	return ReleaseManifestFields{Product: r.Product, Version: r.Version, Commit: r.Commit,
		ReleasedAt: r.ReleasedAt, ReleaseKeyID: r.ReleaseKeyID, Artifacts: arts}
}

func (p possessionVector) fields() KeyPossessionFields {
	return KeyPossessionFields{TenantID: p.TenantID, NodeID: p.NodeID, RequestNonce: p.RequestNonce,
		ServerPublicKey: p.ServerPublicKey, ServerEncPublicKey: p.ServerEncPublicKey}
}
