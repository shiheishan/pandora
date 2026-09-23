package ca42authority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestParseAndVerifyAuthorityDescriptor(t *testing.T) {
	fixture := newAuthorityFixture()
	data := fixture.descriptor(t, nil)
	result, err := ParseAndVerify(data, fixture.roots, "amd64", fixture.host, time.Unix(1700000100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthorityEpoch != 1 || result.AuthoritySequence != 1 ||
		result.AttemptID != "attempt-ca42-1" || result.RootSignatureKeyIDs != [2]string{"root-a", "root-b"} {
		t.Fatalf("unexpected descriptor: %#v", result)
	}
	if result.SHA256 != sha256.Sum256(data) || result.BindingSHA256 == ([sha256.Size]byte{}) ||
		result.ReleaseSignerSHA256 != sha256.Sum256(fixture.releasePublic) {
		t.Fatal("descriptor identity binding lost")
	}
}

func TestParseAndVerifyAuthorityDescriptorRejectsDrift(t *testing.T) {
	fixture := newAuthorityFixture()
	tests := []struct {
		name   string
		mutate func([]string)
		now    int64
	}{
		{"wrong purpose", func(v []string) { v[3] = "other" }, 1700000100},
		{"wrong architecture", func(v []string) { v[11] = "arm64" }, 1700000100},
		{"zero epoch", func(v []string) { v[7] = "0" }, 1700000100},
		{"leading zero sequence", func(v []string) { v[8] = "01" }, 1700000100},
		{"signer hash", func(v []string) { v[19] = strings.Repeat("9", 64) }, 1700000100},
		{"validity too long", func(v []string) { v[22] = "1700007200" }, 1700000100},
		{"clock floor after not before", func(v []string) { v[23] = "1700000001" }, 1700000100},
		{"expired end boundary", nil, 1700000300},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := fixture.descriptor(t, test.mutate)
			if result, err := ParseAndVerify(data, fixture.roots, "amd64", fixture.host, time.Unix(test.now, 0)); err == nil || result.AttemptID != "" {
				t.Fatalf("accepted drift: result=%#v err=%v", result, err)
			}
		})
	}
	data := fixture.descriptor(t, nil)
	data = append([]byte(nil), data...)
	data[len(data)-1] = '\r'
	if _, err := ParseAndVerify(data, fixture.roots, "amd64", fixture.host, time.Unix(1700000100, 0)); err == nil {
		t.Fatal("accepted CR envelope")
	}
	wrongHost := sha256.Sum256([]byte("wrong-host"))
	if _, err := ParseAndVerify(fixture.descriptor(t, nil), fixture.roots, "amd64", wrongHost, time.Unix(1700000100, 0)); err == nil {
		t.Fatal("accepted wrong host")
	}
}

func TestParseAndVerifyAuthorityDescriptorRejectsCallerSelfSigning(t *testing.T) {
	fixture := newAuthorityFixture()
	attacker := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{99}, ed25519.SeedSize))
	data := fixture.descriptorWithSigners(t, nil, []signingKey{
		{id: "root-a", private: fixture.private[0]},
		{id: "root-b", private: attacker},
	})
	if _, err := ParseAndVerify(data, fixture.roots, "amd64", fixture.host, time.Unix(1700000100, 0)); err == nil {
		t.Fatal("accepted caller-controlled self-signing key")
	}
	data = fixture.descriptor(t, nil)
	data = bytes.Replace(data, []byte("root_signature_1_key_id=root-a"), []byte("root_signature_1_key_id=root-b"), 1)
	data = bytes.Replace(data, []byte("root_signature_2_key_id=root-b"), []byte("root_signature_2_key_id=root-a"), 1)
	if _, err := ParseAndVerify(data, fixture.roots, "amd64", fixture.host, time.Unix(1700000100, 0)); err == nil {
		t.Fatal("accepted unsorted root signatures")
	}
}

func TestParseAndVerifyAuthorityDescriptorRequiresKeySeparation(t *testing.T) {
	fixture := newAuthorityFixture()
	duplicateRoots := fixture.roots
	duplicateRoots.Keys = append([]RootKey(nil), fixture.roots.Keys...)
	duplicateRoots.Keys[1].PublicKey = duplicateRoots.Keys[0].PublicKey
	duplicatedSignature := fixture.descriptorWithSigners(t, nil, []signingKey{
		{id: "root-a", private: fixture.private[0]},
		{id: "root-b", private: fixture.private[0]},
	})
	if _, err := ParseAndVerify(duplicatedSignature, duplicateRoots, "amd64", fixture.host, time.Unix(1700000100, 0)); err == nil {
		t.Fatal("accepted duplicate compiled root public keys")
	}
	colliding := fixture
	colliding.releasePublic = fixture.roots.Keys[0].PublicKey
	if _, err := ParseAndVerify(colliding.descriptor(t, nil), colliding.roots, "amd64", colliding.host, time.Unix(1700000100, 0)); err == nil {
		t.Fatal("accepted release signer that reuses an authority root key")
	}
}

type authorityFixture struct {
	roots         RootKeyset
	private       []ed25519.PrivateKey
	releasePublic ed25519.PublicKey
	host          [sha256.Size]byte
}

type signingKey struct {
	id      string
	private ed25519.PrivateKey
}

func newAuthorityFixture() authorityFixture {
	rootKeys := make([]RootKey, 3)
	privateKeys := make([]ed25519.PrivateKey, 3)
	for i := range rootKeys {
		privateKeys[i] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(i + 1)}, ed25519.SeedSize))
		rootKeys[i] = RootKey{ID: "root-" + string(rune('a'+i)), PublicKey: privateKeys[i].Public().(ed25519.PublicKey)}
	}
	releasePrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{44}, ed25519.SeedSize))
	return authorityFixture{
		roots:   RootKeyset{ID: "pandora-ca42-roots-v1", Quorum: 2, Keys: rootKeys},
		private: privateKeys, releasePublic: releasePrivate.Public().(ed25519.PublicKey),
		host: sha256.Sum256([]byte("pandora-test-host")),
	}
}

func (f authorityFixture) descriptor(t *testing.T, mutate func([]string)) []byte {
	t.Helper()
	return f.descriptorWithSigners(t, mutate, []signingKey{
		{id: "root-a", private: f.private[0]},
		{id: "root-b", private: f.private[1]},
	})
}

func (f authorityFixture) descriptorWithSigners(t *testing.T, mutate func([]string), signers []signingKey) []byte {
	t.Helper()
	signerDigest := sha256.Sum256(f.releasePublic)
	values := []string{
		Format, SignatureAlgorithm, SignatureDomain, Purpose, Status,
		f.roots.ID, strings.Repeat("a", 64), "1", "1", strings.Repeat("0", 64), ModeNormal,
		"amd64", hex.EncodeToString(f.host[:]), "release-ca42-1", "run-ca42-1", "attempt-ca42-1",
		strings.Repeat("b", 64), "release-signer-1", base64.StdEncoding.EncodeToString(f.releasePublic),
		hex.EncodeToString(signerDigest[:]), strings.Repeat("c", 64), "1700000000", "1700000300", "1699999999",
	}
	if mutate != nil {
		mutate(values)
	}
	keyIDs := [RequiredQuorum]string{signers[0].id, signers[1].id}
	signed, err := SignatureInput(values, keyIDs)
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i, value := range values {
		body.WriteString(fieldNames[i])
		body.WriteByte('=')
		body.WriteString(value)
		body.WriteByte('\n')
	}
	for index, signer := range signers {
		body.WriteString("root_signature_")
		body.WriteString(string(rune('1' + index)))
		body.WriteString("_key_id=")
		body.WriteString(signer.id)
		body.WriteByte('\n')
		body.WriteString("root_signature_")
		body.WriteString(string(rune('1' + index)))
		body.WriteString("_b64=")
		body.WriteString(base64.StdEncoding.EncodeToString(ed25519.Sign(signer.private, signed)))
		body.WriteByte('\n')
	}
	return []byte(body.String())
}
