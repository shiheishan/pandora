package clientauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStrictEncodingPrimitives(t *testing.T) {
	_ = loadSharedVectors(t)
	valid := EncodeBase64URLNoPad(make([]byte, 32))
	if decoded, err := DecodeBase64URLNoPad(valid, 32); err != nil || len(decoded) != 32 {
		t.Fatalf("valid base64url failed: %v", err)
	}
	for _, bad := range []string{"", valid + "=", "AA+_", "AB", "é"} {
		if _, err := DecodeBase64URLNoPad(bad, -1); err == nil {
			t.Fatalf("accepted non-canonical base64url %q", bad)
		}
	}
	for _, good := range []string{"0", "1785398400"} {
		if _, err := ParseUnixSeconds(good); err != nil {
			t.Fatalf("unix seconds %q: %v", good, err)
		}
	}
	for _, bad := range []string{"", "00", "+1", "-1", "1.0", " 1"} {
		if _, err := ParseUnixSeconds(bad); err == nil {
			t.Fatalf("accepted unix seconds %q", bad)
		}
	}
	if _, err := ParseCanonicalUUID("00112233-4455-6677-8899-aabbccddeeff"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalUUIDv7("01981f36-4c00-7abc-8def-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"00112233-4455-6677-8899-AABBCCDDEEFF", "{00112233-4455-6677-8899-aabbccddeeff}", "00112233445566778899aabbccddeeff", "00112233-4455-6677-0899-aabbccddeeff"} {
		if _, err := ParseCanonicalUUID(bad); err == nil {
			t.Fatalf("accepted UUID %q", bad)
		}
	}
	if _, err := ParseCanonicalUUIDv7("00112233-4455-6677-8899-aabbccddeeff"); err == nil {
		t.Fatal("accepted non-v7 UUID as UUIDv7")
	}
	parsed, err := ParseMicrosecondUTC("2026-07-30T08:00:30.000000Z")
	if err != nil {
		t.Fatal(err)
	}
	if formatted, err := FormatMicrosecondUTC(parsed); err != nil || formatted != "2026-07-30T08:00:30.000000Z" {
		t.Fatalf("microsecond UTC roundtrip = %q, %v", formatted, err)
	}
	for _, bad := range []string{"2026-07-30T08:00:30Z", "2026-07-30T08:00:30.000000+00:00", "2026-07-30T08:00:30.00000Z"} {
		if _, err := ParseMicrosecondUTC(bad); err == nil {
			t.Fatalf("accepted timestamp %q", bad)
		}
	}
	if _, err := FormatMicrosecondUTC(time.Unix(0, 1).UTC()); err == nil {
		t.Fatal("accepted nanosecond time as microsecond precision")
	}
}

func TestCanonicalByteSensitivityAndValidation(t *testing.T) {
	vector := loadDeviceVector(t)
	base := CanonicalRequestV1{Origin: vector.Request.Origin, Method: vector.Request.Method, Target: vector.Request.Target, Timestamp: vector.Request.Timestamp, NonceB64U: vector.Request.NonceB64U, Body: []byte(vector.Request.BodyUTF8), CredentialKind: CredentialDeviceCode, Credential: []byte(vector.Request.CredentialASCII)}
	canonical, err := BuildCanonicalRequestV1(base)
	if err != nil {
		t.Fatal(err)
	}
	changedBody := base
	changedBody.Body = append([]byte(" "), base.Body...)
	changedCanonical, err := BuildCanonicalRequestV1(changedBody)
	if err != nil || bytes.Equal(canonical.Bytes(), changedCanonical.Bytes()) {
		t.Fatal("body whitespace did not change canonical bytes")
	}
	firstQuery := base
	firstQuery.Target = "/v1/example?a=1&b=2"
	secondQuery := base
	secondQuery.Target = "/v1/example?b=2&a=1"
	firstBytes, err1 := BuildCanonicalRequestV1(firstQuery)
	secondBytes, err2 := BuildCanonicalRequestV1(secondQuery)
	if err1 != nil || err2 != nil || bytes.Equal(firstBytes.Bytes(), secondBytes.Bytes()) {
		t.Fatal("query order was normalized")
	}
	for _, mutate := range []func(*CanonicalRequestV1){
		func(value *CanonicalRequestV1) { value.Origin = "HTTPS://client.example.com" },
		func(value *CanonicalRequestV1) { value.Method = "post" },
		func(value *CanonicalRequestV1) { value.Target = "/bad%2" },
		func(value *CanonicalRequestV1) { value.Timestamp = "01785398400" },
		func(value *CanonicalRequestV1) { value.NonceB64U += "=" },
	} {
		candidate := base
		mutate(&candidate)
		if _, err := BuildCanonicalRequestV1(candidate); err == nil {
			t.Fatalf("accepted malformed canonical request %#v", candidate)
		}
	}
	if _, err := NonceRequestHash(CanonicalBytesV1{}); err == nil {
		t.Fatal("nonce hash accepted uninitialized opaque canonical bytes")
	}
}

func TestOriginAndTargetAdversarialBoundaries(t *testing.T) {
	vector := loadDeviceVector(t)
	base := vectorRequest(vector)
	for _, validOrigin := range []string{"https://client.example.com", "https://client.example.com:8443", "https://localhost"} {
		candidate := base
		candidate.Origin = validOrigin
		if _, err := BuildCanonicalRequestV1(candidate); err != nil {
			t.Fatalf("rejected valid canonical origin %q: %v", validOrigin, err)
		}
	}
	for _, invalidOrigin := range []string{
		"https://client.example.com:", "https://127.0.0.1", "https://[::1]", "https://2130706433",
		"https://client.example.com\t", "https://client.example.com\x00", "https://client.example.com\x7f",
		"https://user@client.example.com", "https://client.example.com/", "https://client.example.com?x=1",
		"https://client.example.com#x", "https://client.example.com.", "https://client.example.com:443",
		"https://client.example.com:08443", "https://client_example.com", "https://-client.example.com",
		"HTTPS://client.example.com", "https://CLIENT.example.com",
	} {
		candidate := base
		candidate.Origin = invalidOrigin
		if _, err := BuildCanonicalRequestV1(candidate); err == nil {
			t.Fatalf("accepted invalid origin %q", invalidOrigin)
		}
	}
	for _, validTarget := range []string{"/", "/v1/device/token", "/a%2Fb?b=2&a=1", "/v1/example?x=a+b"} {
		candidate := base
		candidate.Target = validTarget
		if _, err := BuildCanonicalRequestV1(candidate); err != nil {
			t.Fatalf("rejected valid origin-form target %q: %v", validTarget, err)
		}
	}
	for _, invalidTarget := range []string{
		"", "*", "https://client.example.com/v1", "v1/device/token", "/?", "/bad%2", "/bad%GG",
		"/bad path", "/bad#fragment", "/bad\tpath", "/bad\x00path", "/bad\x7fpath", "/bad\\path",
	} {
		candidate := base
		candidate.Target = invalidTarget
		if _, err := BuildCanonicalRequestV1(candidate); err == nil {
			t.Fatalf("accepted invalid target %q", invalidTarget)
		}
	}
}

func TestAESGCMFailClosed(t *testing.T) {
	_ = loadSharedVectors(t)
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	sealed, err := SealAES256GCM(key, nonce, []byte("plaintext"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAES256GCM(key, nonce, sealed.Ciphertext, sealed.Tag, []byte("different")); err == nil {
		t.Fatal("accepted wrong AAD")
	}
	badTag := append([]byte(nil), sealed.Tag...)
	badTag[0] ^= 1
	if _, err := OpenAES256GCM(key, nonce, sealed.Ciphertext, badTag, []byte("aad")); err == nil {
		t.Fatal("accepted wrong tag")
	}
	if _, err := SealAES256GCM(key[:31], nonce, nil, nil); err == nil {
		t.Fatal("accepted non-AES-256 key")
	}
	if _, err := sealRandomAES256GCM(bytes.NewReader(nil), key, nil, nil); err == nil {
		t.Fatal("ignored CSPRNG failure")
	}
}

func TestKeyringStrictLoaderAndSelection(t *testing.T) {
	_ = loadSharedVectors(t)
	jwt, aead := documentKeyrings(t)
	if _, err := LoadKeyrings(jwt, aead); err != nil {
		t.Fatal(err)
	}
	jwtText := string(jwt)
	mutations := []string{
		strings.Replace(jwtText, `"kid":`, `"kid":"duplicate","kid":`, 1),
		strings.Replace(jwtText, `"secret_b64u"`, `"unknown"`, 1),
		strings.Replace(jwtText, `"HS256"`, `"hs256"`, 1),
		strings.Replace(jwtText, `"active"`, `"retired"`, 1),
		strings.Replace(jwtText, `"secret_b64u": "`, `"secret_b64u": "=`, 1),
	}
	for _, malformed := range mutations {
		if _, err := LoadJWTKeyring([]byte(malformed)); err == nil {
			t.Fatalf("accepted malformed JWT keyring %s", malformed)
		}
	}
	var jwtObject []map[string]any
	var aeadObject []map[string]any
	if err := json.Unmarshal(jwt, &jwtObject); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(aead, &aeadObject); err != nil {
		t.Fatal(err)
	}
	aeadObject[0]["key_b64u"] = jwtObject[0]["secret_b64u"]
	duplicateMaterial, err := json.Marshal(aeadObject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyrings(jwt, duplicateMaterial); err == nil {
		t.Fatal("accepted cross-ring decoded material reuse")
	}

	verifyJWT := makeJWTEntry("old", 1, "verify-only", 0x40, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:01:00.000000Z", ptr("2026-07-30T08:02:00.000000Z"), nil)
	activeJWT := makeJWTEntry("new", 2, "active", 0x60, "2026-07-30T08:03:00.000000Z", "2026-07-30T08:03:00.000000Z", nil, nil)
	multi, _ := json.Marshal([]map[string]any{verifyJWT, activeJWT})
	ring, err := LoadJWTKeyring(multi)
	if err != nil {
		t.Fatal(err)
	}
	active, err := ring.Active()
	if err != nil || active.KID != "new" {
		t.Fatalf("active selection = %#v, %v", active, err)
	}
	_, signature, err := ring.SignHS256([]byte("input"))
	if err != nil || ring.VerifyHS256("old", []byte("input"), signature) == nil {
		t.Fatal("JWT lookup fell back from exact kid")
	}
	if ring.VerifyHS256("unknown", []byte("input"), signature) == nil {
		t.Fatal("unknown kid fallback")
	}
	if _, err := (JWTKeyring{}).Active(); err == nil {
		t.Fatal("zero keyring Active panicked or succeeded")
	}
}

func TestNilKeyringsFailClosed(t *testing.T) {
	_ = loadSharedVectors(t)
	var rings *Keyrings
	if _, err := rings.EpochB64U(); err == nil {
		t.Fatal("nil keyrings returned an epoch")
	}
	if err := rings.SelfTest(); err == nil {
		t.Fatal("nil keyrings passed self-test")
	}
}

func TestKeyringLifecycleTransition(t *testing.T) {
	_ = loadSharedVectors(t)
	previousJWT, _ := json.Marshal([]map[string]any{
		makeJWTEntry("old", 1, "active", 0x40, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:00:00.000000Z", nil, nil),
	})
	previousAEAD := mustSingleAEAD(t, "aead_old", 1, "active", 0x80, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:00:00.000000Z", nil, nil)
	previous, err := LoadKeyrings(previousJWT, previousAEAD)
	if err != nil {
		t.Fatal(err)
	}
	nextJWT, _ := json.Marshal([]map[string]any{
		makeJWTEntry("old", 1, "verify-only", 0x40, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:00:00.000000Z", ptr("2026-07-30T08:01:00.000000Z"), nil),
		makeJWTEntry("new", 2, "active", 0x60, "2026-07-30T08:02:00.000000Z", "2026-07-30T08:02:00.000000Z", nil, nil),
	})
	nextAEAD, _ := json.Marshal([]map[string]any{
		makeAEADEntry("aead_old", 1, "verify-only", 0x80, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:00:00.000000Z", ptr("2026-07-30T08:01:00.000000Z"), nil),
		makeAEADEntry("aead_new", 2, "active", 0xa0, "2026-07-30T08:02:00.000000Z", "2026-07-30T08:02:00.000000Z", nil, nil),
	})
	next, err := LoadKeyrings(nextJWT, nextAEAD)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyringTransition(previous, next); err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(nextJWT, []byte(EncodeBase64URLNoPad(bytes.Repeat([]byte{0x40}, 32))), []byte(EncodeBase64URLNoPad(bytes.Repeat([]byte{0x41}, 32))), 1)
	tamperedNext, err := LoadKeyrings(tampered, nextAEAD)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyringTransition(previous, tamperedNext); err == nil {
		t.Fatal("accepted key material rewrite")
	}
	if err := ValidateKeyringTransition(next, previous); err == nil {
		t.Fatal("accepted reverse lifecycle transition")
	}
	if err := ValidateKeyringTransition(previous, previous); err == nil {
		t.Fatal("accepted transition without exactly one appended active")
	}

	removedAndReusedJWT, _ := json.Marshal([]map[string]any{
		makeJWTEntry("replacement", 2, "active", 0x40, "2026-07-30T08:02:00.000000Z", "2026-07-30T08:02:00.000000Z", nil, nil),
	})
	removedAndReusedAEAD := mustSingleAEAD(t, "aead_replacement", 2, "active", 0x80, "2026-07-30T08:02:00.000000Z", "2026-07-30T08:02:00.000000Z", nil, nil)
	removedAndReused, err := LoadKeyrings(removedAndReusedJWT, removedAndReusedAEAD)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyringTransition(previous, removedAndReused); err == nil {
		t.Fatal("accepted removal and historical material reuse")
	}

	directRetiredJWT, _ := json.Marshal([]map[string]any{
		makeJWTEntry("inserted_retired", 1, "retired", 0x20, "2026-07-30T07:00:00.000000Z", "2026-07-30T07:00:00.000000Z", ptr("2026-07-30T07:01:00.000000Z"), ptr("2026-07-30T07:02:00.000000Z")),
		makeJWTEntry("old", 2, "active", 0x40, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:00:00.000000Z", nil, nil),
	})
	directRetiredAEAD, _ := json.Marshal([]map[string]any{
		makeAEADEntry("inserted_aead_retired", 1, "retired", 0x70, "2026-07-30T07:00:00.000000Z", "2026-07-30T07:00:00.000000Z", ptr("2026-07-30T07:01:00.000000Z"), ptr("2026-07-30T07:02:00.000000Z")),
		makeAEADEntry("aead_old", 2, "active", 0x80, "2026-07-30T08:00:00.000000Z", "2026-07-30T08:00:00.000000Z", nil, nil),
	})
	directRetired, err := LoadKeyrings(directRetiredJWT, directRetiredAEAD)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateKeyringTransition(previous, directRetired); err == nil {
		t.Fatal("accepted a directly inserted retired key")
	}
}

func documentKeyrings(t *testing.T) ([]byte, []byte) {
	t.Helper()
	var vector struct {
		JWT  json.RawMessage `json:"jwt_keyring"`
		AEAD json.RawMessage `json:"aead_keyring"`
	}
	readDocumentVector(t, "client-keyring-issuance-replay-v1-01", &vector)
	return vector.JWT, vector.AEAD
}

func makeJWTEntry(kid string, version int, status string, material byte, created, activated string, verify, retired *string) map[string]any {
	return map[string]any{"kid": kid, "version": version, "status": status, "alg": "HS256", "purpose": "client-jwt-signing", "created_at": created, "activated_at": activated, "verify_only_since": verify, "retired_at": retired, "secret_b64u": EncodeBase64URLNoPad(bytes.Repeat([]byte{material}, 32))}
}

func makeAEADEntry(kid string, version int, status string, material byte, created, activated string, verify, retired *string) map[string]any {
	return map[string]any{"kid": kid, "version": version, "status": status, "alg": "A256GCM", "purpose": "client-replay-envelope", "created_at": created, "activated_at": activated, "verify_only_since": verify, "retired_at": retired, "key_b64u": EncodeBase64URLNoPad(bytes.Repeat([]byte{material}, 32))}
}

func mustSingleAEAD(t *testing.T, kid string, version int, status string, material byte, created, activated string, verify, retired *string) []byte {
	t.Helper()
	encoded, err := json.Marshal([]map[string]any{makeAEADEntry(kid, version, status, material, created, activated, verify, retired)})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func ptr(value string) *string { return &value }

func TestHashDomainsAreDistinct(t *testing.T) {
	vector := loadDeviceVector(t)
	canonical, err := buildVectorCanonical(vector)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := NonceRequestHash(canonical)
	if err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256(canonical.Bytes())
	if nonce == plain {
		t.Fatal("nonce request hash lacks domain separation")
	}
}

func TestCanonicalBytesAreOpaqueAndDefensivelyCopied(t *testing.T) {
	vector := loadDeviceVector(t)
	canonical, err := buildVectorCanonical(vector)
	if err != nil {
		t.Fatal(err)
	}
	copyOne := canonical.Bytes()
	copyOne[0] ^= 0xff
	copyTwo := canonical.Bytes()
	if copyTwo[0] != 'A' {
		t.Fatal("caller mutation changed opaque canonical bytes")
	}
	if _, err := NonceRequestHash(canonical); err != nil {
		t.Fatal(err)
	}
}
