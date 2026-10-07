package certbundle

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
)

// 重新生成金样本：go test ./internal/platform/certbundle -run TestGolden -certbundle.update
// 同时写面板与 pdnd 两份。HPKE 每次封装都随机，所以只有改契约时才重生成，生成后两边一起提交。
var updateGolden = flag.Bool("certbundle.update", false, "rewrite the certbundle golden vectors in panel and pdnd")

const goldenName = "certbundle-v1-golden.json"

var (
	goldenPanelPath = filepath.Join("testdata", goldenName)
	goldenPdndPath  = filepath.Join("..", "..", "..", "..", "pdnd", "certbundle", "testdata", goldenName)
)

// 金样本全部是虚构夹具。三把密钥都由固定标签派生，不落在 testdata 里：
//   - 配置签名 Ed25519 种子 = sha256("pandora certbundle v1 golden ed25519 seed")
//   - 节点 X25519 私钥     = sha256("pandora certbundle v1 golden x25519 private")
//   - 证书私钥（明文）      = P-256 标量 sha256("pandora certbundle v1 golden p256 scalar") 的 PKCS#8 DER
const (
	goldenTenant  = "0193f0a0-aaaa-7000-8000-0000000000a1"
	goldenServer  = "0193f0a0-bbbb-7000-8000-0000000000b2"
	goldenNode    = "0193f0a0-cccc-7000-8000-0000000000c3"
	goldenCert    = "0193f0a0-dddd-7000-8000-0000000000d4"
	goldenEpoch   = 42
	goldenVersion = 3
	goldenChain   = "fictional fullchain for certbundle golden vectors\n"
	goldenContent = `{"certificates":[]}`
	goldenIssued  = "2026-10-07T08:00:00.123456Z"
	goldenExpires = "2026-10-07T08:10:00.123456Z"
)

type goldenSignature struct {
	TenantID      string `json:"tenant_id"`
	ServerID      string `json:"server_id"`
	NodeID        string `json:"node_id"`
	Epoch         uint64 `json:"epoch"`
	EncKeyID      string `json:"enc_key_id"`
	ContentSHA256 string `json:"content_sha256"`
	KeyID         string `json:"key_id"`
	IssuedAt      string `json:"issued_at"`
	ExpiresAt     string `json:"expires_at"`
	Preimage      string `json:"preimage"`
	Signature     string `json:"signature"`
}

type goldenSealing struct {
	Alg             string `json:"alg"`
	TenantID        string `json:"tenant_id"`
	ServerID        string `json:"server_id"`
	CertID          string `json:"cert_id"`
	Version         uint64 `json:"version"`
	ChainSHA256     string `json:"chain_sha256"`
	Info            string `json:"info"`
	PlaintextSHA256 string `json:"plaintext_sha256"`
	Sealed          string `json:"sealed"`
}

type goldenFile struct {
	Contract  string          `json:"contract"`
	Note      string          `json:"note"`
	Signature goldenSignature `json:"signature"`
	Sealing   goldenSealing   `json:"sealing"`
}

type goldenKeys struct {
	signer       *platformcrypto.Signer
	recipientSK  []byte
	recipientPK  []byte
	plaintextDER []byte
}

func labelSum(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func deriveGoldenKeys(t *testing.T) goldenKeys {
	t.Helper()
	signer, err := platformcrypto.NewSigner(labelSum("pandora certbundle v1 golden ed25519 seed"))
	if err != nil {
		t.Fatal(err)
	}
	sk := labelSum("pandora certbundle v1 golden x25519 private")
	x, err := ecdh.X25519().NewPrivateKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	p256, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), labelSum("pandora certbundle v1 golden p256 scalar"))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(p256)
	if err != nil {
		t.Fatal(err)
	}
	return goldenKeys{signer: signer, recipientSK: sk, recipientPK: x.PublicKey().Bytes(), plaintextDER: der}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func goldenSignatureFields(t *testing.T, k goldenKeys) SignatureFields {
	return SignatureFields{
		TenantID: goldenTenant, ServerID: goldenServer, NodeID: goldenNode, Epoch: goldenEpoch,
		EncKeyID: KeyID(k.recipientPK), ContentSHA256: ChainSHA256([]byte(goldenContent)), KeyID: k.signer.KeyID(),
		IssuedAt: mustTime(t, goldenIssued), ExpiresAt: mustTime(t, goldenExpires),
	}
}

func goldenInfoFields() KeyInfoFields {
	return KeyInfoFields{
		TenantID: goldenTenant, ServerID: goldenServer, CertID: goldenCert, Version: goldenVersion,
		ChainSHA256: ChainSHA256([]byte(goldenChain)),
	}
}

// expectedGolden 用面板实现重算金样本里所有确定性的部分；sealed 是随机的，由调用方填。
func expectedGolden(t *testing.T, k goldenKeys) goldenFile {
	t.Helper()
	sf := goldenSignatureFields(t, k)
	preimage, err := SignaturePreimage(sf)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(k.signer, sf)
	if err != nil {
		t.Fatal(err)
	}
	inf := goldenInfoFields()
	info, err := KeyInfo(inf)
	if err != nil {
		t.Fatal(err)
	}
	pt := sha256.Sum256(k.plaintextDER)
	return goldenFile{
		Contract: Contract,
		Note:     "fictional fixtures only; keys are derived from fixed labels in golden_test.go and are not stored here",
		Signature: goldenSignature{
			TenantID: sf.TenantID, ServerID: sf.ServerID, NodeID: sf.NodeID, Epoch: sf.Epoch, EncKeyID: sf.EncKeyID,
			ContentSHA256: sf.ContentSHA256, KeyID: sf.KeyID, IssuedAt: goldenIssued, ExpiresAt: goldenExpires,
			Preimage: string(preimage), Signature: sig,
		},
		Sealing: goldenSealing{
			Alg: KeyAlg, TenantID: inf.TenantID, ServerID: inf.ServerID, CertID: inf.CertID, Version: inf.Version,
			ChainSHA256: inf.ChainSHA256, Info: string(info), PlaintextSHA256: base64.StdEncoding.EncodeToString(pt[:]),
		},
	}
}

func writeGolden(t *testing.T, g goldenFile) {
	t.Helper()
	body, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	for _, p := range []string{goldenPanelPath, goldenPdndPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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

// 面板实现重算出的原像、签名、info 与金样本逐字节一致；金样本里的密文用面板的 info 能解开，明文就是夹具私钥。
func TestGoldenVectorsMatchPanelImplementation(t *testing.T) {
	k := deriveGoldenKeys(t)
	want := expectedGolden(t, k)
	if *updateGolden {
		sealed, err := SealPrivateKey(k.recipientPK, goldenInfoFields(), k.plaintextDER)
		if err != nil {
			t.Fatal(err)
		}
		want.Sealing.Sealed = base64.StdEncoding.EncodeToString(sealed)
		writeGolden(t, want)
	}
	got, _ := readGolden(t, goldenPanelPath)
	want.Sealing.Sealed = got.Sealing.Sealed
	if got != want {
		gj, _ := json.MarshalIndent(got, "", "  ")
		wj, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("golden vectors drifted from the panel implementation\n got: %s\nwant: %s", gj, wj)
	}
	sf := goldenSignatureFields(t, k)
	if err := VerifySignature(k.signer.PublicKey(), sf, got.Signature.Signature, sf.IssuedAt.Add(time.Minute)); err != nil {
		t.Fatalf("golden signature: %v", err)
	}

	sealed, err := base64.StdEncoding.DecodeString(got.Sealing.Sealed)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := openWith(k.recipientSK, []byte(got.Sealing.Info), sealed)
	if err != nil {
		t.Fatalf("open golden ciphertext: %v", err)
	}
	if !bytes.Equal(pt, k.plaintextDER) {
		t.Fatal("golden plaintext differs from the fixture PKCS#8 key")
	}
}

// 两份金样本（面板与 pdnd）必须逐字节相同：两端各自的测试都钉在同一份向量上，才算互相钉住。
func TestGoldenVectorsIdenticalInPanelAndPdnd(t *testing.T) {
	_, panel := readGolden(t, goldenPanelPath)
	_, pdnd := readGolden(t, goldenPdndPath)
	if !bytes.Equal(panel, pdnd) {
		t.Fatalf("%s and %s differ; regenerate with -certbundle.update and commit both", goldenPanelPath, goldenPdndPath)
	}
}

// openWith 直接调标准库，按契约套件解开 enc||ciphertext（面板生产代码不需要解密）。
func openWith(recipientSK, info, sealed []byte) ([]byte, error) {
	kem, kdf, aead := hpkeSuite()
	sk, err := kem.NewPrivateKey(recipientSK)
	if err != nil {
		return nil, err
	}
	return hpke.Open(sk, kdf, aead, info, sealed)
}
