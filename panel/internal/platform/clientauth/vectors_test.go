package clientauth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const sharedVectorSchemaVersion = "aegis-device-pop-v1-vectors-1"

var (
	frozenPositiveVectorIDs = []string{
		"device-pop-v1-01",
	}
	frozenPositiveSignatureAlgorithms = map[string][]string{
		"device-pop-v1-01": {AlgorithmP256ES256, AlgorithmEd25519},
	}
	frozenAuthorizationNegativeVectorIDs = []string{
		"authorization-missing-v1",
		"authorization-duplicate-v1",
		"authorization-comma-joined-v1",
		"authorization-bearer-v1",
		"authorization-double-space-v1",
		"authorization-wrong-case-v1",
		"authorization-forbidden-present-v1",
	}
	frozenNegativeVectorIDs = []string{
		"canonical-crlf-v1",
		"canonical-missing-final-lf-v1",
		"canonical-body-whitespace-v1",
		"canonical-query-reorder-v1",
		"p256-der-v1",
		"p256-high-s-v1",
		"ed25519-prehash-v1",
		"nonce-domain-separation-v1",
	}
)

type sharedVectorFile struct {
	SchemaVersion   string              `json:"schema_version"`
	Vectors         []deviceVector      `json:"vectors"`
	Authorization   authorizationVector `json:"authorization"`
	NegativeVectors []negativeVector    `json:"negative_vectors"`
}

type deviceVector struct {
	VectorID             string `json:"vector_id"`
	BodySHA256B64U       string `json:"body_sha256_b64u"`
	ATHB64U              string `json:"ath_b64u"`
	CanonicalUTF8        string `json:"canonical_utf8"`
	CanonicalSHA256B64U  string `json:"canonical_sha256_b64u"`
	NonceRequestHashB64U string `json:"nonce_request_hash_b64u"`
	Request              struct {
		Origin          string `json:"origin"`
		Method          string `json:"method"`
		Target          string `json:"target"`
		Timestamp       string `json:"timestamp"`
		NonceB64U       string `json:"nonce_b64u"`
		BodyUTF8        string `json:"body_utf8"`
		CredentialKind  string `json:"credential_kind"`
		CredentialASCII string `json:"credential_ascii"`
	} `json:"request"`
	Signatures []struct {
		Algorithm         string `json:"algorithm"`
		PublicKeySPKIB64U string `json:"public_key_spki_b64u"`
		SignatureB64U     string `json:"signature_b64u"`
	} `json:"signatures"`
}

type authorizationVector struct {
	VectorID        string                      `json:"vector_id"`
	ValidFieldLines []string                    `json:"valid_field_lines"`
	TokenASCII      string                      `json:"token_ascii"`
	ATHB64U         string                      `json:"ath_b64u"`
	NegativeCases   []authorizationNegativeCase `json:"negative_cases"`
}

type authorizationNegativeCase struct {
	VectorID   string   `json:"vector_id"`
	Mode       string   `json:"mode"`
	FieldLines []string `json:"field_lines"`
}

type negativeVector struct {
	VectorID      string `json:"vector_id"`
	Kind          string `json:"kind"`
	Mutation      string `json:"mutation,omitempty"`
	FirstTarget   string `json:"first_target,omitempty"`
	SecondTarget  string `json:"second_target,omitempty"`
	Algorithm     string `json:"algorithm,omitempty"`
	SignatureB64U string `json:"signature_b64u,omitempty"`
}

func TestSharedVectorSchemaIsStrictAndUnique(t *testing.T) {
	_ = loadSharedVectors(t)
	encoded, err := os.ReadFile(filepath.Join("testdata", "device_pop_v1_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(encoded, []byte(`"schema_version":`), []byte(`"schema_version":"duplicate","schema_version":`), 1)
	if _, err := decodeSharedVectors(duplicate); err == nil {
		t.Fatal("shared vector decoder accepted duplicate JSON key")
	}
	unknown := bytes.Replace(encoded, []byte(`"schema_version"`), []byte(`"unknown_schema_field"`), 1)
	if _, err := decodeSharedVectors(unknown); err == nil {
		t.Fatal("shared vector decoder accepted unknown JSON key")
	}
	wrongVersion := bytes.Replace(encoded, []byte(sharedVectorSchemaVersion), []byte("aegis-device-pop-v1-vectors-2"), 1)
	if _, err := decodeSharedVectors(wrongVersion); err == nil {
		t.Fatal("shared vector decoder accepted wrong schema version")
	}
}

func TestSharedVectorInventoryRejectsRemovalOfRequiredSemantics(t *testing.T) {
	for _, vectorID := range frozenPositiveVectorIDs {
		vectorID := vectorID
		t.Run("positive-vector/"+vectorID, func(t *testing.T) {
			shared := loadSharedVectors(t)
			shared.Vectors = filterDeviceVectors(shared.Vectors, vectorID)
			requireSharedVectorDecodeFailure(t, shared)
		})
	}

	for vectorID, algorithms := range frozenPositiveSignatureAlgorithms {
		vectorID, algorithms := vectorID, algorithms
		for _, algorithm := range algorithms {
			algorithm := algorithm
			t.Run("positive-signature/"+vectorID+"/"+algorithm, func(t *testing.T) {
				shared := loadSharedVectors(t)
				for index := range shared.Vectors {
					if shared.Vectors[index].VectorID == vectorID {
						shared.Vectors[index].Signatures = filterSignatures(shared.Vectors[index].Signatures, algorithm)
					}
				}
				requireSharedVectorDecodeFailure(t, shared)
			})
		}
	}

	for _, vectorID := range frozenAuthorizationNegativeVectorIDs {
		vectorID := vectorID
		t.Run("authorization-negative/"+vectorID, func(t *testing.T) {
			shared := loadSharedVectors(t)
			shared.Authorization.NegativeCases = filterAuthorizationNegativeCases(shared.Authorization.NegativeCases, vectorID)
			requireSharedVectorDecodeFailure(t, shared)
		})
	}

	for _, vectorID := range frozenNegativeVectorIDs {
		vectorID := vectorID
		t.Run("negative/"+vectorID, func(t *testing.T) {
			shared := loadSharedVectors(t)
			shared.NegativeVectors = filterNegativeVectors(shared.NegativeVectors, vectorID)
			requireSharedVectorDecodeFailure(t, shared)
		})
	}
}

func TestDevicePoPGoldenVector(t *testing.T) {
	vector := loadDeviceVector(t)
	canonical, err := buildVectorCanonical(vector)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBytes := canonical.Bytes()
	if string(canonicalBytes) != vector.CanonicalUTF8 {
		t.Fatalf("canonical mismatch\ngot:  %q\nwant: %q", canonicalBytes, vector.CanonicalUTF8)
	}
	digest := sha256.Sum256(canonicalBytes)
	if got := EncodeBase64URLNoPad(digest[:]); got != vector.CanonicalSHA256B64U {
		t.Fatalf("canonical digest = %s", got)
	}
	bodyDigest := sha256.Sum256([]byte(vector.Request.BodyUTF8))
	if got := EncodeBase64URLNoPad(bodyDigest[:]); got != vector.BodySHA256B64U {
		t.Fatalf("body digest = %s", got)
	}
	ath := sha256.Sum256([]byte(vector.Request.CredentialASCII))
	if got := EncodeBase64URLNoPad(ath[:]); got != vector.ATHB64U {
		t.Fatalf("ath = %s", got)
	}
	nonceHash, err := NonceRequestHash(canonical)
	if err != nil || EncodeBase64URLNoPad(nonceHash[:]) != vector.NonceRequestHashB64U {
		t.Fatalf("nonce request hash = %s, err %v", EncodeBase64URLNoPad(nonceHash[:]), err)
	}

	document := readContract(t)
	for _, expected := range []string{vector.BodySHA256B64U, vector.ATHB64U, vector.CanonicalSHA256B64U, vector.NonceRequestHashB64U, vector.CanonicalUTF8} {
		if !strings.Contains(document, expected) {
			t.Fatalf("shared vector value is absent from frozen contract: %q", expected)
		}
	}
	for _, signatureVector := range vector.Signatures {
		spki := mustDecode(t, signatureVector.PublicKeySPKIB64U, -1)
		signature := mustDecode(t, signatureVector.SignatureB64U, 64)
		if err := VerifyProof(signatureVector.Algorithm, spki, canonicalBytes, signature); err != nil {
			t.Fatalf("%s vector: %v", signatureVector.Algorithm, err)
		}
	}
}

func TestSharedFrozenNegativeVectors(t *testing.T) {
	shared := loadSharedVectors(t)
	vector := shared.Vectors[0]
	canonical, err := buildVectorCanonical(vector)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBytes := canonical.Bytes()
	p256 := findSignature(t, vector, AlgorithmP256ES256)
	ed := findSignature(t, vector, AlgorithmEd25519)
	for _, negative := range shared.NegativeVectors {
		switch negative.Kind {
		case "canonical-bytes":
			mutated := append([]byte(nil), canonicalBytes...)
			switch negative.Mutation {
			case "lf-to-crlf":
				mutated = bytes.ReplaceAll(mutated, []byte("\n"), []byte("\r\n"))
			case "remove-final-lf":
				mutated = mutated[:len(mutated)-1]
			default:
				t.Fatalf("unknown canonical mutation %q", negative.Mutation)
			}
			for _, signature := range vector.Signatures {
				if err := VerifyProof(signature.Algorithm, mustDecode(t, signature.PublicKeySPKIB64U, -1), mutated, mustDecode(t, signature.SignatureB64U, 64)); err == nil {
					t.Fatalf("%s accepted %s", signature.Algorithm, negative.VectorID)
				}
			}
		case "request":
			request := vectorRequest(vector)
			if negative.Mutation != "prepend-body-space" {
				t.Fatalf("unknown request mutation %q", negative.Mutation)
			}
			request.Body = append([]byte(" "), request.Body...)
			changed, err := BuildCanonicalRequestV1(request)
			if err != nil || bytes.Equal(changed.Bytes(), canonicalBytes) {
				t.Fatalf("%s did not change canonical bytes", negative.VectorID)
			}
		case "request-pair":
			first := vectorRequest(vector)
			second := vectorRequest(vector)
			first.Target, second.Target = negative.FirstTarget, negative.SecondTarget
			firstBytes, firstErr := BuildCanonicalRequestV1(first)
			secondBytes, secondErr := BuildCanonicalRequestV1(second)
			if firstErr != nil || secondErr != nil || bytes.Equal(firstBytes.Bytes(), secondBytes.Bytes()) {
				t.Fatalf("%s was normalized", negative.VectorID)
			}
		case "signature":
			if negative.Algorithm != AlgorithmP256ES256 {
				t.Fatalf("unsupported signature negative algorithm %q", negative.Algorithm)
			}
			if err := VerifyProof(negative.Algorithm, mustDecode(t, p256.PublicKeySPKIB64U, -1), canonicalBytes, mustDecode(t, negative.SignatureB64U, -1)); err == nil {
				t.Fatalf("accepted %s", negative.VectorID)
			}
		case "signature-message":
			if negative.Algorithm != AlgorithmEd25519 || negative.Mutation != "sha256-message" {
				t.Fatalf("invalid signature-message vector %q", negative.VectorID)
			}
			digest := sha256.Sum256(canonicalBytes)
			if err := VerifyProof(AlgorithmEd25519, mustDecode(t, ed.PublicKeySPKIB64U, -1), digest[:], mustDecode(t, ed.SignatureB64U, 64)); err == nil {
				t.Fatalf("accepted Ed25519 prehash substitution")
			}
		case "domain-separation":
			if negative.Mutation != "omit-domain-and-nul" {
				t.Fatalf("invalid domain mutation %q", negative.Mutation)
			}
			nonce, err := NonceRequestHash(canonical)
			plain := sha256.Sum256(canonicalBytes)
			if err != nil || nonce == plain {
				t.Fatalf("nonce domain separation failed: %v", err)
			}
		default:
			t.Fatalf("unknown negative vector kind %q", negative.Kind)
		}
	}
}

func TestP256RejectsWrongCurve(t *testing.T) {
	vector := loadDeviceVector(t)
	p256 := findSignature(t, vector, AlgorithmP256ES256)
	wrongKey, err := x509.MarshalPKIXPublicKey(&ecdsa.PublicKey{Curve: elliptic.P384(), X: elliptic.P384().Params().Gx, Y: elliptic.P384().Params().Gy})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyProof(AlgorithmP256ES256, wrongKey, []byte(vector.CanonicalUTF8), mustDecode(t, p256.SignatureB64U, 64)); err == nil {
		t.Fatal("accepted wrong curve")
	}
}

func TestAuthorizationDocumentVectorAndCardinality(t *testing.T) {
	shared := loadSharedVectors(t)
	vector := shared.Authorization
	var documentVector struct {
		Authorization string   `json:"authorization_field_value_ascii"`
		Token         string   `json:"token_ascii"`
		ATH           string   `json:"ath_b64u"`
		Negative      []string `json:"negative_field_values"`
	}
	readDocumentVector(t, vector.VectorID, &documentVector)
	if len(vector.ValidFieldLines) != 1 || vector.ValidFieldLines[0] != documentVector.Authorization || vector.TokenASCII != documentVector.Token || vector.ATHB64U != documentVector.ATH {
		t.Fatal("shared Authorization vector diverges from frozen document")
	}
	token, err := ParseAegisPoPAuthorization(vector.ValidFieldLines)
	if err != nil || string(token) != vector.TokenASCII {
		t.Fatalf("authorization parse = %q, %v", token, err)
	}
	hash, err := AuthorizationTokenHash(token)
	if err != nil || EncodeBase64URLNoPad(hash[:]) != vector.ATHB64U {
		t.Fatalf("authorization ath = %s, %v", EncodeBase64URLNoPad(hash[:]), err)
	}
	for _, negative := range vector.NegativeCases {
		switch negative.Mode {
		case "required":
			if _, err := ParseAegisPoPAuthorization(negative.FieldLines); err == nil {
				t.Fatalf("accepted %s", negative.VectorID)
			}
		case "forbidden":
			if err := RejectAuthorization(negative.FieldLines); err == nil {
				t.Fatalf("accepted %s", negative.VectorID)
			}
		default:
			t.Fatalf("unknown authorization mode %q", negative.Mode)
		}
	}
	if err := RejectAuthorization(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshReplayDocumentVector(t *testing.T) {
	_ = loadSharedVectors(t)
	var vector struct {
		RefreshToken string `json:"refresh_token_ascii"`
		RequestID    string `json:"refresh_request_id"`
		KeyID        string `json:"key_id_b64u"`
		Body         string `json:"body_utf8"`
		BodyHash     string `json:"body_sha256_b64u"`
		ATH          string `json:"ath_b64u"`
		Record       string `json:"stable_record_utf8"`
		Hash         string `json:"replay_request_hash_b64u"`
	}
	readDocumentVector(t, "refresh-replay-v1-01", &vector)
	bodyHash := sha256.Sum256([]byte(vector.Body))
	ath := sha256.Sum256([]byte(vector.RefreshToken))
	if EncodeBase64URLNoPad(bodyHash[:]) != vector.BodyHash || EncodeBase64URLNoPad(ath[:]) != vector.ATH {
		t.Fatal("refresh body/credential digest mismatch")
	}
	record := RefreshReplayRecord{Origin: "https://client.example.com", Method: "POST", Target: "/v1/auth/refresh", BodySHA256B64U: vector.BodyHash, RefreshATHB64U: vector.ATH, KeyIDB64U: vector.KeyID, RefreshRequestID: vector.RequestID}
	encoded, err := BuildRefreshReplayRecord(record)
	if err != nil || string(encoded) != vector.Record {
		t.Fatalf("refresh record mismatch: %v", err)
	}
	hash, err := RefreshReplayRequestHash(record)
	if err != nil || EncodeBase64URLNoPad(hash[:]) != vector.Hash {
		t.Fatalf("refresh hash = %s, %v", EncodeBase64URLNoPad(hash[:]), err)
	}
}

func TestKeyringIssuanceAndAEADDocumentVector(t *testing.T) {
	_ = loadSharedVectors(t)
	var vector struct {
		JWTKeyring       json.RawMessage `json:"jwt_keyring"`
		JWTHeader        string          `json:"jwt_header_utf8"`
		JWTPayload       string          `json:"jwt_payload_utf8"`
		JWTCompact       string          `json:"jwt_compact"`
		AEADKeyring      json.RawMessage `json:"aead_keyring"`
		IssuanceFields   []string        `json:"issuance_stable_fields"`
		StableHash       string          `json:"stable_request_hash_b64u"`
		AAD              string          `json:"aad_utf8"`
		Plaintext        string          `json:"plaintext_envelope_utf8"`
		Nonce            string          `json:"aead_nonce_b64u"`
		Ciphertext       string          `json:"ciphertext_b64u"`
		Tag              string          `json:"tag_b64u"`
		ResponseBody     string          `json:"response_body_utf8"`
		ResponseBodyHash string          `json:"response_body_sha256_b64u"`
	}
	readDocumentVector(t, "client-keyring-issuance-replay-v1-01", &vector)
	if len(vector.IssuanceFields) != 6 {
		t.Fatal("invalid issuance vector field count")
	}
	fields := IssuanceStableFields{Origin: vector.IssuanceFields[0], Method: vector.IssuanceFields[1], Target: vector.IssuanceFields[2], RequestBodySHA256B64U: vector.IssuanceFields[3], DeviceCodeATHB64U: vector.IssuanceFields[4], KeyFingerprintSHA256B64U: vector.IssuanceFields[5]}
	stableHash, err := IssuanceStableRequestHash(fields)
	if err != nil || EncodeBase64URLNoPad(stableHash[:]) != vector.StableHash {
		t.Fatalf("issuance stable hash = %s, %v", EncodeBase64URLNoPad(stableHash[:]), err)
	}
	aad, err := BuildReplayAAD(ReplayAAD{Kind: ReplayIssuance, TenantID: "00112233-4455-6677-8899-aabbccddeeff", ReplayID: "01981f36-4c00-7abc-8def-0123456789ab", StableRequestHashB64U: vector.StableHash, KeyFingerprintSHA256B64U: vector.IssuanceFields[5], ExpiresAtMicrosecondUTC: "2026-07-30T08:00:30.000000Z"})
	if err != nil || string(aad) != vector.AAD {
		t.Fatalf("AAD mismatch: %v", err)
	}
	rings, err := LoadKeyrings(vector.JWTKeyring, vector.AEADKeyring)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(vector.JWTHeader))
	payload := base64.RawURLEncoding.EncodeToString([]byte(vector.JWTPayload))
	input := []byte(header + "." + payload)
	jwtMetadata, signature, err := rings.JWT.SignHS256(input)
	if err != nil {
		t.Fatal(err)
	}
	compact := string(input) + "." + EncodeBase64URLNoPad(signature)
	if compact != vector.JWTCompact || jwtMetadata.KID != "cljwt_00000001" {
		t.Fatalf("JWT vector mismatch: %s", compact)
	}
	if err := rings.JWT.VerifyHS256(jwtMetadata.KID, input, signature); err != nil {
		t.Fatal(err)
	}
	nonce := mustDecode(t, vector.Nonce, 12)
	aeadMetadata, sealed, err := rings.AEAD.SealWithNonce(nonce, []byte(vector.Plaintext), aad)
	if err != nil {
		t.Fatal(err)
	}
	if EncodeBase64URLNoPad(sealed.Ciphertext) != vector.Ciphertext || EncodeBase64URLNoPad(sealed.Tag) != vector.Tag {
		t.Fatal("AES-GCM vector mismatch")
	}
	opened, err := rings.AEAD.Open(aeadMetadata.KID, aeadMetadata.Version, sealed.Nonce, sealed.Ciphertext, sealed.Tag, aad)
	if err != nil || string(opened) != vector.Plaintext {
		t.Fatalf("AES-GCM open mismatch: %v", err)
	}
	bodyHash := sha256.Sum256([]byte(vector.ResponseBody))
	if EncodeBase64URLNoPad(bodyHash[:]) != vector.ResponseBodyHash {
		t.Fatal("response body hash mismatch")
	}
	if err := rings.SelfTest(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringEpochUsesExactBytes(t *testing.T) {
	_ = loadSharedVectors(t)
	jwt := []byte("[ ]")
	aead := []byte("[\n]")
	first := KeyringEpoch(jwt, aead)
	second := KeyringEpoch([]byte("[]"), aead)
	if hmac.Equal(first[:], second[:]) {
		t.Fatal("epoch ignored exact JSON whitespace bytes")
	}
}

func buildVectorCanonical(vector deviceVector) (CanonicalBytesV1, error) {
	return BuildCanonicalRequestV1(vectorRequest(vector))
}

func vectorRequest(vector deviceVector) CanonicalRequestV1 {
	return CanonicalRequestV1{Origin: vector.Request.Origin, Method: vector.Request.Method, Target: vector.Request.Target, Timestamp: vector.Request.Timestamp, NonceB64U: vector.Request.NonceB64U, Body: []byte(vector.Request.BodyUTF8), CredentialKind: CredentialKind(vector.Request.CredentialKind), Credential: []byte(vector.Request.CredentialASCII)}
}

func findSignature(t *testing.T, vector deviceVector, algorithm string) struct {
	Algorithm         string `json:"algorithm"`
	PublicKeySPKIB64U string `json:"public_key_spki_b64u"`
	SignatureB64U     string `json:"signature_b64u"`
} {
	t.Helper()
	for _, signature := range vector.Signatures {
		if signature.Algorithm == algorithm {
			return signature
		}
	}
	t.Fatalf("signature vector %q not found", algorithm)
	panic("unreachable")
}

func loadDeviceVector(t *testing.T) deviceVector {
	t.Helper()
	return loadSharedVectors(t).Vectors[0]
}

func loadSharedVectors(t *testing.T) sharedVectorFile {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("testdata", "device_pop_v1_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := decodeSharedVectors(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return vectors
}

func decodeSharedVectors(encoded []byte) (sharedVectorFile, error) {
	if err := rejectDuplicateJSONKeys(encoded); err != nil {
		return sharedVectorFile{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var shared sharedVectorFile
	if err := decoder.Decode(&shared); err != nil {
		return sharedVectorFile{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return sharedVectorFile{}, fmt.Errorf("trailing shared vector JSON")
	}
	if shared.SchemaVersion != sharedVectorSchemaVersion || len(shared.Vectors) == 0 || shared.Authorization.VectorID == "" || len(shared.NegativeVectors) == 0 || len(shared.Authorization.NegativeCases) == 0 {
		return sharedVectorFile{}, fmt.Errorf("invalid shared vector schema/version")
	}
	ids := make(map[string]struct{})
	register := func(id string) error {
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`).MatchString(id) {
			return fmt.Errorf("invalid vector_id %q", id)
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("duplicate vector_id %q", id)
		}
		ids[id] = struct{}{}
		return nil
	}
	for _, vector := range shared.Vectors {
		if err := register(vector.VectorID); err != nil {
			return sharedVectorFile{}, err
		}
		if len(vector.Signatures) != 2 || vector.CanonicalUTF8 == "" || vector.Request.Origin == "" {
			return sharedVectorFile{}, fmt.Errorf("positive vector must have exactly two signatures")
		}
		algorithms := map[string]bool{}
		for _, signature := range vector.Signatures {
			if signature.Algorithm != AlgorithmP256ES256 && signature.Algorithm != AlgorithmEd25519 || algorithms[signature.Algorithm] || signature.PublicKeySPKIB64U == "" || signature.SignatureB64U == "" {
				return sharedVectorFile{}, fmt.Errorf("invalid or duplicate positive signature algorithm")
			}
			algorithms[signature.Algorithm] = true
		}
	}
	if err := register(shared.Authorization.VectorID); err != nil {
		return sharedVectorFile{}, err
	}
	for _, vector := range shared.Authorization.NegativeCases {
		if err := register(vector.VectorID); err != nil {
			return sharedVectorFile{}, err
		}
		if vector.Mode != "required" && vector.Mode != "forbidden" {
			return sharedVectorFile{}, fmt.Errorf("invalid Authorization negative mode")
		}
	}
	validKinds := map[string]bool{"canonical-bytes": true, "request": true, "request-pair": true, "signature": true, "signature-message": true, "domain-separation": true}
	for _, vector := range shared.NegativeVectors {
		if err := register(vector.VectorID); err != nil {
			return sharedVectorFile{}, err
		}
		if !validKinds[vector.Kind] {
			return sharedVectorFile{}, fmt.Errorf("invalid negative vector kind %q", vector.Kind)
		}
		if err := validateNegativeVectorShape(vector); err != nil {
			return sharedVectorFile{}, err
		}
	}
	if err := validateFrozenSharedVectorInventory(shared); err != nil {
		return sharedVectorFile{}, err
	}
	return shared, nil
}

func validateFrozenSharedVectorInventory(shared sharedVectorFile) error {
	positiveIDs := make([]string, 0, len(shared.Vectors))
	for _, vector := range shared.Vectors {
		positiveIDs = append(positiveIDs, vector.VectorID)
		expectedAlgorithms, exists := frozenPositiveSignatureAlgorithms[vector.VectorID]
		if !exists {
			return fmt.Errorf("unexpected positive vector_id %q", vector.VectorID)
		}
		algorithms := make([]string, 0, len(vector.Signatures))
		for _, signature := range vector.Signatures {
			algorithms = append(algorithms, signature.Algorithm)
		}
		if err := requireExactStringSet("positive signature algorithms for "+vector.VectorID, algorithms, expectedAlgorithms); err != nil {
			return err
		}
	}
	if err := requireExactStringSet("positive vector IDs", positiveIDs, frozenPositiveVectorIDs); err != nil {
		return err
	}
	if shared.Authorization.VectorID != "authorization-ath-v1-01" {
		return fmt.Errorf("unexpected Authorization vector_id %q", shared.Authorization.VectorID)
	}
	authorizationNegativeIDs := make([]string, 0, len(shared.Authorization.NegativeCases))
	for _, vector := range shared.Authorization.NegativeCases {
		authorizationNegativeIDs = append(authorizationNegativeIDs, vector.VectorID)
	}
	if err := requireExactStringSet("Authorization negative vector IDs", authorizationNegativeIDs, frozenAuthorizationNegativeVectorIDs); err != nil {
		return err
	}
	negativeIDs := make([]string, 0, len(shared.NegativeVectors))
	for _, vector := range shared.NegativeVectors {
		negativeIDs = append(negativeIDs, vector.VectorID)
	}
	if err := requireExactStringSet("negative vector IDs", negativeIDs, frozenNegativeVectorIDs); err != nil {
		return err
	}
	const frozenSemanticIDCount = 17
	if got := len(shared.Vectors) + 1 + len(shared.Authorization.NegativeCases) + len(shared.NegativeVectors); got != frozenSemanticIDCount {
		return fmt.Errorf("shared semantic vector ID count = %d, want %d", got, frozenSemanticIDCount)
	}
	return nil
}

func requireExactStringSet(label string, got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s count = %d, want %d", label, len(got), len(want))
	}
	expected := make(map[string]struct{}, len(want))
	for _, value := range want {
		expected[value] = struct{}{}
	}
	seen := make(map[string]struct{}, len(got))
	for _, value := range got {
		if _, exists := expected[value]; !exists {
			return fmt.Errorf("%s contains unexpected value %q", label, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate value %q", label, value)
		}
		seen[value] = struct{}{}
	}
	for _, value := range want {
		if _, exists := seen[value]; !exists {
			return fmt.Errorf("%s is missing required value %q", label, value)
		}
	}
	return nil
}

func requireSharedVectorDecodeFailure(t *testing.T, shared sharedVectorFile) {
	t.Helper()
	encoded, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSharedVectors(encoded); err == nil {
		t.Fatal("shared vector decoder accepted removal of required frozen semantics")
	}
}

func filterDeviceVectors(vectors []deviceVector, removeID string) []deviceVector {
	filtered := make([]deviceVector, 0, len(vectors))
	for _, vector := range vectors {
		if vector.VectorID != removeID {
			filtered = append(filtered, vector)
		}
	}
	return filtered
}

func filterSignatures(signatures []struct {
	Algorithm         string `json:"algorithm"`
	PublicKeySPKIB64U string `json:"public_key_spki_b64u"`
	SignatureB64U     string `json:"signature_b64u"`
}, removeAlgorithm string) []struct {
	Algorithm         string `json:"algorithm"`
	PublicKeySPKIB64U string `json:"public_key_spki_b64u"`
	SignatureB64U     string `json:"signature_b64u"`
} {
	filtered := make([]struct {
		Algorithm         string `json:"algorithm"`
		PublicKeySPKIB64U string `json:"public_key_spki_b64u"`
		SignatureB64U     string `json:"signature_b64u"`
	}, 0, len(signatures))
	for _, signature := range signatures {
		if signature.Algorithm != removeAlgorithm {
			filtered = append(filtered, signature)
		}
	}
	return filtered
}

func filterAuthorizationNegativeCases(vectors []authorizationNegativeCase, removeID string) []authorizationNegativeCase {
	filtered := make([]authorizationNegativeCase, 0, len(vectors))
	for _, vector := range vectors {
		if vector.VectorID != removeID {
			filtered = append(filtered, vector)
		}
	}
	return filtered
}

func filterNegativeVectors(vectors []negativeVector, removeID string) []negativeVector {
	filtered := make([]negativeVector, 0, len(vectors))
	for _, vector := range vectors {
		if vector.VectorID != removeID {
			filtered = append(filtered, vector)
		}
	}
	return filtered
}

func validateNegativeVectorShape(vector negativeVector) error {
	switch vector.Kind {
	case "canonical-bytes":
		if vector.Mutation != "lf-to-crlf" && vector.Mutation != "remove-final-lf" || vector.FirstTarget != "" || vector.SecondTarget != "" || vector.Algorithm != "" || vector.SignatureB64U != "" {
			return fmt.Errorf("invalid canonical-bytes negative vector")
		}
	case "request":
		if vector.Mutation != "prepend-body-space" || vector.FirstTarget != "" || vector.SecondTarget != "" || vector.Algorithm != "" || vector.SignatureB64U != "" {
			return fmt.Errorf("invalid request negative vector")
		}
	case "request-pair":
		if vector.Mutation != "" || vector.FirstTarget == "" || vector.SecondTarget == "" || vector.FirstTarget == vector.SecondTarget || vector.Algorithm != "" || vector.SignatureB64U != "" {
			return fmt.Errorf("invalid request-pair negative vector")
		}
	case "signature":
		if vector.Mutation != "" || vector.FirstTarget != "" || vector.SecondTarget != "" || vector.Algorithm != AlgorithmP256ES256 || vector.SignatureB64U == "" {
			return fmt.Errorf("invalid signature negative vector")
		}
	case "signature-message":
		if vector.Mutation != "sha256-message" || vector.FirstTarget != "" || vector.SecondTarget != "" || vector.Algorithm != AlgorithmEd25519 || vector.SignatureB64U != "" {
			return fmt.Errorf("invalid signature-message negative vector")
		}
	case "domain-separation":
		if vector.Mutation != "omit-domain-and-nul" || vector.FirstTarget != "" || vector.SecondTarget != "" || vector.Algorithm != "" || vector.SignatureB64U != "" {
			return fmt.Errorf("invalid domain-separation negative vector")
		}
	}
	return nil
}

func readContract(t *testing.T) string {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "潘多拉面板-CLIENT-AUTH-01冻结契约-20260730.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func readDocumentVector(t *testing.T, vectorID string, target any) {
	t.Helper()
	document := readContract(t)
	blocks := regexp.MustCompile("(?s)```json\\s*(.*?)\\s*```").FindAllStringSubmatch(document, -1)
	for _, block := range blocks {
		var identity struct {
			VectorID string `json:"vector_id"`
		}
		if json.Unmarshal([]byte(block[1]), &identity) == nil && identity.VectorID == vectorID {
			if err := json.Unmarshal([]byte(block[1]), target); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("document vector %q not found", vectorID)
}

func mustDecode(t *testing.T, value string, expected int) []byte {
	t.Helper()
	decoded, err := DecodeBase64URLNoPad(value, expected)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
