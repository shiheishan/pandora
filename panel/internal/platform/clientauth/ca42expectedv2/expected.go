package ca42expectedv2

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
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
)

const (
	Format                        = ca42protocolv2.ExpectedFormat
	AttestationSignatureAlgorithm = "ed25519"
	MaxExpectedBytes              = 128 << 10
	MaxValidity                   = time.Hour
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
	FieldAttestationFormat
	FieldAttestationSignatureAlgorithm
	FieldReleaseID
	FieldReleaseRunID
	FieldAttemptID
	FieldArchitecture
	FieldTransition
	FieldAttestationPublicKeySHA256
	FieldCredentialSourceDescriptorSHA256
	FieldPathtrustBinarySHA256
	FieldPathtrustChainSHA256
	FieldPathtrustDevice
	FieldPathtrustMode
	FieldAttestationCoreSHA256
	FieldAttestationCoreChainSHA256
	FieldAttestationCoreDevice
	FieldAttestationCoreMode
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
	FieldExternalManifestSHA256
	FieldExternalObjectManifestSHA256
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
	FieldNotBeforeEpoch
	FieldNotAfterEpoch
	fieldCount
)

var fieldNames = [fieldCount]string{
	"format", "profile_id", "profile_sha256", "attestation_format", "attestation_signature_algorithm",
	"release_id", "release_run_id", "attempt_id", "architecture", "transition", "attestation_public_key_sha256",
	"credential_source_descriptor_sha256", "pathtrust_binary_sha256", "pathtrust_chain_sha256", "pathtrust_device", "pathtrust_mode",
	"attestation_core_sha256", "attestation_core_chain_sha256", "attestation_core_device", "attestation_core_mode",
	"bash_binary_sha256", "bash_chain_sha256", "bash_device", "bash_mode", "docker_client_sha256", "docker_client_chain_sha256",
	"docker_client_device", "docker_client_mode", "runtime_closure_manifest_sha256", "manifest_verifier_sha256",
	"preflight_runner_sha256", "migration_runner_sha256", "goose_binary_sha256", "goose_version", "goose_build_info_sha256",
	"migration_set_sha256", "client_auth_00042_sha256", "artifact_storage_profile", "artifact_storage_descriptor_sha256",
	"globals_dump_sha256", "globals_dump_size_bytes", "database_dump_sha256", "database_dump_size_bytes", "postgres_image_sha256",
	"external_manifest_sha256", "external_object_manifest_sha256", "source_container_id", "source_system_identifier",
	"source_database_name", "source_database_oid", "source_database_owner_oid", "source_database_owner_name", "source_goose_waterline",
	"isolated_container_id", "isolated_system_identifier", "isolated_network_id", "isolated_database_name", "isolated_database_oid",
	"isolated_image_id", "isolated_run_id", "ledger_namespace", "ledger_directory_sha256", "not_before_epoch", "not_after_epoch",
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

type Expected struct {
	snapshot     Snapshot
	sha256       [sha256.Size]byte
	canonical    []byte
	architecture string
	parseNow     time.Time
	parsed       bool
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte, architecture string, now time.Time) (Expected, error) {
	var empty Expected
	if len(data) == 0 || len(data) > MaxExpectedBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("expected v2 envelope invalid")
	}
	canonical := append([]byte(nil), data...)
	digest := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("expected v2 identity mismatch")
	}
	lines := bytes.Split(canonical[:len(canonical)-1], []byte{'\n'})
	if len(lines) != int(fieldCount) {
		return empty, errors.New("expected v2 field count invalid")
	}
	var snapshot Snapshot
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("expected v2 field order invalid: %s", name)
		}
		snapshot.values[index] = string(lines[index][len(prefix):])
	}
	if err := validate(snapshot, architecture, now); err != nil {
		return empty, err
	}
	return Expected{snapshot: snapshot, sha256: digest, canonical: canonical, architecture: architecture, parseNow: now.UTC(), parsed: true}, nil
}

func validate(s Snapshot, architecture string, now time.Time) error {
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		return err
	}
	if s.Value(FieldFormat) != Format || s.Value(FieldProfileID) != ca42protocolv2.ProfileID ||
		s.Value(FieldProfileSHA256) != hex.EncodeToString(profileSHA[:]) ||
		s.Value(FieldAttestationFormat) != ca42protocolv2.AttestationFormat ||
		s.Value(FieldAttestationSignatureAlgorithm) != AttestationSignatureAlgorithm ||
		s.Value(FieldArchitecture) != architecture || (architecture != "amd64" && architecture != "arm64") ||
		s.Value(FieldTransition) != ca42executionv2.Transition ||
		s.Value(FieldPathtrustMode) != ca42executionv2.RequiredAttemptExecutableMode ||
		s.Value(FieldAttestationCoreMode) != ca42executionv2.RequiredAttemptExecutableMode ||
		s.Value(FieldBashMode) != ca42executionv2.RequiredSystemExecutableMode ||
		s.Value(FieldDockerClientMode) != ca42executionv2.RequiredSystemExecutableMode ||
		s.Value(FieldGooseVersion) != ca42executionv2.RequiredGooseVersion ||
		s.Value(FieldClientAuth00042SHA256) != ca42manifest.FrozenMigrationSHA256 ||
		s.Value(FieldArtifactStorageProfile) != ca42executionv2.RequiredStorageProfile ||
		s.Value(FieldSourceGooseWaterline) != "41" ||
		s.Value(FieldLedgerNamespace) != ca42protocolv2.LedgerNamespace ||
		s.Value(FieldLedgerDirectorySHA256) != ca42executionv2.LedgerDirectorySHA256 {
		return errors.New("expected v2 fixed field mismatch")
	}
	for _, field := range []Field{FieldProfileID, FieldReleaseID, FieldReleaseRunID, FieldAttemptID, FieldLedgerNamespace} {
		if !tokenRE.MatchString(s.Value(field)) {
			return fmt.Errorf("expected v2 token invalid: %s", fieldNames[field])
		}
	}
	hashes := []Field{
		FieldProfileSHA256, FieldAttestationPublicKeySHA256, FieldCredentialSourceDescriptorSHA256,
		FieldPathtrustBinarySHA256, FieldPathtrustChainSHA256, FieldAttestationCoreSHA256,
		FieldAttestationCoreChainSHA256, FieldBashBinarySHA256, FieldBashChainSHA256,
		FieldDockerClientSHA256, FieldDockerClientChainSHA256, FieldRuntimeClosureManifestSHA256,
		FieldManifestVerifierSHA256, FieldPreflightRunnerSHA256, FieldMigrationRunnerSHA256,
		FieldGooseBinarySHA256, FieldGooseBuildInfoSHA256, FieldMigrationSetSHA256,
		FieldClientAuth00042SHA256, FieldArtifactStorageDescriptorSHA256, FieldGlobalsDumpSHA256,
		FieldDatabaseDumpSHA256, FieldPostgresImageSHA256, FieldExternalManifestSHA256,
		FieldExternalObjectManifestSHA256, FieldSourceContainerID, FieldIsolatedContainerID,
		FieldIsolatedNetworkID, FieldLedgerDirectorySHA256,
	}
	for _, field := range hashes {
		if !nonZeroHex64(s.Value(field)) {
			return fmt.Errorf("expected v2 SHA256 invalid: %s", fieldNames[field])
		}
	}
	if duplicateHash(s, []Field{
		FieldAttestationPublicKeySHA256, FieldCredentialSourceDescriptorSHA256, FieldPathtrustBinarySHA256,
		FieldPathtrustChainSHA256, FieldAttestationCoreSHA256, FieldAttestationCoreChainSHA256,
		FieldBashBinarySHA256, FieldBashChainSHA256, FieldDockerClientSHA256, FieldDockerClientChainSHA256,
		FieldRuntimeClosureManifestSHA256, FieldManifestVerifierSHA256, FieldPreflightRunnerSHA256,
		FieldMigrationRunnerSHA256, FieldGooseBinarySHA256, FieldGooseBuildInfoSHA256,
		FieldMigrationSetSHA256, FieldClientAuth00042SHA256, FieldArtifactStorageDescriptorSHA256,
		FieldGlobalsDumpSHA256, FieldDatabaseDumpSHA256, FieldPostgresImageSHA256,
		FieldExternalManifestSHA256, FieldExternalObjectManifestSHA256,
	}) {
		return errors.New("expected v2 artifact separation invalid")
	}
	for _, field := range []Field{FieldPathtrustDevice, FieldAttestationCoreDevice, FieldBashDevice, FieldDockerClientDevice, FieldSourceSystemIdentifier, FieldIsolatedSystemIdentifier} {
		if !canonicalPositive(s.Value(field), 64) {
			return fmt.Errorf("expected v2 integer invalid: %s", fieldNames[field])
		}
	}
	for _, field := range []Field{FieldSourceDatabaseOID, FieldSourceDatabaseOwnerOID, FieldIsolatedDatabaseOID} {
		if !canonicalPositive(s.Value(field), 32) {
			return fmt.Errorf("expected v2 OID invalid: %s", fieldNames[field])
		}
	}
	for _, field := range []Field{FieldSourceDatabaseName, FieldSourceDatabaseOwnerName, FieldIsolatedDatabaseName} {
		if !identifierRE.MatchString(s.Value(field)) {
			return fmt.Errorf("expected v2 identifier invalid: %s", fieldNames[field])
		}
	}
	if !boundedSize(s.Value(FieldGlobalsDumpSizeBytes), ca42executionv2.MaxGlobalsDumpBytes) ||
		!boundedSize(s.Value(FieldDatabaseDumpSizeBytes), ca42executionv2.MaxDatabaseDumpBytes) ||
		s.Value(FieldIsolatedImageID) != "sha256:"+s.Value(FieldPostgresImageSHA256) ||
		!isolatedRunRE.MatchString(s.Value(FieldIsolatedRunID)) ||
		s.Value(FieldSourceContainerID) == s.Value(FieldIsolatedContainerID) ||
		s.Value(FieldSourceSystemIdentifier) == s.Value(FieldIsolatedSystemIdentifier) ||
		s.Value(FieldSourceDatabaseName) != s.Value(FieldIsolatedDatabaseName) {
		return errors.New("expected v2 source/isolated identity invalid")
	}
	notBefore, err := epoch(s.Value(FieldNotBeforeEpoch))
	if err != nil {
		return errors.New("expected v2 not-before invalid")
	}
	notAfter, err := epoch(s.Value(FieldNotAfterEpoch))
	if err != nil || notAfter <= notBefore || notAfter-notBefore > int64(MaxValidity/time.Second) {
		return errors.New("expected v2 validity invalid")
	}
	nowEpoch := now.UTC().Unix()
	if now.IsZero() || nowEpoch < notBefore || nowEpoch >= notAfter {
		return errors.New("expected v2 outside validity window")
	}
	return nil
}

func BindPlan(expected Expected, plan ca42executionv2.Plan, now time.Time) error {
	e, err := expected.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	p, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	s := e.snapshot
	pairs := []struct{ expected, plan string }{
		{hex.EncodeToString(e.sha256[:]), p.ExpectedSHA256}, {s.Value(FieldProfileID), p.ProfileID}, {s.Value(FieldProfileSHA256), p.ProfileSHA256},
		{s.Value(FieldReleaseID), p.ReleaseID}, {s.Value(FieldReleaseRunID), p.ReleaseRunID}, {s.Value(FieldAttemptID), p.AttemptID}, {s.Value(FieldArchitecture), p.Architecture},
		{s.Value(FieldAttestationPublicKeySHA256), p.AttestationPublicKeySHA256}, {s.Value(FieldCredentialSourceDescriptorSHA256), p.CredentialSourceDescriptorSHA256},
		{s.Value(FieldPathtrustBinarySHA256), p.PathtrustBinarySHA256}, {s.Value(FieldPathtrustChainSHA256), p.PathtrustChainSHA256}, {s.Value(FieldPathtrustDevice), strconv.FormatUint(p.PathtrustDevice, 10)},
		{s.Value(FieldAttestationCoreSHA256), p.AttestationCoreSHA256}, {s.Value(FieldAttestationCoreChainSHA256), p.AttestationCoreChainSHA256}, {s.Value(FieldAttestationCoreDevice), strconv.FormatUint(p.AttestationCoreDevice, 10)},
		{s.Value(FieldBashBinarySHA256), p.BashBinarySHA256}, {s.Value(FieldBashChainSHA256), p.BashChainSHA256}, {s.Value(FieldBashDevice), strconv.FormatUint(p.BashDevice, 10)},
		{s.Value(FieldDockerClientSHA256), p.DockerClientSHA256}, {s.Value(FieldDockerClientChainSHA256), p.DockerClientChainSHA256}, {s.Value(FieldDockerClientDevice), strconv.FormatUint(p.DockerClientDevice, 10)},
		{s.Value(FieldRuntimeClosureManifestSHA256), p.RuntimeClosureManifestSHA256}, {s.Value(FieldManifestVerifierSHA256), p.ManifestVerifierSHA256},
		{s.Value(FieldPreflightRunnerSHA256), p.PreflightRunnerSHA256}, {s.Value(FieldMigrationRunnerSHA256), p.MigrationRunnerSHA256},
		{s.Value(FieldGooseBinarySHA256), p.GooseBinarySHA256}, {s.Value(FieldGooseBuildInfoSHA256), p.GooseBuildInfoSHA256},
		{s.Value(FieldMigrationSetSHA256), p.MigrationSetSHA256}, {s.Value(FieldClientAuth00042SHA256), p.ClientAuth00042SHA256},
		{s.Value(FieldArtifactStorageDescriptorSHA256), p.ArtifactStorageDescriptorSHA256}, {s.Value(FieldGlobalsDumpSHA256), p.GlobalsDumpSHA256},
		{s.Value(FieldGlobalsDumpSizeBytes), strconv.FormatUint(p.GlobalsDumpSizeBytes, 10)}, {s.Value(FieldDatabaseDumpSHA256), p.DatabaseDumpSHA256},
		{s.Value(FieldDatabaseDumpSizeBytes), strconv.FormatUint(p.DatabaseDumpSizeBytes, 10)}, {s.Value(FieldPostgresImageSHA256), p.PostgresImageSHA256},
		{s.Value(FieldExternalManifestSHA256), p.ExternalManifestSHA256}, {s.Value(FieldSourceContainerID), p.SourceContainerID},
		{s.Value(FieldSourceSystemIdentifier), p.SourceSystemIdentifier}, {s.Value(FieldSourceDatabaseName), p.SourceDatabase}, {s.Value(FieldSourceDatabaseOID), p.SourceDatabaseOID},
		{s.Value(FieldSourceDatabaseOwnerOID), p.SourceDatabaseOwnerOID}, {s.Value(FieldSourceDatabaseOwnerName), p.SourceDatabaseOwner},
		{s.Value(FieldIsolatedContainerID), p.IsolatedContainerID}, {s.Value(FieldIsolatedSystemIdentifier), p.IsolatedSystemIdentifier},
		{s.Value(FieldIsolatedNetworkID), p.IsolatedNetworkID}, {s.Value(FieldIsolatedDatabaseName), p.IsolatedDatabase}, {s.Value(FieldIsolatedDatabaseOID), p.IsolatedDatabaseOID},
		{s.Value(FieldIsolatedImageID), p.IsolatedImageID}, {s.Value(FieldIsolatedRunID), p.IsolatedRunID},
		{s.Value(FieldNotBeforeEpoch), strconv.FormatInt(p.NotBefore.Unix(), 10)}, {s.Value(FieldNotAfterEpoch), strconv.FormatInt(p.NotAfter.Unix(), 10)},
	}
	for _, pair := range pairs {
		if pair.expected != pair.plan {
			return errors.New("expected v2 and execution plan v2 binding mismatch")
		}
	}
	return nil
}

func BindRelease(expected Expected, release ca42releasev3.Manifest, now time.Time) error {
	e, err := expected.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	r, err := release.SnapshotAt(now)
	if err != nil {
		return err
	}
	s := e.snapshot
	pairs := []struct{ expected, release string }{
		{hex.EncodeToString(e.sha256[:]), r.Value(ca42releasev3.FieldExpectedSHA256)}, {s.Value(FieldFormat), r.Value(ca42releasev3.FieldExpectedFormat)},
		{s.Value(FieldProfileID), r.Value(ca42releasev3.FieldProfileID)}, {s.Value(FieldProfileSHA256), r.Value(ca42releasev3.FieldProfileSHA256)},
		{s.Value(FieldReleaseID), r.Value(ca42releasev3.FieldReleaseID)}, {s.Value(FieldReleaseRunID), r.Value(ca42releasev3.FieldReleaseRunID)},
		{s.Value(FieldAttemptID), r.Value(ca42releasev3.FieldAttemptID)}, {s.Value(FieldArchitecture), r.Value(ca42releasev3.FieldArchitecture)},
		{s.Value(FieldTransition), r.Value(ca42releasev3.FieldTransition)}, {s.Value(FieldAttestationPublicKeySHA256), r.Value(ca42releasev3.FieldAttestationPublicKeySHA256)},
		{s.Value(FieldCredentialSourceDescriptorSHA256), r.Value(ca42releasev3.FieldCredentialSourceDescriptorSHA256)},
		{s.Value(FieldPathtrustBinarySHA256), r.Value(ca42releasev3.FieldPathtrustBinarySHA256)}, {s.Value(FieldPathtrustChainSHA256), r.Value(ca42releasev3.FieldPathtrustChainSHA256)},
		{s.Value(FieldPathtrustDevice), r.Value(ca42releasev3.FieldPathtrustDevice)}, {s.Value(FieldPathtrustMode), r.Value(ca42releasev3.FieldPathtrustMode)},
		{s.Value(FieldAttestationCoreSHA256), r.Value(ca42releasev3.FieldAttestationCoreSHA256)}, {s.Value(FieldAttestationCoreChainSHA256), r.Value(ca42releasev3.FieldAttestationCoreChainSHA256)},
		{s.Value(FieldAttestationCoreDevice), r.Value(ca42releasev3.FieldAttestationCoreDevice)}, {s.Value(FieldAttestationCoreMode), r.Value(ca42releasev3.FieldAttestationCoreMode)},
		{s.Value(FieldBashBinarySHA256), r.Value(ca42releasev3.FieldBashBinarySHA256)}, {s.Value(FieldBashChainSHA256), r.Value(ca42releasev3.FieldBashChainSHA256)},
		{s.Value(FieldBashDevice), r.Value(ca42releasev3.FieldBashDevice)}, {s.Value(FieldBashMode), r.Value(ca42releasev3.FieldBashMode)},
		{s.Value(FieldDockerClientSHA256), r.Value(ca42releasev3.FieldDockerClientSHA256)}, {s.Value(FieldDockerClientChainSHA256), r.Value(ca42releasev3.FieldDockerClientChainSHA256)},
		{s.Value(FieldDockerClientDevice), r.Value(ca42releasev3.FieldDockerClientDevice)}, {s.Value(FieldDockerClientMode), r.Value(ca42releasev3.FieldDockerClientMode)},
		{s.Value(FieldRuntimeClosureManifestSHA256), r.Value(ca42releasev3.FieldRuntimeClosureManifestSHA256)},
		{s.Value(FieldManifestVerifierSHA256), r.Value(ca42releasev3.FieldManifestVerifierSHA256)}, {s.Value(FieldPreflightRunnerSHA256), r.Value(ca42releasev3.FieldPreflightRunnerSHA256)},
		{s.Value(FieldMigrationRunnerSHA256), r.Value(ca42releasev3.FieldMigrationRunnerSHA256)}, {s.Value(FieldGooseBinarySHA256), r.Value(ca42releasev3.FieldGooseBinarySHA256)},
		{s.Value(FieldGooseBuildInfoSHA256), r.Value(ca42releasev3.FieldGooseBuildInfoSHA256)}, {s.Value(FieldMigrationSetSHA256), r.Value(ca42releasev3.FieldMigrationSetSHA256)},
		{s.Value(FieldClientAuth00042SHA256), r.Value(ca42releasev3.FieldClientAuth00042SHA256)},
		{s.Value(FieldArtifactStorageProfile), r.Value(ca42releasev3.FieldArtifactStorageProfile)}, {s.Value(FieldArtifactStorageDescriptorSHA256), r.Value(ca42releasev3.FieldArtifactStorageDescriptorSHA256)},
		{s.Value(FieldGlobalsDumpSHA256), r.Value(ca42releasev3.FieldGlobalsDumpSHA256)}, {s.Value(FieldGlobalsDumpSizeBytes), r.Value(ca42releasev3.FieldGlobalsDumpSizeBytes)},
		{s.Value(FieldDatabaseDumpSHA256), r.Value(ca42releasev3.FieldDatabaseDumpSHA256)}, {s.Value(FieldDatabaseDumpSizeBytes), r.Value(ca42releasev3.FieldDatabaseDumpSizeBytes)},
		{s.Value(FieldPostgresImageSHA256), r.Value(ca42releasev3.FieldPostgresImageSHA256)}, {s.Value(FieldExternalManifestSHA256), r.Value(ca42releasev3.FieldExternalManifestSHA256)},
		{s.Value(FieldExternalObjectManifestSHA256), r.Value(ca42releasev3.FieldExternalObjectManifestSHA256)},
		{s.Value(FieldSourceContainerID), r.Value(ca42releasev3.FieldProductionSourceContainerID)}, {s.Value(FieldSourceSystemIdentifier), r.Value(ca42releasev3.FieldProductionSourceSystemIdentifier)},
		{s.Value(FieldSourceDatabaseName), r.Value(ca42releasev3.FieldProductionSourceDatabase)}, {s.Value(FieldSourceDatabaseOID), r.Value(ca42releasev3.FieldProductionSourceDatabaseOID)},
		{s.Value(FieldSourceDatabaseOwnerOID), r.Value(ca42releasev3.FieldProductionSourceDatabaseOwnerOID)}, {s.Value(FieldSourceDatabaseOwnerName), r.Value(ca42releasev3.FieldProductionSourceDatabaseOwnerName)},
		{s.Value(FieldSourceGooseWaterline), r.Value(ca42releasev3.FieldProductionSourceGooseWaterline)},
		{s.Value(FieldIsolatedContainerID), r.Value(ca42releasev3.FieldIsolatedTargetContainerID)}, {s.Value(FieldIsolatedSystemIdentifier), r.Value(ca42releasev3.FieldIsolatedTargetSystemIdentifier)},
		{s.Value(FieldIsolatedNetworkID), r.Value(ca42releasev3.FieldIsolatedTargetNetworkID)}, {s.Value(FieldIsolatedDatabaseName), r.Value(ca42releasev3.FieldIsolatedTargetDatabase)},
		{s.Value(FieldIsolatedDatabaseOID), r.Value(ca42releasev3.FieldIsolatedTargetDatabaseOID)}, {s.Value(FieldIsolatedImageID), r.Value(ca42releasev3.FieldIsolatedTargetImageID)},
		{s.Value(FieldIsolatedRunID), r.Value(ca42releasev3.FieldIsolatedTargetRunID)}, {s.Value(FieldLedgerNamespace), r.Value(ca42releasev3.FieldLedgerNamespace)},
		{s.Value(FieldLedgerDirectorySHA256), r.Value(ca42releasev3.FieldLedgerDirectorySHA256)},
		{s.Value(FieldNotBeforeEpoch), r.Value(ca42releasev3.FieldNotBeforeEpoch)}, {s.Value(FieldNotAfterEpoch), r.Value(ca42releasev3.FieldNotAfterEpoch)},
	}
	for _, pair := range pairs {
		if pair.expected != pair.release {
			return errors.New("expected v2 and release manifest v3 binding mismatch")
		}
	}
	return nil
}

func (expected Expected) VerifiedCopyAt(now time.Time) (Expected, error) {
	if !expected.parsed || len(expected.canonical) == 0 || expected.architecture == "" || expected.parseNow.IsZero() || expected.sha256 == ([sha256.Size]byte{}) {
		return Expected{}, errors.New("expected v2 capability invalid")
	}
	digest := sha256.Sum256(expected.canonical)
	if digest != expected.sha256 {
		return Expected{}, errors.New("expected v2 identity changed")
	}
	return Parse(expected.canonical, digest, expected.architecture, now)
}

func (expected Expected) SnapshotAt(now time.Time) (Snapshot, error) {
	verified, err := expected.VerifiedCopyAt(now)
	if err != nil {
		return Snapshot{}, err
	}
	return verified.snapshot, nil
}

func (expected Expected) SHA256Hex(now time.Time) (string, error) {
	verified, err := expected.VerifiedCopyAt(now)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(verified.sha256[:]), nil
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != int(fieldCount) {
		return nil, errors.New("expected v2 field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("expected v2 value invalid")
		}
		if output.Len()+len(fieldNames[index])+len(value)+2 > MaxExpectedBytes {
			return nil, errors.New("expected v2 size invalid")
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
