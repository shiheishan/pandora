package bindingcontract

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// 本文件在 pdnd/bindingcontract 里有一份同样的副本：两端各自从金样本读输入、用本端实现重算，
// 原像字节与签名必须完全一致。

// verifySigned 核对一个签名向量：valid 时原像逐字节相同、签名能用对应公钥验过、
// 用派生私钥重签得到同一签名（Ed25519 是确定性的）；invalid 时原像函数必须报错。
func verifySigned(t *testing.T, kind string, v vectorResult, signer ed25519.PrivateKey, preimage []byte, err error) {
	t.Helper()
	if !v.Valid {
		if err == nil {
			t.Errorf("%s %q: expected rejection, got preimage %q", kind, v.Name, preimage)
		}
		if v.PreimageHex != "" || v.Signature != "" {
			t.Errorf("%s %q: invalid vector must not carry preimage or signature", kind, v.Name)
		}
		return
	}
	if err != nil {
		t.Errorf("%s %q: unexpected error: %v", kind, v.Name, err)
		return
	}
	if got := hex.EncodeToString(preimage); got != v.PreimageHex {
		t.Errorf("%s %q: preimage drifted\n got: %q\nwant hex: %s", kind, v.Name, preimage, v.PreimageHex)
		return
	}
	sig, decErr := base64.StdEncoding.DecodeString(v.Signature)
	if decErr != nil || !ed25519.Verify(pub(signer), preimage, sig) {
		t.Errorf("%s %q: signature does not verify", kind, v.Name)
	}
	if got := b64(ed25519.Sign(signer, preimage)); got != v.Signature {
		t.Errorf("%s %q: re-signing gave %s, golden has %s", kind, v.Name, got, v.Signature)
	}
}

func TestGoldenKeysMatchLabels(t *testing.T) {
	ownPath, _ := goldenPaths(t)
	g, _ := readGolden(t, ownPath)
	k := deriveTestKeys(t)
	pin, err := PanelKeyFingerprint(pub(k.panelConfig))
	if err != nil {
		t.Fatal(err)
	}
	want := goldenKeys{
		PanelConfigSeedLabel: labelPanelConfig, PanelConfigPublicKey: b64(pub(k.panelConfig)),
		PanelConfigKeyID: KeyID(pub(k.panelConfig)), PanelKeyPin: pin,
		ServerSeedLabel: labelServer, ServerPublicKey: b64(pub(k.server)), ServerKeyID: KeyID(pub(k.server)),
		ServerEncLabel: labelServerEnc, ServerEncPublicKey: b64(k.serverEnc.PublicKey().Bytes()),
		ServerEncKeyID:   KeyID(k.serverEnc.PublicKey().Bytes()),
		ReleaseSeedLabel: labelRelease, ReleasePublicKey: b64(pub(k.release)), ReleaseKeyID: KeyID(pub(k.release)),
		GatewayScalarLabel: labelGateway, GatewaySPKIDER: b64(k.gatewaySPKI), GatewayTLSPin: TLSPin(k.gatewaySPKI),
	}
	if g.Keys != want {
		t.Fatalf("golden keys drifted from labels\n got: %+v\nwant: %+v", g.Keys, want)
	}
	if err := CheckPanelKey(g.Keys.PanelKeyPin, pub(k.panelConfig)); err != nil {
		t.Fatalf("panel key pin: %v", err)
	}
	if err := CheckPanelKey(g.Keys.PanelKeyPin, pub(k.server)); err == nil {
		t.Fatal("panel key pin must reject another key")
	}
	if err := CheckTLSPin(k.gatewaySPKI, g.Keys.GatewayTLSPin); err != nil {
		t.Fatalf("gateway pin: %v", err)
	}
}

func TestGoldenSignedVectors(t *testing.T) {
	ownPath, _ := goldenPaths(t)
	g, _ := readGolden(t, ownPath)
	k := deriveTestKeys(t)
	if len(g.ServerRequests) == 0 || len(g.Manifests) == 0 || len(g.Users) == 0 || len(g.UpgradeOrders) == 0 ||
		len(g.ReleaseManifests) == 0 || len(g.KeyPossessions) == 0 {
		t.Fatal("golden file is missing a vector family")
	}
	for _, v := range g.ServerRequests {
		if v.Valid && BodySHA256([]byte(v.Body)) != v.BodySHA256 {
			t.Errorf("server request %q: body_sha256 does not match body", v.Name)
		}
		p, err := ServerRequestPreimage(v.fields())
		verifySigned(t, "server request", v.vectorResult, k.server, p, err)
	}
	for _, v := range g.Manifests {
		p, err := ManifestPreimage(v.fields())
		verifySigned(t, "manifest", v.vectorResult, k.panelConfig, p, err)
	}
	for _, v := range g.Users {
		if v.Valid && BodySHA256([]byte(v.Payload)) != v.ContentSHA256 {
			t.Errorf("users %q: content_sha256 does not match payload", v.Name)
		}
		p, err := UsersPreimage(v.fields())
		verifySigned(t, "users", v.vectorResult, k.panelConfig, p, err)
	}
	for _, v := range g.ReleaseManifests {
		p, err := ReleaseManifestPreimage(v.fields())
		verifySigned(t, "release manifest", v.vectorResult, k.release, p, err)
		if v.Valid {
			checkReleaseDocument(t, v)
		}
	}
	for _, v := range g.UpgradeOrders {
		p, err := UpgradeOrderPreimage(v.fields())
		verifySigned(t, "upgrade order", v.vectorResult, k.panelConfig, p, err)
	}
	for _, v := range g.KeyPossessions {
		p, err := KeyPossessionPreimage(v.fields())
		verifySigned(t, "key possession", v.vectorResult, k.server, p, err)
	}
	// 合法的升级指令必须点名金样本里某份合法发布清单文档的哈希
	docs := map[string]bool{}
	for _, r := range g.ReleaseManifests {
		if r.Valid {
			sum := sha256.Sum256([]byte(r.Document))
			docs[b64(sum[:])] = true
		}
	}
	for _, o := range g.UpgradeOrders {
		if o.Valid && !docs[o.ReleaseManifestSHA256] {
			t.Errorf("upgrade order %q names a release manifest that is not in the golden file", o.Name)
		}
	}
}

// releaseDocument 是线上发布清单 JSON 的字段与顺序。
type releaseDocument struct {
	Contract     string                  `json:"contract"`
	Product      string                  `json:"product"`
	Version      string                  `json:"version"`
	Commit       string                  `json:"commit"`
	ReleasedAt   string                  `json:"released_at"`
	ReleaseKeyID string                  `json:"release_key_id"`
	Artifacts    []releaseArtifactVector `json:"artifacts"`
	Signature    string                  `json:"signature"`
}

// checkReleaseDocument 核对线上文档解出来就是向量里的字段与签名。
func checkReleaseDocument(t *testing.T, v releaseVector) {
	t.Helper()
	var doc releaseDocument
	dec := json.NewDecoder(bytes.NewReader([]byte(v.Document)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Errorf("release %q: decode document: %v", v.Name, err)
		return
	}
	want := releaseDocument{Contract: ReleaseManifestContract, Product: v.Product, Version: v.Version,
		Commit: v.Commit, ReleasedAt: v.ReleasedAt, ReleaseKeyID: v.ReleaseKeyID, Artifacts: v.Artifacts,
		Signature: v.Signature}
	if !reflect.DeepEqual(doc, want) {
		t.Errorf("release %q: document fields differ from the vector", v.Name)
	}
}

func TestGoldenPanelOrigins(t *testing.T) {
	ownPath, _ := goldenPaths(t)
	g, _ := readGolden(t, ownPath)
	if len(g.PanelOrigins) == 0 {
		t.Fatal("no panel origin vectors")
	}
	for _, v := range g.PanelOrigins {
		got, err := CanonicalPanelOrigin(v.Input)
		switch {
		case v.Valid && err != nil:
			t.Errorf("origin %q: unexpected error %v", v.Input, err)
		case v.Valid && got != v.Canonical:
			t.Errorf("origin %q: got %q want %q", v.Input, got, v.Canonical)
		case !v.Valid && err == nil:
			t.Errorf("origin %q: expected rejection, got %q", v.Input, got)
		}
	}
}

func TestGoldenAgentVersions(t *testing.T) {
	ownPath, _ := goldenPaths(t)
	g, _ := readGolden(t, ownPath)
	if len(g.AgentVersions) == 0 {
		t.Fatal("no agent version vectors")
	}
	for _, v := range g.AgentVersions {
		got, err := CompareAgentVersions(v.A, v.B)
		switch {
		case v.Valid && (err != nil || got != v.Compare):
			t.Errorf("compare %q %q: got %d, %v; want %d", v.A, v.B, got, err, v.Compare)
		case !v.Valid && err == nil:
			t.Errorf("compare %q %q: expected rejection", v.A, v.B)
		}
	}
}

func TestGoldenSamePanelTenant(t *testing.T) {
	ownPath, _ := goldenPaths(t)
	g, _ := readGolden(t, ownPath)
	if len(g.SamePanelTenant) == 0 {
		t.Fatal("no same-panel-tenant vectors")
	}
	for i, v := range g.SamePanelTenant {
		a, b := PanelTenant(v.A), PanelTenant(v.B)
		if got := SamePanelTenant(a, b); got != v.Same {
			t.Errorf("same_panel_tenant[%d]: got %v want %v", i, got, v.Same)
		}
		if got := SamePanelTenant(b, a); got != v.Same {
			t.Errorf("same_panel_tenant[%d]: not symmetric", i)
		}
	}
}

func TestGoldenEnums(t *testing.T) {
	ownPath, _ := goldenPaths(t)
	g, _ := readGolden(t, ownPath)
	want := goldenEnums{FailureCodes: FailureCodes, WarningOnlyCodes: WarningOnlyCodes, RuntimeStates: RuntimeStates,
		ReceiptResults: ReceiptResults, UpgradeStates: UpgradeStates, UpgradeFailureCodes: UpgradeFailureCodes,
		EventTypes: EventTypes, UnboundReasons: UnboundReasons}
	if !reflect.DeepEqual(g.Enums, want) {
		t.Fatalf("enums drifted from golden\n got: %+v\nwant: %+v", want, g.Enums)
	}
	wantContracts := map[string]string{
		"server_request": ServerRequestContract, "manifest": ManifestContract, "users": UsersContract,
		"upgrade_order": UpgradeOrderContract, "release_manifest": ReleaseManifestContract,
		"key_possession": KeyPossessionContract,
	}
	if !reflect.DeepEqual(g.Contracts, wantContracts) {
		t.Fatalf("contract names drifted\n got: %v\nwant: %v", g.Contracts, wantContracts)
	}
}

// 两份金样本（面板与 pdnd）必须逐字节相同：两端各自的测试都钉在同一份向量上，才算互相钉住。
func TestGoldenIdenticalInPanelAndPdnd(t *testing.T) {
	ownPath, otherPath := goldenPaths(t)
	_, own := readGolden(t, ownPath)
	_, other := readGolden(t, otherPath)
	if !bytes.Equal(own, other) {
		t.Fatalf("%s and %s differ; regenerate in panel with -bindingcontract.update and commit both", ownPath, otherPath)
	}
}
