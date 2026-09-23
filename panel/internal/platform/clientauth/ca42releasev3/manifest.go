package ca42releasev3

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

const (
	Format             = ca42protocolv2.ReleaseManifestFormat
	SignatureAlgorithm = "ed25519"
	Status             = "SEALED_FOR_ROOT_RUNNER"
	MaxManifestBytes   = 128 << 10
	MaxValidity        = time.Hour
	PostgreSQLMajor    = "18"
	TargetWaterline    = "42"
)

var signatureDomain = []byte("PANDORA\x00CA42-RELEASE-MANIFEST\x00V3\x00")

var (
	hex64RE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tokenRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifierRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	isolatedRunRE = regexp.MustCompile(`^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$`)
)

type Field uint8

const (
	FieldFormat Field = iota
	FieldSignatureAlgorithm
	FieldStatus
	FieldProfileID
	FieldProfileSHA256
	FieldAuthorityEpoch
	FieldAuthoritySequence
	FieldAuthorityBindingSHA256
	FieldReleaseSignerKeyID
	FieldArchitecture
	FieldReleaseID
	FieldReleaseRunID
	FieldAttemptID
	FieldTransition
	FieldCredentialSourceDescriptorSHA256
	FieldPathtrustBinarySHA256
	FieldPathtrustChainSHA256
	FieldPathtrustDevice
	FieldPathtrustMode
	FieldTrustCapsuleSHA256
	FieldAttestationCoreSHA256
	FieldAttestationCoreChainSHA256
	FieldAttestationCoreDevice
	FieldAttestationCoreMode
	FieldAttestationFormat
	FieldAttestationSHA256
	FieldExpectedFormat
	FieldExpectedSHA256
	FieldAttestationPublicKeySHA256
	FieldExternalManifestSHA256
	FieldExternalObjectManifestSHA256
	FieldBashBinarySHA256
	FieldBashChainSHA256
	FieldBashDevice
	FieldBashMode
	FieldDockerClientSHA256
	FieldDockerClientChainSHA256
	FieldDockerClientDevice
	FieldDockerClientMode
	FieldRuntimeClosureManifestSHA256
	FieldManifestVerifierSHA256
	FieldPreflightRunnerSHA256
	FieldMigrationRunnerSHA256
	FieldGooseBinarySHA256
	FieldGooseVersion
	FieldGooseBuildInfoSHA256
	FieldMigrationSetSHA256
	FieldClientAuth00042SHA256
	FieldArtifactStorageProfile
	FieldArtifactStorageDescriptorSHA256
	FieldGlobalsDumpSHA256
	FieldGlobalsDumpSizeBytes
	FieldDatabaseDumpSHA256
	FieldDatabaseDumpSizeBytes
	FieldPostgresImageSHA256
	FieldProductionSourceContainerID
	FieldProductionSourceSystemIdentifier
	FieldProductionSourceDatabase
	FieldProductionSourceDatabaseOID
	FieldProductionSourceDatabaseOwnerOID
	FieldProductionSourceDatabaseOwnerName
	FieldProductionSourceGooseWaterline
	FieldIsolatedTargetContainerID
	FieldIsolatedTargetSystemIdentifier
	FieldIsolatedTargetNetworkID
	FieldIsolatedTargetDatabase
	FieldIsolatedTargetDatabaseOID
	FieldIsolatedTargetImageID
	FieldIsolatedTargetRunID
	FieldLedgerNamespace
	FieldLedgerDirectorySHA256
	FieldReleaseJournalFormat
	FieldReleaseJournalManifestFormat
	FieldReleaseJournalNamespace
	FieldReleaseJournalHeadSHA256
	FieldReleaseJournalSnapshotSHA256
	FieldExecutionPlanFormat
	FieldExecutionPlanSHA256
	FieldNotBeforeEpoch
	FieldNotAfterEpoch
	FieldSignatureB64
	fieldCount
)

var fieldNames = [fieldCount]string{
	"format", "signature_algorithm", "status", "profile_id", "profile_sha256",
	"authority_epoch", "authority_sequence", "authority_binding_sha256", "release_signer_key_id",
	"architecture", "release_id", "release_run_id", "attempt_id", "transition", "credential_source_descriptor_sha256",
	"pathtrust_binary_sha256", "pathtrust_chain_sha256", "pathtrust_device", "pathtrust_mode",
	"trust_capsule_sha256", "attestation_core_sha256", "attestation_core_chain_sha256",
	"attestation_core_device", "attestation_core_mode", "attestation_format", "attestation_sha256",
	"expected_format", "expected_sha256", "attestation_public_key_sha256", "external_manifest_sha256",
	"external_object_manifest_sha256", "bash_binary_sha256", "bash_chain_sha256", "bash_device", "bash_mode",
	"docker_client_sha256", "docker_client_chain_sha256", "docker_client_device", "docker_client_mode",
	"runtime_closure_manifest_sha256", "manifest_verifier_sha256", "preflight_runner_sha256",
	"migration_runner_sha256", "goose_binary_sha256", "goose_version", "goose_build_info_sha256",
	"migration_set_sha256", "client_auth_00042_sha256", "artifact_storage_profile",
	"artifact_storage_descriptor_sha256", "globals_dump_sha256", "globals_dump_size_bytes",
	"database_dump_sha256", "database_dump_size_bytes", "postgres_image_sha256",
	"production_source_container_id", "production_source_system_identifier", "production_source_database",
	"production_source_database_oid", "production_source_database_owner_oid", "production_source_database_owner_name",
	"production_source_goose_waterline", "isolated_target_container_id", "isolated_target_system_identifier",
	"isolated_target_network_id", "isolated_target_database", "isolated_target_database_oid",
	"isolated_target_image_id", "isolated_target_run_id", "ledger_namespace", "ledger_directory_sha256",
	"release_journal_format", "release_journal_manifest_format", "release_journal_namespace", "release_journal_head_sha256",
	"release_journal_snapshot_sha256", "execution_plan_format", "execution_plan_sha256",
	"not_before_epoch", "not_after_epoch", "signature_b64",
}

type Snapshot struct{ values [fieldCount]string }

func (snapshot Snapshot) Value(field Field) string {
	if field >= fieldCount {
		return ""
	}
	return snapshot.values[field]
}

func FieldName(field Field) string {
	if field >= fieldCount {
		return ""
	}
	return fieldNames[field]
}

type AuthorityBinding struct {
	PublicKey          ed25519.PublicKey
	ManifestSHA256     [sha256.Size]byte
	SignerSHA256       [sha256.Size]byte
	Epoch              uint64
	Sequence           uint64
	BindingSHA256      [sha256.Size]byte
	ReleaseSignerKeyID string
}

type Manifest struct {
	snapshot      Snapshot
	sha256        [sha256.Size]byte
	canonical     []byte
	authority     AuthorityBinding
	architecture  string
	externalBytes []byte
	parseNow      time.Time
	parsed        bool
}

// AuthorityVerifiedManifest is an opaque first-stage capability. It proves
// the release bytes, signer, authority binding, fixed fields and validity but
// grants no complete release authority until BindExternal verifies the exact
// retained external manifest.
type AuthorityVerifiedManifest struct {
	snapshot     Snapshot
	sha256       [sha256.Size]byte
	canonical    []byte
	authority    AuthorityBinding
	architecture string
	parseNow     time.Time
	parsed       bool
}

func ParseAndVerify(data []byte, authority AuthorityBinding, architecture string, now time.Time, externalManifestBytes []byte) (Manifest, error) {
	verified, err := ParseAuthorityVerified(data, authority, architecture, now)
	if err != nil {
		return Manifest{}, err
	}
	return verified.BindExternal(externalManifestBytes, now)
}

// ParseAuthorityVerified breaks the production dependency cycle without
// trusting detached fields: only this signature-verified capability can reveal
// the execution-plan digest needed to retain the exact external manifest.
func ParseAuthorityVerified(data []byte, authority AuthorityBinding, architecture string, now time.Time) (AuthorityVerifiedManifest, error) {
	var empty AuthorityVerifiedManifest
	if len(data) == 0 || len(data) > MaxManifestBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("release manifest v3 envelope invalid")
	}
	canonical := append([]byte(nil), data...)
	digest := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(digest[:], authority.ManifestSHA256[:]) != 1 {
		return empty, errors.New("release manifest v3 identity mismatch")
	}
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.Epoch == 0 || authority.Sequence == 0 ||
		authority.BindingSHA256 == ([sha256.Size]byte{}) || !tokenRE.MatchString(authority.ReleaseSignerKeyID) {
		return empty, errors.New("release manifest v3 authority invalid")
	}
	authority.PublicKey = append(ed25519.PublicKey(nil), authority.PublicKey...)
	keyDigest := sha256.Sum256(authority.PublicKey)
	if subtle.ConstantTimeCompare(keyDigest[:], authority.SignerSHA256[:]) != 1 {
		return empty, errors.New("release manifest v3 signer identity mismatch")
	}
	lines := bytes.Split(canonical[:len(canonical)-1], []byte{'\n'})
	if len(lines) != int(fieldCount) {
		return empty, errors.New("release manifest v3 field count invalid")
	}
	var snapshot Snapshot
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("release manifest v3 field order invalid: %s", name)
		}
		snapshot.values[index] = string(lines[index][len(prefix):])
	}
	signatureText := snapshot.Value(FieldSignatureB64)
	signature, err := base64.StdEncoding.Strict().DecodeString(signatureText)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != signatureText {
		return empty, errors.New("release manifest v3 signature encoding invalid")
	}
	unsignedLength := len(canonical) - len(lines[FieldSignatureB64]) - 1
	if unsignedLength <= 0 {
		return empty, errors.New("release manifest v3 signature boundary invalid")
	}
	message := signatureMessage(canonical[:unsignedLength])
	if !ed25519.Verify(authority.PublicKey, message, signature) {
		return empty, errors.New("release manifest v3 signature denied")
	}
	if err := validateSnapshotCore(snapshot, authority, architecture, now); err != nil {
		return empty, err
	}
	return AuthorityVerifiedManifest{
		snapshot: snapshot, sha256: digest, canonical: canonical, authority: authority,
		architecture: architecture, parseNow: now.UTC(), parsed: true,
	}, nil
}

func (verified AuthorityVerifiedManifest) verifiedCopyAt(now time.Time) (AuthorityVerifiedManifest, error) {
	if !verified.parsed || len(verified.canonical) == 0 || verified.parseNow.IsZero() || verified.architecture == "" ||
		verified.sha256 == ([sha256.Size]byte{}) || sha256.Sum256(verified.canonical) != verified.sha256 {
		return AuthorityVerifiedManifest{}, errors.New("release manifest v3 authority-verified capability invalid")
	}
	authority := verified.authority
	authority.PublicKey = append(ed25519.PublicKey(nil), verified.authority.PublicKey...)
	return ParseAuthorityVerified(verified.canonical, authority, verified.architecture, now)
}

// ExecutionPlanSHA256At returns only the signed plan digest. The caller must
// still parse and cross-bind the plan; no other release projection is exposed.
func (verified AuthorityVerifiedManifest) ExecutionPlanSHA256At(now time.Time) ([sha256.Size]byte, error) {
	trusted, err := verified.verifiedCopyAt(now)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	decoded, err := hex.DecodeString(trusted.snapshot.Value(FieldExecutionPlanSHA256))
	if err != nil || len(decoded) != sha256.Size {
		return [sha256.Size]byte{}, errors.New("release manifest v3 execution plan digest invalid")
	}
	var result [sha256.Size]byte
	copy(result[:], decoded)
	return result, nil
}

// BindExternal completes the release capability using the exact retained
// external-manifest bytes after revalidating the authority stage at now.
func (verified AuthorityVerifiedManifest) BindExternal(externalManifestBytes []byte, now time.Time) (Manifest, error) {
	if len(externalManifestBytes) == 0 || len(externalManifestBytes) > ca42manifest.MaxManifestBytes {
		return Manifest{}, errors.New("release manifest v3 external envelope invalid")
	}
	trusted, err := verified.verifiedCopyAt(now)
	if err != nil {
		return Manifest{}, err
	}
	externalCanonical := append([]byte(nil), externalManifestBytes...)
	if err := validateExternalSnapshot(trusted.snapshot, externalCanonical); err != nil {
		return Manifest{}, err
	}
	return Manifest{
		snapshot: trusted.snapshot, sha256: trusted.sha256, canonical: trusted.canonical, authority: trusted.authority,
		architecture: trusted.architecture, externalBytes: externalCanonical, parseNow: now.UTC(), parsed: true,
	}, nil
}

func validateSnapshotCore(snapshot Snapshot, authority AuthorityBinding, architecture string, now time.Time) error {
	profile := ca42protocolv2.Strict()
	profileSHA, err := profile.SHA256()
	if err != nil {
		return err
	}
	if snapshot.Value(FieldFormat) != Format || snapshot.Value(FieldSignatureAlgorithm) != SignatureAlgorithm ||
		snapshot.Value(FieldStatus) != Status || snapshot.Value(FieldProfileID) != ca42protocolv2.ProfileID ||
		snapshot.Value(FieldProfileSHA256) != hex.EncodeToString(profileSHA[:]) ||
		snapshot.Value(FieldArchitecture) != architecture || (architecture != "amd64" && architecture != "arm64") ||
		snapshot.Value(FieldTransition) != ca42executionv2.Transition ||
		snapshot.Value(FieldPathtrustMode) != ca42executionv2.RequiredAttemptExecutableMode ||
		snapshot.Value(FieldAttestationCoreMode) != ca42executionv2.RequiredAttemptExecutableMode ||
		snapshot.Value(FieldBashMode) != ca42executionv2.RequiredSystemExecutableMode ||
		snapshot.Value(FieldDockerClientMode) != ca42executionv2.RequiredSystemExecutableMode ||
		snapshot.Value(FieldAttestationFormat) != ca42protocolv2.AttestationFormat ||
		snapshot.Value(FieldExpectedFormat) != ca42protocolv2.ExpectedFormat ||
		snapshot.Value(FieldGooseVersion) != ca42executionv2.RequiredGooseVersion ||
		snapshot.Value(FieldArtifactStorageProfile) != ca42executionv2.RequiredStorageProfile ||
		snapshot.Value(FieldLedgerNamespace) != ca42protocolv2.LedgerNamespace ||
		snapshot.Value(FieldLedgerDirectorySHA256) != ca42executionv2.LedgerDirectorySHA256 ||
		snapshot.Value(FieldReleaseJournalFormat) != ca42protocolv2.ReleaseJournalFormat ||
		snapshot.Value(FieldReleaseJournalManifestFormat) != ca42protocolv2.ReleaseJournalManifestFormat ||
		snapshot.Value(FieldReleaseJournalNamespace) != ca42protocolv2.ReleaseJournalNamespace ||
		snapshot.Value(FieldExecutionPlanFormat) != ca42protocolv2.ExecutionPlanFormat ||
		snapshot.Value(FieldProductionSourceGooseWaterline) != "41" {
		return errors.New("release manifest v3 fixed field mismatch")
	}
	if snapshot.Value(FieldAuthorityEpoch) != strconv.FormatUint(authority.Epoch, 10) ||
		snapshot.Value(FieldAuthoritySequence) != strconv.FormatUint(authority.Sequence, 10) ||
		!constantHashEqual(snapshot.Value(FieldAuthorityBindingSHA256), authority.BindingSHA256) ||
		snapshot.Value(FieldReleaseSignerKeyID) != authority.ReleaseSignerKeyID {
		return errors.New("release manifest v3 authority binding mismatch")
	}
	for _, field := range []Field{FieldReleaseSignerKeyID, FieldReleaseID, FieldReleaseRunID, FieldAttemptID} {
		if !tokenRE.MatchString(snapshot.Value(field)) {
			return fmt.Errorf("release manifest v3 token invalid: %s", FieldName(field))
		}
	}
	hashFields := []Field{
		FieldProfileSHA256, FieldAuthorityBindingSHA256, FieldCredentialSourceDescriptorSHA256,
		FieldPathtrustBinarySHA256, FieldPathtrustChainSHA256, FieldTrustCapsuleSHA256,
		FieldAttestationCoreSHA256, FieldAttestationCoreChainSHA256, FieldAttestationSHA256,
		FieldExpectedSHA256, FieldAttestationPublicKeySHA256, FieldExternalManifestSHA256,
		FieldExternalObjectManifestSHA256, FieldBashBinarySHA256, FieldBashChainSHA256,
		FieldDockerClientSHA256, FieldDockerClientChainSHA256, FieldRuntimeClosureManifestSHA256,
		FieldManifestVerifierSHA256, FieldPreflightRunnerSHA256, FieldMigrationRunnerSHA256,
		FieldGooseBinarySHA256, FieldGooseBuildInfoSHA256, FieldMigrationSetSHA256,
		FieldClientAuth00042SHA256, FieldArtifactStorageDescriptorSHA256, FieldGlobalsDumpSHA256,
		FieldDatabaseDumpSHA256, FieldPostgresImageSHA256, FieldProductionSourceContainerID,
		FieldIsolatedTargetContainerID, FieldIsolatedTargetNetworkID, FieldLedgerDirectorySHA256,
		FieldReleaseJournalHeadSHA256, FieldReleaseJournalSnapshotSHA256, FieldExecutionPlanSHA256,
	}
	for _, field := range hashFields {
		if !nonZeroHex64(snapshot.Value(field)) {
			return fmt.Errorf("release manifest v3 SHA256 invalid: %s", FieldName(field))
		}
	}
	for _, field := range []Field{FieldPathtrustDevice, FieldAttestationCoreDevice, FieldBashDevice, FieldDockerClientDevice} {
		if !canonicalPositive(snapshot.Value(field), 64) {
			return fmt.Errorf("release manifest v3 device invalid: %s", FieldName(field))
		}
	}
	for _, field := range []Field{FieldProductionSourceSystemIdentifier, FieldIsolatedTargetSystemIdentifier} {
		if !canonicalPositive(snapshot.Value(field), 64) {
			return fmt.Errorf("release manifest v3 system identifier invalid: %s", FieldName(field))
		}
	}
	for _, field := range []Field{FieldProductionSourceDatabaseOID, FieldProductionSourceDatabaseOwnerOID, FieldIsolatedTargetDatabaseOID} {
		if !canonicalPositive(snapshot.Value(field), 32) {
			return fmt.Errorf("release manifest v3 OID invalid: %s", FieldName(field))
		}
	}
	for _, field := range []Field{FieldProductionSourceDatabase, FieldProductionSourceDatabaseOwnerName, FieldIsolatedTargetDatabase} {
		if !identifierRE.MatchString(snapshot.Value(field)) {
			return fmt.Errorf("release manifest v3 identifier invalid: %s", FieldName(field))
		}
	}
	if !canonicalBoundedSize(snapshot.Value(FieldGlobalsDumpSizeBytes), ca42executionv2.MaxGlobalsDumpBytes) ||
		!canonicalBoundedSize(snapshot.Value(FieldDatabaseDumpSizeBytes), ca42executionv2.MaxDatabaseDumpBytes) {
		return errors.New("release manifest v3 dump size invalid")
	}
	if snapshot.Value(FieldClientAuth00042SHA256) != ca42manifest.FrozenMigrationSHA256 ||
		snapshot.Value(FieldIsolatedTargetImageID) != "sha256:"+snapshot.Value(FieldPostgresImageSHA256) ||
		!isolatedRunRE.MatchString(snapshot.Value(FieldIsolatedTargetRunID)) ||
		snapshot.Value(FieldProductionSourceContainerID) == snapshot.Value(FieldIsolatedTargetContainerID) ||
		snapshot.Value(FieldProductionSourceSystemIdentifier) == snapshot.Value(FieldIsolatedTargetSystemIdentifier) ||
		snapshot.Value(FieldProductionSourceDatabase) != snapshot.Value(FieldIsolatedTargetDatabase) {
		return errors.New("release manifest v3 source/isolated identity invalid")
	}
	if hasDuplicateHash(snapshot, hashFields) {
		return errors.New("release manifest v3 artifact separation invalid")
	}
	notBefore, err := canonicalEpoch(snapshot.Value(FieldNotBeforeEpoch))
	if err != nil {
		return errors.New("release manifest v3 not-before invalid")
	}
	notAfter, err := canonicalEpoch(snapshot.Value(FieldNotAfterEpoch))
	if err != nil || notAfter <= notBefore || notAfter-notBefore > int64(MaxValidity/time.Second) {
		return errors.New("release manifest v3 validity invalid")
	}
	nowEpoch := now.UTC().Unix()
	if now.IsZero() || nowEpoch < notBefore || nowEpoch >= notAfter {
		return errors.New("release manifest v3 outside validity window")
	}
	return nil
}

func validateExternalSnapshot(snapshot Snapshot, externalBytes []byte) error {
	external, err := ca42manifest.Verify(externalBytes)
	if err != nil || external.Decision != "STRUCTURALLY_VALID" || external.Authorization != "NONE" ||
		external.ContractSHA256 != ca42manifest.ContractSHA256 ||
		external.ExternalManifestSHA256 != snapshot.Value(FieldExternalManifestSHA256) ||
		external.ExactObjectManifestSHA256 != snapshot.Value(FieldExternalObjectManifestSHA256) ||
		external.IsolatedContainerID != snapshot.Value(FieldIsolatedTargetContainerID) ||
		external.IsolatedSystemIdentifier != snapshot.Value(FieldIsolatedTargetSystemIdentifier) ||
		external.IsolatedNetworkID != snapshot.Value(FieldIsolatedTargetNetworkID) ||
		external.IsolatedDatabase != snapshot.Value(FieldIsolatedTargetDatabase) ||
		external.IsolatedDatabaseOID != snapshot.Value(FieldIsolatedTargetDatabaseOID) ||
		external.IsolatedImageID != snapshot.Value(FieldIsolatedTargetImageID) ||
		external.IsolatedRunID != snapshot.Value(FieldIsolatedTargetRunID) {
		return errors.New("release manifest v3 external manifest binding mismatch")
	}
	return nil
}

func (manifest Manifest) VerifiedCopyAt(now time.Time) (Manifest, error) {
	if !manifest.parsed || len(manifest.canonical) == 0 || len(manifest.externalBytes) == 0 ||
		manifest.parseNow.IsZero() || manifest.architecture == "" || manifest.sha256 == ([sha256.Size]byte{}) {
		return Manifest{}, errors.New("release manifest v3 capability invalid")
	}
	digest := sha256.Sum256(manifest.canonical)
	if digest != manifest.sha256 {
		return Manifest{}, errors.New("release manifest v3 identity changed")
	}
	authority := manifest.authority
	authority.PublicKey = append(ed25519.PublicKey(nil), manifest.authority.PublicKey...)
	return ParseAndVerify(manifest.canonical, authority, manifest.architecture, now, manifest.externalBytes)
}

func (manifest Manifest) SnapshotAt(now time.Time) (Snapshot, error) {
	verified, err := manifest.VerifiedCopyAt(now)
	if err != nil {
		return Snapshot{}, err
	}
	return verified.snapshot, nil
}

func (manifest Manifest) SHA256Hex(now time.Time) (string, error) {
	verified, err := manifest.VerifiedCopyAt(now)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(verified.sha256[:]), nil
}

func UnsignedCanonicalBytes(values []string) ([]byte, error) {
	if len(values) != int(fieldCount)-1 {
		return nil, errors.New("release manifest v3 unsigned field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("release manifest v3 unsigned value invalid")
		}
		if output.Len()+len(fieldNames[index])+len(value)+2 > MaxManifestBytes {
			return nil, errors.New("release manifest v3 unsigned size invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func SignedCanonicalBytes(unsigned, signature []byte) ([]byte, error) {
	if len(unsigned) == 0 || len(unsigned) > MaxManifestBytes || unsigned[len(unsigned)-1] != '\n' || len(signature) != ed25519.SignatureSize ||
		len(unsigned)+len(fieldNames[FieldSignatureB64])+2+base64.StdEncoding.EncodedLen(len(signature)) > MaxManifestBytes {
		return nil, errors.New("release manifest v3 signed input invalid")
	}
	prefix := []byte(fieldNames[FieldSignatureB64] + "=")
	result := make([]byte, 0, len(unsigned)+len(prefix)+base64.StdEncoding.EncodedLen(len(signature))+1)
	result = append(result, unsigned...)
	result = append(result, prefix...)
	result = append(result, base64.StdEncoding.EncodeToString(signature)...)
	result = append(result, '\n')
	return result, nil
}

func SignatureMessage(unsigned []byte) ([]byte, error) {
	if len(unsigned) == 0 || len(unsigned) > MaxManifestBytes || unsigned[len(unsigned)-1] != '\n' || bytes.IndexByte(unsigned, 0) >= 0 ||
		bytes.IndexByte(unsigned, '\r') >= 0 || !utf8.Valid(unsigned) {
		return nil, errors.New("release manifest v3 signature input invalid")
	}
	return signatureMessage(unsigned), nil
}

func signatureMessage(unsigned []byte) []byte {
	message := make([]byte, 0, len(signatureDomain)+len(unsigned))
	message = append(message, signatureDomain...)
	message = append(message, unsigned...)
	return message
}

func nonZeroHex64(value string) bool {
	return hex64RE.MatchString(value) && value != strings.Repeat("0", 64)
}

func constantHashEqual(value string, expected [sha256.Size]byte) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && subtle.ConstantTimeCompare(decoded, expected[:]) == 1
}

func canonicalPositive(value string, bits int) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func canonicalBoundedSize(value string, maximum uint64) bool {
	if !canonicalPositive(value, 64) {
		return false
	}
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed <= maximum
}

func canonicalEpoch(value string) (int64, error) {
	if value == "" || value[0] == '0' || len(value) > 10 {
		return 0, errors.New("epoch invalid")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("epoch invalid")
	}
	return parsed, nil
}

func hasDuplicateHash(snapshot Snapshot, fields []Field) bool {
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		value := snapshot.Value(field)
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
