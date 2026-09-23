package ca42runner

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsule"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

const (
	attestationV2Format    = "client-auth-attestation-v2"
	attestationV2Algorithm = "ed25519"
	maxAttestationBytes    = 64 << 10
	maxExpectedBytes       = 64 << 10
	maxPublicKeyBytes      = 16 << 10
)

var (
	attestationTokenPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	attestationDatabasePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	attestationEpochPattern      = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)
	attestationSystemIDPattern   = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	attestationSignaturePattern  = regexp.MustCompile(`^[A-Za-z0-9+/]{86}==$`)
	attestationPayloadFieldNames = [...]string{
		"format", "signature_algorithm", "nonce", "issued_at", "expires_at", "release_id",
		"source_container_id", "source_system_identifier", "source_database_name",
		"source_database_oid", "source_database_owner_oid", "source_database_owner_name",
		"source_goose_waterline", "migration_set_sha256", "client_auth_00042_sha256",
		"goose_binary_sha256", "preflight_runner_sha256", "globals_dump_sha256",
		"database_dump_sha256", "postgres_image_sha256", "catalog_manifest_sha256",
	}
	attestationExpectedFieldNames = [...]string{
		"release_id", "source_container_id", "source_system_identifier", "source_database_name",
		"source_database_oid", "source_database_owner_oid", "source_database_owner_name",
		"source_goose_waterline", "migration_set_sha256", "client_auth_00042_sha256",
		"goose_binary_sha256", "preflight_runner_sha256", "globals_dump_sha256",
		"database_dump_sha256", "postgres_image_sha256", "catalog_manifest_sha256",
	}
)

type attestationValidity struct {
	notBefore time.Time
	notAfter  time.Time
}

func (v attestationValidity) validateAt(now time.Time) error {
	now = now.UTC()
	if v.notBefore.IsZero() || v.notAfter.IsZero() || now.IsZero() || now.Before(v.notBefore) || !now.Before(v.notAfter) {
		return errors.New("CA42 retained attestation outside validity window")
	}
	return nil
}

func verifyRetainedAttestationV2(
	attestationBytes, expectedBytes, publicKeyPEM []byte,
	plan ca42execution.Plan,
	capsule ca42capsule.Capsule,
	manifest ca42manifest.Receipt,
	now time.Time,
) (attestationValidity, error) {
	var empty attestationValidity
	if len(attestationBytes) == 0 || len(attestationBytes) > maxAttestationBytes || len(expectedBytes) == 0 ||
		len(expectedBytes) > maxExpectedBytes || len(publicKeyPEM) == 0 || len(publicKeyPEM) > maxPublicKeyBytes {
		return empty, errors.New("CA42 attestation v2 size invalid")
	}
	attestationValues, attestationLines, err := parseCanonicalFields(attestationBytes, attestationPayloadFieldNames[:], true)
	if err != nil {
		return empty, fmt.Errorf("CA42 attestation v2: %w", err)
	}
	expectedValues, _, err := parseCanonicalFields(expectedBytes, attestationExpectedFieldNames[:], false)
	if err != nil {
		return empty, fmt.Errorf("CA42 expected attestation v2: %w", err)
	}
	if attestationValues[0] != attestationV2Format || attestationValues[1] != attestationV2Algorithm ||
		!artifactSHA256Pattern.MatchString(attestationValues[2]) || attestationValues[2] == strings.Repeat("0", 64) ||
		!attestationTokenPattern.MatchString(attestationValues[5]) ||
		!artifactSHA256Pattern.MatchString(attestationValues[6]) || attestationValues[6] == strings.Repeat("0", 64) ||
		!attestationSystemIDPattern.MatchString(attestationValues[7]) || !canonicalPositiveUint(attestationValues[7], 64) ||
		!attestationDatabasePattern.MatchString(attestationValues[8]) ||
		!canonicalPositiveUint(attestationValues[9], 32) ||
		!canonicalPositiveUint(attestationValues[10], 32) ||
		!attestationDatabasePattern.MatchString(attestationValues[11]) || attestationValues[12] != "41" {
		return empty, errors.New("CA42 attestation v2 fixed field invalid")
	}
	for index := 13; index <= 20; index++ {
		if !artifactSHA256Pattern.MatchString(attestationValues[index]) || attestationValues[index] == strings.Repeat("0", 64) {
			return empty, fmt.Errorf("CA42 attestation v2 hash invalid: %s", attestationPayloadFieldNames[index])
		}
	}
	issuedEpoch, err := parseAttestationEpoch(attestationValues[3])
	if err != nil {
		return empty, errors.New("CA42 attestation v2 issued-at invalid")
	}
	expiresEpoch, err := parseAttestationEpoch(attestationValues[4])
	if err != nil || expiresEpoch <= issuedEpoch || expiresEpoch-issuedEpoch > 3600 {
		return empty, errors.New("CA42 attestation v2 validity invalid")
	}
	issuedAt, expiresAt := time.Unix(issuedEpoch, 0).UTC(), time.Unix(expiresEpoch, 0).UTC()
	now = now.UTC()
	if now.IsZero() || now.Before(issuedAt) || !now.Before(expiresAt) {
		return empty, errors.New("CA42 attestation v2 outside validity window")
	}
	if issuedAt.Before(plan.NotBefore) || expiresAt.After(plan.NotAfter) {
		return empty, errors.New("CA42 attestation v2 validity escapes execution plan")
	}
	for index := range attestationExpectedFieldNames {
		if expectedValues[index] != attestationValues[index+5] {
			return empty, fmt.Errorf("CA42 attestation v2 expected binding mismatch: %s", attestationExpectedFieldNames[index])
		}
	}
	expectedPlanValues := [...]string{
		plan.ReleaseID, plan.SourceContainerID, plan.SourceSystemIdentifier, plan.SourceDatabase,
		plan.SourceDatabaseOID, plan.SourceDatabaseOwnerOID, plan.SourceDatabaseOwner, "41",
		plan.MigrationSetSHA256, plan.ClientAuth00042SHA256, plan.GooseBinarySHA256,
		plan.PreflightRunnerSHA256, plan.GlobalsDumpSHA256, plan.DatabaseDumpSHA256,
		plan.PostgresImageSHA256, plan.ExternalManifestSHA256,
	}
	for index := range expectedPlanValues {
		if expectedValues[index] != expectedPlanValues[index] {
			return empty, fmt.Errorf("CA42 attestation v2 plan binding mismatch: %s", attestationExpectedFieldNames[index])
		}
	}
	if capsule.ReleaseID != plan.ReleaseID || capsule.ReleaseRunID != plan.ReleaseRunID || capsule.TargetSystemIdentifier != plan.SourceSystemIdentifier ||
		capsule.TargetDatabase != plan.SourceDatabase || capsule.TargetDatabaseOID != plan.SourceDatabaseOID ||
		capsule.AttestationSHA256 != plan.AttestationSHA256 || capsule.ExpectedSHA256 != plan.ExpectedSHA256 ||
		capsule.PublicKeySHA256 != plan.AttestationPublicKeySHA256 || capsule.ExternalManifestSHA256 != plan.ExternalManifestSHA256 {
		return empty, errors.New("CA42 attestation v2 capsule binding mismatch")
	}
	if manifest.ReceiptFormat != "client-auth-00042-external-manifest-structural-receipt-v1" ||
		manifest.Decision != "STRUCTURALLY_VALID" || manifest.Authorization != "NONE" ||
		manifest.ContractSHA256 != ca42manifest.ContractSHA256 || manifest.ExternalManifestSHA256 != plan.ExternalManifestSHA256 ||
		manifest.IsolatedSystemIdentifier != plan.IsolatedSystemIdentifier || manifest.IsolatedContainerID != plan.IsolatedContainerID ||
		manifest.IsolatedNetworkID != plan.IsolatedNetworkID || manifest.IsolatedDatabase != plan.IsolatedDatabase ||
		manifest.IsolatedDatabaseOID != plan.IsolatedDatabaseOID || manifest.IsolatedImageID != plan.IsolatedImageID ||
		manifest.IsolatedRunID != plan.IsolatedRunID || manifest.IsolatedImageID != "sha256:"+plan.PostgresImageSHA256 {
		return empty, errors.New("CA42 attestation v2 external manifest receipt binding mismatch")
	}
	publicKey, err := parseCanonicalEd25519PublicKey(publicKeyPEM)
	if err != nil {
		return empty, err
	}
	signatureText := attestationValues[len(attestationValues)-1]
	if !attestationSignaturePattern.MatchString(signatureText) {
		return empty, errors.New("CA42 attestation v2 signature encoding invalid")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(signatureText)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != signatureText {
		return empty, errors.New("CA42 attestation v2 signature encoding invalid")
	}
	payload := bytes.Join(attestationLines[:len(attestationPayloadFieldNames)], []byte{'\n'})
	payload = append(payload, '\n')
	if !ed25519.Verify(publicKey, payload, signature) {
		return empty, errors.New("CA42 attestation v2 signature invalid")
	}
	return attestationValidity{notBefore: issuedAt, notAfter: expiresAt}, nil
}

func parseCanonicalFields(data []byte, names []string, hasSignature bool) ([]string, [][]byte, error) {
	expectedLines := len(names)
	if hasSignature {
		expectedLines++
	}
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.IndexByte(data, '\r') >= 0 ||
		bytes.IndexByte(data, 0) >= 0 || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return nil, nil, errors.New("canonical envelope invalid")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != expectedLines {
		return nil, nil, errors.New("canonical field count invalid")
	}
	values := make([]string, expectedLines)
	for index, name := range names {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return nil, nil, fmt.Errorf("canonical field order invalid: %s", name)
		}
		values[index] = string(lines[index][len(prefix):])
	}
	if hasSignature {
		prefix := []byte("signature_b64=")
		last := len(lines) - 1
		if !bytes.HasPrefix(lines[last], prefix) || len(lines[last]) == len(prefix) {
			return nil, nil, errors.New("canonical signature field invalid")
		}
		values[last] = string(lines[last][len(prefix):])
	}
	return values, lines, nil
}

func parseAttestationEpoch(value string) (int64, error) {
	if !attestationEpochPattern.MatchString(value) {
		return 0, errors.New("epoch invalid")
	}
	return strconv.ParseInt(value, 10, 64)
}

func canonicalPositiveUint(value string, bits int) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func parseCanonicalEd25519PublicKey(data []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" || len(block.Headers) != 0 || len(rest) != 0 ||
		!bytes.Equal(data, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: block.Bytes})) {
		return nil, errors.New("CA42 attestation public key PEM invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("CA42 attestation public key is not Ed25519")
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	canonicalDER, marshalErr := x509.MarshalPKIXPublicKey(parsed)
	if marshalErr != nil || !ok || len(publicKey) != ed25519.PublicKeySize ||
		bytes.Equal(publicKey, make([]byte, ed25519.PublicKeySize)) || !bytes.Equal(canonicalDER, block.Bytes) {
		return nil, errors.New("CA42 attestation public key is not Ed25519")
	}
	return publicKey, nil
}
