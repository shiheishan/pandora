package ca42capsulev2

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
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
	Format          = ca42protocolv2.TrustCapsuleFormat
	MaxCapsuleBytes = 96 << 10
)

var (
	hex64RE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tokenRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifierRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	isolatedRunRE = regexp.MustCompile(`^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$`)
)

type Field uint8

const (
	FieldFormat Field = iota
	FieldProfileID
	FieldProfileSHA256
	FieldTransition
	FieldReleaseID
	FieldReleaseRunID
	FieldAttemptID
	FieldArchitecture
	FieldCredentialSourceDescriptorSHA256
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
	FieldPreflightRunnerSHA256
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
	FieldSourceContainerID
	FieldSourceSystemIdentifier
	FieldSourceDatabaseName
	FieldSourceDatabaseOID
	FieldSourceDatabaseOwnerOID
	FieldSourceDatabaseOwnerName
	FieldSourceGooseWaterline
	FieldIsolatedContainerID
	FieldIsolatedSystemIdentifier
	FieldIsolatedNetworkID
	FieldIsolatedDatabaseName
	FieldIsolatedDatabaseOID
	FieldIsolatedImageID
	FieldIsolatedRunID
	FieldLedgerNamespace
	FieldLedgerDirectorySHA256
	fieldCount
)

var fieldNames = [fieldCount]string{
	"format", "profile_id", "profile_sha256", "transition", "release_id", "release_run_id", "attempt_id", "architecture",
	"credential_source_descriptor_sha256", "attestation_core_sha256", "attestation_core_chain_sha256",
	"attestation_core_device", "attestation_core_mode", "attestation_format", "attestation_sha256",
	"expected_format", "expected_sha256", "attestation_public_key_sha256", "external_manifest_sha256",
	"external_object_manifest_sha256", "bash_binary_sha256", "bash_chain_sha256", "bash_device", "bash_mode",
	"docker_client_sha256", "docker_client_chain_sha256", "docker_client_device", "docker_client_mode",
	"runtime_closure_manifest_sha256", "preflight_runner_sha256", "goose_binary_sha256", "goose_version",
	"goose_build_info_sha256", "migration_set_sha256", "client_auth_00042_sha256", "artifact_storage_profile",
	"artifact_storage_descriptor_sha256", "globals_dump_sha256", "globals_dump_size_bytes", "database_dump_sha256",
	"database_dump_size_bytes", "postgres_image_sha256", "source_container_id", "source_system_identifier",
	"source_database_name", "source_database_oid", "source_database_owner_oid", "source_database_owner_name",
	"source_goose_waterline", "isolated_container_id", "isolated_system_identifier", "isolated_network_id",
	"isolated_database_name", "isolated_database_oid", "isolated_image_id", "isolated_run_id",
	"ledger_namespace", "ledger_directory_sha256",
}

type Snapshot struct{ values [fieldCount]string }

func (snapshot Snapshot) Value(field Field) string {
	if field >= fieldCount {
		return ""
	}
	return snapshot.values[field]
}

func (snapshot Snapshot) ValueName(name string) string {
	for field, candidate := range fieldNames {
		if candidate == name {
			return snapshot.values[field]
		}
	}
	return ""
}

func FieldNames() []string { return append([]string(nil), fieldNames[:]...) }

type Capsule struct {
	snapshot  Snapshot
	sha256    [sha256.Size]byte
	canonical []byte
	parsed    bool
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte) (Capsule, error) {
	var empty Capsule
	if len(data) == 0 || len(data) > MaxCapsuleBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("trust capsule v2 envelope invalid")
	}
	canonical := append([]byte(nil), data...)
	digest := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("trust capsule v2 identity mismatch")
	}
	lines := bytes.Split(canonical[:len(canonical)-1], []byte{'\n'})
	if len(lines) != int(fieldCount) {
		return empty, errors.New("trust capsule v2 field count invalid")
	}
	var snapshot Snapshot
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("trust capsule v2 field order invalid: %s", name)
		}
		snapshot.values[index] = string(lines[index][len(prefix):])
	}
	if err := validate(snapshot); err != nil {
		return empty, err
	}
	return Capsule{snapshot: snapshot, sha256: digest, canonical: canonical, parsed: true}, nil
}

func validate(snapshot Snapshot) error {
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		return err
	}
	if snapshot.Value(FieldFormat) != Format || snapshot.Value(FieldProfileID) != ca42protocolv2.ProfileID ||
		snapshot.Value(FieldProfileSHA256) != hex.EncodeToString(profileSHA[:]) ||
		snapshot.Value(FieldTransition) != ca42executionv2.Transition ||
		(snapshot.Value(FieldArchitecture) != "amd64" && snapshot.Value(FieldArchitecture) != "arm64") ||
		snapshot.Value(FieldAttestationCoreMode) != ca42executionv2.RequiredAttemptExecutableMode ||
		snapshot.Value(FieldAttestationFormat) != ca42protocolv2.AttestationFormat ||
		snapshot.Value(FieldExpectedFormat) != ca42protocolv2.ExpectedFormat ||
		snapshot.Value(FieldBashMode) != ca42executionv2.RequiredSystemExecutableMode ||
		snapshot.Value(FieldDockerClientMode) != ca42executionv2.RequiredSystemExecutableMode ||
		snapshot.Value(FieldGooseVersion) != ca42executionv2.RequiredGooseVersion ||
		snapshot.Value(FieldClientAuth00042SHA256) != ca42manifest.FrozenMigrationSHA256 ||
		snapshot.Value(FieldArtifactStorageProfile) != ca42executionv2.RequiredStorageProfile ||
		snapshot.Value(FieldSourceGooseWaterline) != "41" ||
		snapshot.Value(FieldLedgerNamespace) != ca42protocolv2.LedgerNamespace ||
		snapshot.Value(FieldLedgerDirectorySHA256) != ca42executionv2.LedgerDirectorySHA256 {
		return errors.New("trust capsule v2 fixed field mismatch")
	}
	for _, field := range []Field{FieldProfileID, FieldReleaseID, FieldReleaseRunID, FieldAttemptID, FieldLedgerNamespace} {
		if !tokenRE.MatchString(snapshot.Value(field)) {
			return fmt.Errorf("trust capsule v2 token invalid: %s", fieldNames[field])
		}
	}
	hashes := []Field{
		FieldProfileSHA256, FieldCredentialSourceDescriptorSHA256, FieldAttestationCoreSHA256,
		FieldAttestationCoreChainSHA256, FieldAttestationSHA256, FieldExpectedSHA256,
		FieldAttestationPublicKeySHA256, FieldExternalManifestSHA256, FieldExternalObjectManifestSHA256,
		FieldBashBinarySHA256, FieldBashChainSHA256, FieldDockerClientSHA256, FieldDockerClientChainSHA256,
		FieldRuntimeClosureManifestSHA256, FieldPreflightRunnerSHA256, FieldGooseBinarySHA256,
		FieldGooseBuildInfoSHA256, FieldMigrationSetSHA256, FieldClientAuth00042SHA256,
		FieldArtifactStorageDescriptorSHA256, FieldGlobalsDumpSHA256, FieldDatabaseDumpSHA256,
		FieldPostgresImageSHA256, FieldSourceContainerID, FieldIsolatedContainerID,
		FieldIsolatedNetworkID, FieldLedgerDirectorySHA256,
	}
	for _, field := range hashes {
		if !nonZeroHex64(snapshot.Value(field)) {
			return fmt.Errorf("trust capsule v2 SHA256 invalid: %s", fieldNames[field])
		}
	}
	if duplicateHash(snapshot, []Field{
		FieldCredentialSourceDescriptorSHA256, FieldAttestationCoreSHA256, FieldAttestationCoreChainSHA256,
		FieldAttestationSHA256, FieldExpectedSHA256, FieldAttestationPublicKeySHA256,
		FieldExternalManifestSHA256, FieldExternalObjectManifestSHA256, FieldBashBinarySHA256,
		FieldBashChainSHA256, FieldDockerClientSHA256, FieldDockerClientChainSHA256,
		FieldRuntimeClosureManifestSHA256, FieldPreflightRunnerSHA256, FieldGooseBinarySHA256,
		FieldGooseBuildInfoSHA256, FieldMigrationSetSHA256, FieldClientAuth00042SHA256,
		FieldArtifactStorageDescriptorSHA256, FieldGlobalsDumpSHA256, FieldDatabaseDumpSHA256,
		FieldPostgresImageSHA256,
	}) {
		return errors.New("trust capsule v2 artifact separation invalid")
	}
	for _, field := range []Field{FieldAttestationCoreDevice, FieldBashDevice, FieldDockerClientDevice, FieldSourceSystemIdentifier, FieldIsolatedSystemIdentifier} {
		if !canonicalPositive(snapshot.Value(field), 64) {
			return fmt.Errorf("trust capsule v2 integer invalid: %s", fieldNames[field])
		}
	}
	for _, field := range []Field{FieldSourceDatabaseOID, FieldSourceDatabaseOwnerOID, FieldIsolatedDatabaseOID} {
		if !canonicalPositive(snapshot.Value(field), 32) {
			return fmt.Errorf("trust capsule v2 OID invalid: %s", fieldNames[field])
		}
	}
	for _, field := range []Field{FieldSourceDatabaseName, FieldSourceDatabaseOwnerName, FieldIsolatedDatabaseName} {
		if !identifierRE.MatchString(snapshot.Value(field)) {
			return fmt.Errorf("trust capsule v2 identifier invalid: %s", fieldNames[field])
		}
	}
	if !boundedSize(snapshot.Value(FieldGlobalsDumpSizeBytes), ca42executionv2.MaxGlobalsDumpBytes) ||
		!boundedSize(snapshot.Value(FieldDatabaseDumpSizeBytes), ca42executionv2.MaxDatabaseDumpBytes) ||
		snapshot.Value(FieldIsolatedImageID) != "sha256:"+snapshot.Value(FieldPostgresImageSHA256) ||
		!isolatedRunRE.MatchString(snapshot.Value(FieldIsolatedRunID)) ||
		snapshot.Value(FieldSourceContainerID) == snapshot.Value(FieldIsolatedContainerID) ||
		snapshot.Value(FieldSourceSystemIdentifier) == snapshot.Value(FieldIsolatedSystemIdentifier) ||
		snapshot.Value(FieldSourceDatabaseName) != snapshot.Value(FieldIsolatedDatabaseName) {
		return errors.New("trust capsule v2 source/isolated identity invalid")
	}
	return nil
}

func BindPlan(capsule Capsule, plan ca42executionv2.Plan, now time.Time) error {
	trustedCapsule, err := capsule.VerifiedCopy()
	if err != nil {
		return err
	}
	trustedPlan, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	planCapsuleSHA := hex.EncodeToString(trustedCapsule.sha256[:])
	s := trustedCapsule.snapshot
	pairs := []struct{ capsule, plan string }{
		{planCapsuleSHA, trustedPlan.TrustCapsuleSHA256}, {s.Value(FieldProfileID), trustedPlan.ProfileID},
		{s.Value(FieldProfileSHA256), trustedPlan.ProfileSHA256}, {s.Value(FieldReleaseID), trustedPlan.ReleaseID},
		{s.Value(FieldReleaseRunID), trustedPlan.ReleaseRunID}, {s.Value(FieldAttemptID), trustedPlan.AttemptID},
		{s.Value(FieldArchitecture), trustedPlan.Architecture},
		{s.Value(FieldCredentialSourceDescriptorSHA256), trustedPlan.CredentialSourceDescriptorSHA256},
		{s.Value(FieldAttestationCoreSHA256), trustedPlan.AttestationCoreSHA256},
		{s.Value(FieldAttestationCoreChainSHA256), trustedPlan.AttestationCoreChainSHA256},
		{s.Value(FieldAttestationCoreDevice), strconv.FormatUint(trustedPlan.AttestationCoreDevice, 10)},
		{s.Value(FieldAttestationSHA256), trustedPlan.AttestationSHA256}, {s.Value(FieldExpectedSHA256), trustedPlan.ExpectedSHA256},
		{s.Value(FieldAttestationPublicKeySHA256), trustedPlan.AttestationPublicKeySHA256},
		{s.Value(FieldExternalManifestSHA256), trustedPlan.ExternalManifestSHA256},
		{s.Value(FieldBashBinarySHA256), trustedPlan.BashBinarySHA256}, {s.Value(FieldBashChainSHA256), trustedPlan.BashChainSHA256},
		{s.Value(FieldBashDevice), strconv.FormatUint(trustedPlan.BashDevice, 10)},
		{s.Value(FieldDockerClientSHA256), trustedPlan.DockerClientSHA256}, {s.Value(FieldDockerClientChainSHA256), trustedPlan.DockerClientChainSHA256},
		{s.Value(FieldDockerClientDevice), strconv.FormatUint(trustedPlan.DockerClientDevice, 10)},
		{s.Value(FieldRuntimeClosureManifestSHA256), trustedPlan.RuntimeClosureManifestSHA256},
		{s.Value(FieldPreflightRunnerSHA256), trustedPlan.PreflightRunnerSHA256},
		{s.Value(FieldGooseBinarySHA256), trustedPlan.GooseBinarySHA256}, {s.Value(FieldGooseBuildInfoSHA256), trustedPlan.GooseBuildInfoSHA256},
		{s.Value(FieldMigrationSetSHA256), trustedPlan.MigrationSetSHA256}, {s.Value(FieldClientAuth00042SHA256), trustedPlan.ClientAuth00042SHA256},
		{s.Value(FieldArtifactStorageDescriptorSHA256), trustedPlan.ArtifactStorageDescriptorSHA256},
		{s.Value(FieldGlobalsDumpSHA256), trustedPlan.GlobalsDumpSHA256}, {s.Value(FieldGlobalsDumpSizeBytes), strconv.FormatUint(trustedPlan.GlobalsDumpSizeBytes, 10)},
		{s.Value(FieldDatabaseDumpSHA256), trustedPlan.DatabaseDumpSHA256}, {s.Value(FieldDatabaseDumpSizeBytes), strconv.FormatUint(trustedPlan.DatabaseDumpSizeBytes, 10)},
		{s.Value(FieldPostgresImageSHA256), trustedPlan.PostgresImageSHA256},
		{s.Value(FieldSourceContainerID), trustedPlan.SourceContainerID}, {s.Value(FieldSourceSystemIdentifier), trustedPlan.SourceSystemIdentifier},
		{s.Value(FieldSourceDatabaseName), trustedPlan.SourceDatabase}, {s.Value(FieldSourceDatabaseOID), trustedPlan.SourceDatabaseOID},
		{s.Value(FieldSourceDatabaseOwnerOID), trustedPlan.SourceDatabaseOwnerOID}, {s.Value(FieldSourceDatabaseOwnerName), trustedPlan.SourceDatabaseOwner},
		{s.Value(FieldIsolatedContainerID), trustedPlan.IsolatedContainerID}, {s.Value(FieldIsolatedSystemIdentifier), trustedPlan.IsolatedSystemIdentifier},
		{s.Value(FieldIsolatedNetworkID), trustedPlan.IsolatedNetworkID}, {s.Value(FieldIsolatedDatabaseName), trustedPlan.IsolatedDatabase},
		{s.Value(FieldIsolatedDatabaseOID), trustedPlan.IsolatedDatabaseOID}, {s.Value(FieldIsolatedImageID), trustedPlan.IsolatedImageID},
		{s.Value(FieldIsolatedRunID), trustedPlan.IsolatedRunID},
	}
	for _, pair := range pairs {
		if pair.capsule != pair.plan {
			return errors.New("trust capsule v2 and execution plan v2 binding mismatch")
		}
	}
	return nil
}

func (capsule Capsule) VerifiedCopy() (Capsule, error) {
	if !capsule.parsed || len(capsule.canonical) == 0 || capsule.sha256 == ([sha256.Size]byte{}) {
		return Capsule{}, errors.New("trust capsule v2 capability invalid")
	}
	digest := sha256.Sum256(capsule.canonical)
	if digest != capsule.sha256 {
		return Capsule{}, errors.New("trust capsule v2 identity changed")
	}
	return Parse(capsule.canonical, digest)
}

func (capsule Capsule) Snapshot() (Snapshot, error) {
	verified, err := capsule.VerifiedCopy()
	if err != nil {
		return Snapshot{}, err
	}
	return verified.snapshot, nil
}

func (capsule Capsule) SHA256Hex() (string, error) {
	verified, err := capsule.VerifiedCopy()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(verified.sha256[:]), nil
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != int(fieldCount) {
		return nil, errors.New("trust capsule v2 field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("trust capsule v2 value invalid")
		}
		if output.Len()+len(fieldNames[index])+len(value)+2 > MaxCapsuleBytes {
			return nil, errors.New("trust capsule v2 size invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func nonZeroHex64(value string) bool {
	return hex64RE.MatchString(value) && value != strings.Repeat("0", 64)
}

func canonicalPositive(value string, bits int) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func boundedSize(value string, maximum uint64) bool {
	if !canonicalPositive(value, 64) {
		return false
	}
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed <= maximum
}

func duplicateHash(snapshot Snapshot, fields []Field) bool {
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
