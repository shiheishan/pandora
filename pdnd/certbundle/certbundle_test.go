package certbundle

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 金样本由面板侧生成（panel/internal/platform/certbundle 的 -certbundle.update），两份逐字节相同，
// 面板侧的 TestGoldenVectorsIdenticalInPanelAndPdnd 守着这一点。这里用节点侧实现独立重算并解密。
var goldenPath = filepath.Join("testdata", "certbundle-v1-golden.json")

type goldenFile struct {
	Contract  string `json:"contract"`
	Note      string `json:"note"`
	Signature struct {
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
	} `json:"signature"`
	Sealing struct {
		Alg             string `json:"alg"`
		TenantID        string `json:"tenant_id"`
		ServerID        string `json:"server_id"`
		CertID          string `json:"cert_id"`
		Version         uint64 `json:"version"`
		ChainSHA256     string `json:"chain_sha256"`
		Info            string `json:"info"`
		PlaintextSHA256 string `json:"plaintext_sha256"`
		Sealed          string `json:"sealed"`
	} `json:"sealing"`
}

// 夹具密钥与面板侧 golden_test.go 同一套标签派生，全是虚构值，不落在 testdata 里。
type goldenKeys struct {
	configPriv   ed25519.PrivateKey
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
	sk := labelSum("pandora certbundle v1 golden x25519 private")
	pk, err := RecipientPublicKey(sk)
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
	return goldenKeys{
		configPriv:   ed25519.NewKeyFromSeed(labelSum("pandora certbundle v1 golden ed25519 seed")),
		recipientSK:  sk,
		recipientPK:  pk,
		plaintextDER: der,
	}
}

func readGolden(t *testing.T) goldenFile {
	t.Helper()
	body, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func goldenFields(t *testing.T, g goldenFile) (SignatureFields, KeyInfoFields) {
	s := g.Signature
	return SignatureFields{
		TenantID: s.TenantID, ServerID: s.ServerID, NodeID: s.NodeID, Epoch: s.Epoch, EncKeyID: s.EncKeyID,
		ContentSHA256: s.ContentSHA256, KeyID: s.KeyID, IssuedAt: mustTime(t, s.IssuedAt), ExpiresAt: mustTime(t, s.ExpiresAt),
	}, KeyInfoFields{
		TenantID: g.Sealing.TenantID, ServerID: g.Sealing.ServerID, CertID: g.Sealing.CertID,
		Version: g.Sealing.Version, ChainSHA256: g.Sealing.ChainSHA256,
	}
}

// 节点侧实现重建的原像、签名、info 与金样本逐字节一致，并能解开面板封装的私钥。
func TestGoldenVectorsMatchNodeImplementation(t *testing.T) {
	g := readGolden(t)
	k := deriveGoldenKeys(t)
	sf, inf := goldenFields(t, g)
	if g.Contract != Contract || g.Sealing.Alg != KeyAlg {
		t.Fatalf("contract/alg = %q/%q", g.Contract, g.Sealing.Alg)
	}
	preimage, err := SignaturePreimage(sf)
	if err != nil {
		t.Fatal(err)
	}
	if string(preimage) != g.Signature.Preimage {
		t.Fatalf("preimage drifted\n got: %q\nwant: %q", preimage, g.Signature.Preimage)
	}
	pub := k.configPriv.Public().(ed25519.PublicKey)
	if KeyID(pub) != sf.KeyID || KeyID(k.recipientPK) != sf.EncKeyID {
		t.Fatal("golden key ids do not match the fixture keys")
	}
	if sig := base64.StdEncoding.EncodeToString(ed25519.Sign(k.configPriv, preimage)); sig != g.Signature.Signature {
		t.Fatalf("Ed25519 signature drifted: %s", sig)
	}
	if err := VerifySignature(pub, sf, g.Signature.Signature, sf.IssuedAt.Add(time.Minute)); err != nil {
		t.Fatalf("golden signature: %v", err)
	}

	info, err := KeyInfo(inf)
	if err != nil {
		t.Fatal(err)
	}
	if string(info) != g.Sealing.Info {
		t.Fatalf("info drifted\n got: %q\nwant: %q", info, g.Sealing.Info)
	}
	sealed, err := base64.StdEncoding.DecodeString(g.Sealing.Sealed)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := OpenPrivateKey(k.recipientSK, inf, sealed)
	if err != nil {
		t.Fatalf("open golden ciphertext: %v", err)
	}
	sum := sha256.Sum256(pt)
	if !bytes.Equal(pt, k.plaintextDER) || base64.StdEncoding.EncodeToString(sum[:]) != g.Sealing.PlaintextSHA256 {
		t.Fatal("golden plaintext differs from the fixture PKCS#8 key")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 契约套件与 RFC 9180 官方向量对齐：CFRG test-vectors.json 中 mode 0、kem 0x0020、kdf 0x0001、
// aead 0x0002（AES-256-GCM）的 base 向量，第 0 号加密。
func TestHPKESuiteMatchesRFC9180Vector(t *testing.T) {
	kem, kdf, aead := hpkeSuite()
	if kem.ID() != 0x0020 || kdf.ID() != 0x0001 || aead.ID() != 0x0002 {
		t.Fatalf("suite ids = %#x/%#x/%#x", kem.ID(), kdf.ID(), aead.ID())
	}
	sk, err := kem.NewPrivateKey(mustHex(t, "497b4502664cfea5d5af0b39934dac72242a74f8480451e1aee7d6a53320333d"))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(sk.PublicKey().Bytes()); got != "430f4b9859665145a6b1ba274024487bd66f03a2dd577d7753c68d7d7d00c00c" {
		t.Fatalf("pkRm = %s", got)
	}
	r, err := hpke.NewRecipient(
		mustHex(t, "6c93e09869df3402d7bf231bf540fadd35cd56be14f97178f0954db94b7fc256"),
		sk, kdf, aead, mustHex(t, "4f6465206f6e2061204772656369616e2055726e"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := r.Open(mustHex(t, "436f756e742d30"),
		mustHex(t, "e5d84cd531cfb583096e7cfa9641bd3079cf3a91cda813c52deb5f512be9931980a41de125a925cdad859d5b7a"))
	if err != nil {
		t.Fatalf("open RFC 9180 vector: %v", err)
	}
	if !bytes.Equal(pt, mustHex(t, "4265617574792069732074727574682c20747275746820626561757479")) {
		t.Fatalf("plaintext = %x", pt)
	}
}

// 负例：info 任一字节不同、info 字段指向别的版本、换接收方密钥、篡改密文，都解不开。
func TestOpenRejectsWrongInfoRecipientOrTampering(t *testing.T) {
	g := readGolden(t)
	k := deriveGoldenKeys(t)
	_, inf := goldenFields(t, g)
	sealed, _ := base64.StdEncoding.DecodeString(g.Sealing.Sealed)
	info := []byte(g.Sealing.Info)
	kem, kdf, aead := hpkeSuite()
	sk, err := kem.NewPrivateKey(k.recipientSK)
	if err != nil {
		t.Fatal(err)
	}
	for i := range info {
		mutated := bytes.Clone(info)
		mutated[i] ^= 0x01
		if _, err := hpke.Open(sk, kdf, aead, mutated, sealed); err == nil {
			t.Fatalf("ciphertext opened with info byte %d flipped", i)
		}
	}
	for name, mutate := range map[string]func(*KeyInfoFields){
		"version": func(f *KeyInfoFields) { f.Version++ },
		"cert":    func(f *KeyInfoFields) { f.CertID = g.Signature.NodeID },
		"server":  func(f *KeyInfoFields) { f.ServerID = g.Signature.NodeID },
		"tenant":  func(f *KeyInfoFields) { f.TenantID = g.Signature.NodeID },
		"chain":   func(f *KeyInfoFields) { f.ChainSHA256 = ChainSHA256([]byte("another chain")) },
	} {
		f := inf
		mutate(&f)
		if _, err := OpenPrivateKey(k.recipientSK, f, sealed); err == nil {
			t.Fatalf("ciphertext opened with a different %s", name)
		}
	}
	stranger, err := ecdh.X25519().GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivateKey(stranger.Bytes(), inf, sealed); err == nil {
		t.Fatal("ciphertext opened with another recipient key")
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := OpenPrivateKey(k.recipientSK, inf, tampered); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if _, err := OpenPrivateKey(k.recipientSK, inf, sealed[:16]); err == nil {
		t.Fatal("truncated ciphertext opened")
	}
	// 合法 HPKE 但明文不是 PKCS#8：按解密失败处理。
	pk, _ := kem.NewPublicKey(k.recipientPK)
	notKey, err := hpke.Seal(pk, kdf, aead, info, []byte("not a pkcs8 key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivateKey(k.recipientSK, inf, notKey); err == nil || !strings.Contains(err.Error(), "PKCS#8") {
		t.Fatalf("non-PKCS#8 plaintext: %v", err)
	}
}

// 负例：签名任一字节改动、任一签名字段改动、key_id 不是钉住的公钥、窗口外，都验不过。
func TestVerifySignatureRejectsTampering(t *testing.T) {
	g := readGolden(t)
	k := deriveGoldenKeys(t)
	sf, _ := goldenFields(t, g)
	pub := k.configPriv.Public().(ed25519.PublicKey)
	now := sf.IssuedAt.Add(time.Minute)
	raw, _ := base64.StdEncoding.DecodeString(g.Signature.Signature)
	for i := range raw {
		mutated := bytes.Clone(raw)
		mutated[i] ^= 0x01
		if err := VerifySignature(pub, sf, base64.StdEncoding.EncodeToString(mutated), now); err == nil {
			t.Fatalf("signature with byte %d flipped verified", i)
		}
	}
	for name, mutate := range map[string]func(*SignatureFields){
		"tenant":         func(f *SignatureFields) { f.TenantID = f.NodeID },
		"server":         func(f *SignatureFields) { f.ServerID = f.NodeID },
		"node":           func(f *SignatureFields) { f.NodeID = f.ServerID },
		"epoch":          func(f *SignatureFields) { f.Epoch++ },
		"enc_key_id":     func(f *SignatureFields) { f.EncKeyID = KeyID([]byte("other")) },
		"content_sha256": func(f *SignatureFields) { f.ContentSHA256 = ChainSHA256([]byte("other")) },
		"issued_at":      func(f *SignatureFields) { f.IssuedAt = f.IssuedAt.Add(-time.Microsecond) },
	} {
		f := sf
		mutate(&f)
		if err := VerifySignature(pub, f, g.Signature.Signature, now); err == nil {
			t.Fatalf("signature verified after changing %s", name)
		}
	}
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if err := VerifySignature(otherPub, sf, g.Signature.Signature, now); err == nil {
		t.Fatal("verified against a key that is not the pinned config key")
	}
	if err := VerifySignature(pub, sf, g.Signature.Signature, sf.ExpiresAt); err == nil {
		t.Fatal("expired bundle verified")
	}
	if err := VerifySignature(pub, sf, g.Signature.Signature, sf.IssuedAt.Add(-time.Microsecond)); err == nil {
		t.Fatal("not-yet-valid bundle verified")
	}
	if err := VerifySignature(pub, sf, g.Signature.Signature+"=", now); err == nil {
		t.Fatal("non-canonical signature encoding verified")
	}
}

func TestPreimageRejectsNonCanonicalFields(t *testing.T) {
	sf, inf := goldenFields(t, readGolden(t))
	for name, mutate := range map[string]func(*SignatureFields){
		"uppercase uuid":  func(f *SignatureFields) { f.TenantID = strings.ToUpper(f.TenantID) },
		"zero epoch":      func(f *SignatureFields) { f.Epoch = 0 },
		"huge epoch":      func(f *SignatureFields) { f.Epoch = 1 << 63 },
		"padded key id":   func(f *SignatureFields) { f.KeyID += "=" },
		"short hash":      func(f *SignatureFields) { f.ContentSHA256 = "AAAA" },
		"nanoseconds":     func(f *SignatureFields) { f.ExpiresAt = f.ExpiresAt.Add(time.Nanosecond) },
		"window too long": func(f *SignatureFields) { f.ExpiresAt = f.IssuedAt.Add(MaxDeliveryWindow + time.Microsecond) },
		"inverted window": func(f *SignatureFields) { f.ExpiresAt = f.IssuedAt },
		"nil uuid":        func(f *SignatureFields) { f.NodeID = "00000000-0000-0000-0000-000000000000" },
	} {
		f := sf
		mutate(&f)
		if _, err := SignaturePreimage(f); err == nil {
			t.Fatalf("%s: non-canonical preimage accepted", name)
		}
	}
	bad := inf
	bad.CertID = "../../etc"
	if _, err := KeyInfo(bad); err == nil {
		t.Fatal("non-UUID cert_id accepted in info")
	}
}

func TestGenerateRecipientKeyRoundTrip(t *testing.T) {
	sk, pk, err := GenerateRecipientKey()
	if err != nil {
		t.Fatal(err)
	}
	derived, err := RecipientPublicKey(sk)
	if err != nil || !bytes.Equal(derived, pk) || len(pk) != 32 {
		t.Fatalf("derived public key mismatch: %v", err)
	}
	_, inf := goldenFields(t, readGolden(t))
	info, _ := KeyInfo(inf)
	kem, kdf, aead := hpkeSuite()
	hpk, _ := kem.NewPublicKey(pk)
	k := deriveGoldenKeys(t)
	sealed, err := hpke.Seal(hpk, kdf, aead, info, k.plaintextDER)
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := OpenPrivateKey(sk, inf, sealed); err != nil || !bytes.Equal(pt, k.plaintextDER) {
		t.Fatalf("round trip with a generated key: %v", err)
	}
}
