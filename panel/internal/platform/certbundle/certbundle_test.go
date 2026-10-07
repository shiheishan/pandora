package certbundle

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 契约套件与 RFC 9180 官方向量对齐：CFRG test-vectors.json 中 mode 0、kem 0x0020、kdf 0x0001、
// aead 0x0002（AES-256-GCM）的 base 向量，第 0 号加密。该文件的 A.1/A.2 两组与 RFC 9180 正文逐字一致。
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

// 负例：info 任一字节不同、换接收方密钥、篡改密文，都解不开。
func TestSealedKeyIsBoundToInfoAndRecipient(t *testing.T) {
	k := deriveGoldenKeys(t)
	inf := goldenInfoFields()
	sealed, err := SealPrivateKey(k.recipientPK, inf, k.plaintextDER)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := KeyInfo(inf)
	if pt, err := openWith(k.recipientSK, info, sealed); err != nil || !bytes.Equal(pt, k.plaintextDER) {
		t.Fatalf("round trip: %v", err)
	}
	for i := range info {
		mutated := bytes.Clone(info)
		mutated[i] ^= 0x01
		if _, err := openWith(k.recipientSK, mutated, sealed); err == nil {
			t.Fatalf("ciphertext opened with info byte %d flipped", i)
		}
	}
	other := inf
	other.Version++
	otherInfo, _ := KeyInfo(other)
	if _, err := openWith(k.recipientSK, otherInfo, sealed); err == nil {
		t.Fatal("ciphertext opened for another certificate version")
	}
	stranger, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openWith(stranger.Bytes(), info, sealed); err == nil {
		t.Fatal("ciphertext opened with another recipient key")
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := openWith(k.recipientSK, info, tampered); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if _, err := SealPrivateKey(k.recipientPK, inf, []byte("not pkcs8")); err == nil {
		t.Fatal("non-PKCS#8 plaintext sealed")
	}
	if _, err := SealPrivateKey(k.recipientPK[:31], inf, k.plaintextDER); err == nil {
		t.Fatal("short recipient public key accepted")
	}
}

// 负例：签名任一字节改动、任一签名字段改动都验不过；key_id 与 signer 不符拒签。
func TestSignatureRejectsTampering(t *testing.T) {
	k := deriveGoldenKeys(t)
	sf := goldenSignatureFields(t, k)
	sig, err := Sign(k.signer, sf)
	if err != nil {
		t.Fatal(err)
	}
	now := sf.IssuedAt.Add(time.Minute)
	if err := VerifySignature(k.signer.PublicKey(), sf, sig, now); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(sig)
	for i := range raw {
		mutated := bytes.Clone(raw)
		mutated[i] ^= 0x01
		if err := VerifySignature(k.signer.PublicKey(), sf, base64.StdEncoding.EncodeToString(mutated), now); err == nil {
			t.Fatalf("signature with byte %d flipped verified", i)
		}
	}
	for name, mutate := range map[string]func(*SignatureFields){
		"epoch":          func(f *SignatureFields) { f.Epoch++ },
		"server":         func(f *SignatureFields) { f.ServerID = goldenNode },
		"node":           func(f *SignatureFields) { f.NodeID = goldenServer },
		"enc_key_id":     func(f *SignatureFields) { f.EncKeyID = KeyID([]byte("other")) },
		"content_sha256": func(f *SignatureFields) { f.ContentSHA256 = ChainSHA256([]byte("other")) },
		"expires_at":     func(f *SignatureFields) { f.ExpiresAt = f.ExpiresAt.Add(-time.Microsecond) },
	} {
		f := sf
		mutate(&f)
		if err := VerifySignature(k.signer.PublicKey(), f, sig, now); err == nil {
			t.Fatalf("signature verified after changing %s", name)
		}
	}
	if err := VerifySignature(k.signer.PublicKey(), sf, sig, sf.ExpiresAt); err == nil {
		t.Fatal("expired bundle verified")
	}
	if err := VerifySignature(k.signer.PublicKey(), sf, sig, sf.IssuedAt.Add(-time.Microsecond)); err == nil {
		t.Fatal("not-yet-valid bundle verified")
	}
	wrong := sf
	wrong.KeyID = KeyID([]byte("someone else"))
	if _, err := Sign(k.signer, wrong); err == nil {
		t.Fatal("signed with a key_id that is not the signer's")
	}
}

func TestPreimageRejectsNonCanonicalFields(t *testing.T) {
	k := deriveGoldenKeys(t)
	base := goldenSignatureFields(t, k)
	for name, mutate := range map[string]func(*SignatureFields){
		"uppercase uuid":   func(f *SignatureFields) { f.TenantID = strings.ToUpper(f.TenantID) },
		"zero epoch":       func(f *SignatureFields) { f.Epoch = 0 },
		"padded key id":    func(f *SignatureFields) { f.KeyID += "=" },
		"short hash":       func(f *SignatureFields) { f.ContentSHA256 = "AAAA" },
		"nanoseconds":      func(f *SignatureFields) { f.IssuedAt = f.IssuedAt.Add(time.Nanosecond) },
		"window too long":  func(f *SignatureFields) { f.ExpiresAt = f.IssuedAt.Add(MaxDeliveryWindow + time.Microsecond) },
		"inverted window":  func(f *SignatureFields) { f.ExpiresAt = f.IssuedAt },
		"nil node uuid":    func(f *SignatureFields) { f.NodeID = "00000000-0000-0000-0000-000000000000" },
		"bad enc key id":   func(f *SignatureFields) { f.EncKeyID = "!" },
		"missing server":   func(f *SignatureFields) { f.ServerID = "" },
		"zero issued time": func(f *SignatureFields) { f.IssuedAt = time.Time{} },
	} {
		f := base
		mutate(&f)
		if _, err := SignaturePreimage(f); err == nil {
			t.Fatalf("%s: non-canonical preimage accepted", name)
		}
	}
	inf := goldenInfoFields()
	inf.Version = 0
	if _, err := KeyInfo(inf); err == nil {
		t.Fatal("zero version accepted in info")
	}
}
