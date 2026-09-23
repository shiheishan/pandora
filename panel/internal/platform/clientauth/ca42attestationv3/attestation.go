package ca42attestationv3

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
)

const (
	Format             = ca42protocolv2.AttestationFormat
	SignatureAlgorithm = "ed25519"
	MaxBytes           = 192 << 10
	MaxPublicKeyBytes  = 16 << 10
	MaxValidity        = time.Hour
)

var (
	signatureDomain = []byte("PANDORA\x00CA42-ATTESTATION\x00V3\x00")
	hex64RE         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	prefixNames     = []string{
		"format", "signature_algorithm", "profile_id", "profile_sha256", "expected_format",
		"expected_sha256", "nonce", "issued_at", "expires_at",
	}
	fieldNames = buildFieldNames()
)

func buildFieldNames() []string {
	names := append([]string(nil), prefixNames...)
	expectedNames := ca42expectedv2.FieldNames()
	for _, name := range expectedNames {
		switch name {
		case "format", "profile_id", "profile_sha256", "attestation_format", "attestation_signature_algorithm":
			continue
		}
		names = append(names, name)
	}
	return append(names, "signature_b64")
}

type Snapshot struct{ values []string }

func (snapshot Snapshot) Value(name string) string {
	for index, candidate := range fieldNames {
		if candidate == name && index < len(snapshot.values) {
			return snapshot.values[index]
		}
	}
	return ""
}

func (snapshot Snapshot) copy() Snapshot {
	return Snapshot{values: append([]string(nil), snapshot.values...)}
}
func FieldNames() []string { return append([]string(nil), fieldNames...) }

type Attestation struct {
	snapshot  Snapshot
	sha256    [sha256.Size]byte
	canonical []byte
	expected  ca42expectedv2.Expected
	publicPEM []byte
	publicKey ed25519.PublicKey
	parseNow  time.Time
	notBefore time.Time
	notAfter  time.Time
	parsed    bool
}

func ParseAndVerify(
	data []byte,
	expectedArtifactSHA256 [sha256.Size]byte,
	expected ca42expectedv2.Expected,
	publicKeyPEM []byte,
	now time.Time,
) (Attestation, error) {
	var empty Attestation
	if len(data) == 0 || len(data) > MaxBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("attestation v3 envelope invalid")
	}
	if len(publicKeyPEM) == 0 || len(publicKeyPEM) > MaxPublicKeyBytes {
		return empty, errors.New("attestation v3 public key size invalid")
	}
	canonical := append([]byte(nil), data...)
	publicPEMCopy := append([]byte(nil), publicKeyPEM...)
	digest := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(digest[:], expectedArtifactSHA256[:]) != 1 {
		return empty, errors.New("attestation v3 identity mismatch")
	}
	trustedExpected, err := expected.VerifiedCopyAt(now)
	if err != nil {
		return empty, err
	}
	expectedSnapshot, err := trustedExpected.SnapshotAt(now)
	if err != nil {
		return empty, err
	}
	expectedSHA, err := trustedExpected.SHA256Hex(now)
	if err != nil {
		return empty, err
	}
	publicKey, err := parseCanonicalEd25519PublicKey(publicPEMCopy)
	if err != nil {
		return empty, err
	}
	lines := bytes.Split(canonical[:len(canonical)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("attestation v3 field count invalid")
	}
	snapshot := Snapshot{values: make([]string, len(fieldNames))}
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("attestation v3 field order invalid: %s", name)
		}
		snapshot.values[index] = string(lines[index][len(prefix):])
	}
	signatureText := snapshot.Value("signature_b64")
	signature, err := base64.StdEncoding.Strict().DecodeString(signatureText)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != signatureText {
		return empty, errors.New("attestation v3 signature encoding invalid")
	}
	unsignedLength := len(canonical) - len(lines[len(lines)-1]) - 1
	if unsignedLength <= 0 || !ed25519.Verify(publicKey, signatureMessage(canonical[:unsignedLength]), signature) {
		return empty, errors.New("attestation v3 signature denied")
	}
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		return empty, err
	}
	if snapshot.Value("format") != Format || snapshot.Value("signature_algorithm") != SignatureAlgorithm ||
		snapshot.Value("profile_id") != ca42protocolv2.ProfileID || snapshot.Value("profile_sha256") != hex.EncodeToString(profileSHA[:]) ||
		snapshot.Value("expected_format") != ca42expectedv2.Format || snapshot.Value("expected_sha256") != expectedSHA ||
		!nonZeroHex64(snapshot.Value("nonce")) {
		return empty, errors.New("attestation v3 fixed field mismatch")
	}
	for _, name := range ca42expectedv2.FieldNames() {
		switch name {
		case "format", "profile_id", "profile_sha256", "attestation_format", "attestation_signature_algorithm":
			continue
		}
		if snapshot.Value(name) != expectedSnapshot.ValueName(name) {
			return empty, fmt.Errorf("attestation v3 expected binding mismatch: %s", name)
		}
	}
	// The plan pins the exact retained PEM artifact bytes, not only the raw key.
	publicDigest := sha256.Sum256(publicPEMCopy)
	if snapshot.Value("attestation_public_key_sha256") != hex.EncodeToString(publicDigest[:]) {
		return empty, errors.New("attestation v3 public key binding mismatch")
	}
	issuedEpoch, err := epoch(snapshot.Value("issued_at"))
	if err != nil {
		return empty, errors.New("attestation v3 issued-at invalid")
	}
	expiresEpoch, err := epoch(snapshot.Value("expires_at"))
	if err != nil || expiresEpoch <= issuedEpoch || expiresEpoch-issuedEpoch > int64(MaxValidity/time.Second) {
		return empty, errors.New("attestation v3 validity invalid")
	}
	expectedNotBefore, err := epoch(expectedSnapshot.ValueName("not_before_epoch"))
	if err != nil {
		return empty, err
	}
	expectedNotAfter, err := epoch(expectedSnapshot.ValueName("not_after_epoch"))
	if err != nil || issuedEpoch < expectedNotBefore || expiresEpoch > expectedNotAfter {
		return empty, errors.New("attestation v3 validity escapes expected v2")
	}
	nowEpoch := now.UTC().Unix()
	if now.IsZero() || nowEpoch < issuedEpoch || nowEpoch >= expiresEpoch {
		return empty, errors.New("attestation v3 outside validity window")
	}
	return Attestation{
		snapshot: snapshot, sha256: digest, canonical: canonical, expected: trustedExpected,
		publicPEM: publicPEMCopy, publicKey: append(ed25519.PublicKey(nil), publicKey...), parseNow: now.UTC(),
		notBefore: time.Unix(issuedEpoch, 0).UTC(), notAfter: time.Unix(expiresEpoch, 0).UTC(), parsed: true,
	}, nil
}

func BindPlan(attestation Attestation, plan ca42executionv2.Plan, now time.Time) error {
	a, err := attestation.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	p, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	if err := ca42expectedv2.BindPlan(a.expected, p, now); err != nil {
		return err
	}
	if hex.EncodeToString(a.sha256[:]) != p.AttestationSHA256 || a.snapshot.Value("attestation_public_key_sha256") != p.AttestationPublicKeySHA256 ||
		a.notBefore.Before(p.NotBefore) || a.notAfter.After(p.NotAfter) {
		return errors.New("attestation v3 and execution plan v2 binding mismatch")
	}
	return nil
}

func BindRelease(attestation Attestation, release ca42releasev3.Manifest, now time.Time) error {
	a, err := attestation.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	r, err := release.SnapshotAt(now)
	if err != nil {
		return err
	}
	if err := ca42expectedv2.BindRelease(a.expected, release, now); err != nil {
		return err
	}
	if hex.EncodeToString(a.sha256[:]) != r.Value(ca42releasev3.FieldAttestationSHA256) ||
		a.snapshot.Value("format") != r.Value(ca42releasev3.FieldAttestationFormat) {
		return errors.New("attestation v3 and release manifest v3 binding mismatch")
	}
	return nil
}

func BindCapsule(attestation Attestation, capsule ca42capsulev2.Capsule, now time.Time) error {
	a, err := attestation.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	c, err := capsule.Snapshot()
	if err != nil {
		return err
	}
	expectedSHA, err := a.expected.SHA256Hex(now)
	if err != nil {
		return err
	}
	for _, name := range ca42capsulev2.FieldNames() {
		capsuleValue := c.ValueName(name)
		var attestationValue string
		switch name {
		case "format":
			attestationValue = ca42capsulev2.Format
		case "attestation_format":
			attestationValue = a.snapshot.Value("format")
		case "attestation_sha256":
			attestationValue = hex.EncodeToString(a.sha256[:])
		case "expected_format":
			attestationValue = a.snapshot.Value("expected_format")
		case "expected_sha256":
			attestationValue = expectedSHA
		default:
			attestationValue = a.snapshot.Value(name)
		}
		if attestationValue == "" || attestationValue != capsuleValue {
			return errors.New("attestation v3 and trust capsule v2 binding mismatch")
		}
	}
	return nil
}

func BindExternal(attestation Attestation, externalManifestBytes []byte, now time.Time) error {
	a, err := attestation.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	if len(externalManifestBytes) == 0 || len(externalManifestBytes) > ca42manifest.MaxManifestBytes {
		return errors.New("attestation v3 external manifest size invalid")
	}
	receipt, err := ca42manifest.Verify(append([]byte(nil), externalManifestBytes...))
	if err != nil {
		return errors.New("attestation v3 external manifest invalid")
	}
	s := a.snapshot
	if receipt.ReceiptFormat != "client-auth-00042-external-manifest-structural-receipt-v1" ||
		receipt.Decision != "STRUCTURALLY_VALID" || receipt.Authorization != "NONE" || receipt.ContractSHA256 != ca42manifest.ContractSHA256 ||
		receipt.ExternalManifestSHA256 != s.Value("external_manifest_sha256") || receipt.ExactObjectManifestSHA256 != s.Value("external_object_manifest_sha256") ||
		receipt.IsolatedContainerID != s.Value("isolated_container_id") || receipt.IsolatedSystemIdentifier != s.Value("isolated_system_identifier") ||
		receipt.IsolatedNetworkID != s.Value("isolated_network_id") || receipt.IsolatedDatabase != s.Value("isolated_database_name") ||
		receipt.IsolatedDatabaseOID != s.Value("isolated_database_oid") || receipt.IsolatedImageID != s.Value("isolated_image_id") ||
		receipt.IsolatedRunID != s.Value("isolated_run_id") || receipt.IsolatedImageID != "sha256:"+s.Value("postgres_image_sha256") {
		return errors.New("attestation v3 external manifest binding mismatch")
	}
	return nil
}

func (attestation Attestation) VerifiedCopyAt(now time.Time) (Attestation, error) {
	if !attestation.parsed || len(attestation.canonical) == 0 || len(attestation.publicPEM) == 0 ||
		attestation.parseNow.IsZero() || attestation.sha256 == ([sha256.Size]byte{}) {
		return Attestation{}, errors.New("attestation v3 capability invalid")
	}
	digest := sha256.Sum256(attestation.canonical)
	if digest != attestation.sha256 {
		return Attestation{}, errors.New("attestation v3 identity changed")
	}
	return ParseAndVerify(attestation.canonical, digest, attestation.expected, attestation.publicPEM, now)
}

func (attestation Attestation) SnapshotAt(now time.Time) (Snapshot, error) {
	verified, err := attestation.VerifiedCopyAt(now)
	if err != nil {
		return Snapshot{}, err
	}
	return verified.snapshot.copy(), nil
}

func (attestation Attestation) SHA256Hex(now time.Time) (string, error) {
	verified, err := attestation.VerifiedCopyAt(now)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(verified.sha256[:]), nil
}

func UnsignedCanonicalBytes(values []string) ([]byte, error) {
	if len(values) != len(fieldNames)-1 {
		return nil, errors.New("attestation v3 unsigned field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("attestation v3 unsigned value invalid")
		}
		if output.Len()+len(fieldNames[index])+len(value)+2 > MaxBytes {
			return nil, errors.New("attestation v3 unsigned size invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func SignedCanonicalBytes(unsigned, signature []byte) ([]byte, error) {
	if len(unsigned) == 0 || len(unsigned) > MaxBytes || unsigned[len(unsigned)-1] != '\n' || len(signature) != ed25519.SignatureSize ||
		len(unsigned)+len("signature_b64=")+base64.StdEncoding.EncodedLen(len(signature))+1 > MaxBytes {
		return nil, errors.New("attestation v3 signed input invalid")
	}
	result := append([]byte(nil), unsigned...)
	result = append(result, []byte("signature_b64=")...)
	result = append(result, base64.StdEncoding.EncodeToString(signature)...)
	result = append(result, '\n')
	return result, nil
}

func SignatureMessage(unsigned []byte) ([]byte, error) {
	if len(unsigned) == 0 || len(unsigned) > MaxBytes || unsigned[len(unsigned)-1] != '\n' ||
		bytes.IndexByte(unsigned, '\r') >= 0 || bytes.IndexByte(unsigned, 0) >= 0 || !utf8.Valid(unsigned) {
		return nil, errors.New("attestation v3 signature input invalid")
	}
	return signatureMessage(unsigned), nil
}

func signatureMessage(unsigned []byte) []byte {
	message := make([]byte, 0, len(signatureDomain)+len(unsigned))
	message = append(message, signatureDomain...)
	message = append(message, unsigned...)
	return message
}

func parseCanonicalEd25519PublicKey(data []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" || len(block.Headers) != 0 || len(rest) != 0 ||
		!bytes.Equal(data, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: block.Bytes})) {
		return nil, errors.New("attestation v3 public key PEM invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("attestation v3 public key is not Ed25519")
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	canonicalDER, marshalErr := x509.MarshalPKIXPublicKey(parsed)
	if marshalErr != nil || !ok || len(publicKey) != ed25519.PublicKeySize ||
		bytes.Equal(publicKey, make([]byte, ed25519.PublicKeySize)) || !bytes.Equal(canonicalDER, block.Bytes) {
		return nil, errors.New("attestation v3 public key is not Ed25519")
	}
	return append(ed25519.PublicKey(nil), publicKey...), nil
}

func nonZeroHex64(value string) bool {
	return hex64RE.MatchString(value) && value != strings.Repeat("0", 64)
}
func epoch(value string) (int64, error) {
	if value == "" || value[0] == '0' || len(value) > 10 {
		return 0, errors.New("epoch invalid")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("epoch invalid")
	}
	return parsed, nil
}
