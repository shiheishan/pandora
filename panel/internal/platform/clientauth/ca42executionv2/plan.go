package ca42executionv2

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

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

const (
	Format                        = "pandora-ca42-execution-plan-v2"
	Status                        = "SEALED_FOR_ROOT_RUNNER"
	Transition                    = "goose-41-to-42"
	LedgerNamespace               = "client-auth-00042-v2"
	LedgerDirectorySHA256         = "e2080312e2443e215abfe18900bbcc8fb9b78a53d10121aaa80c1d0ada396283"
	RequiredAttemptExecutableMode = "0500"
	RequiredSystemExecutableMode  = "0755"
	RequiredGooseVersion          = "v3.24.1"
	RequiredStorageProfile        = "fs-verity-sha256-v2"
	CanonicalBashPath             = "/usr/bin/bash"
	CanonicalDockerClientPath     = "/usr/bin/docker"
	CredentialDelivery            = "retained-fd-only-v1"
	RequiredProfileID             = "client-auth-00042-v2"
	RequiredProfileSHA256         = "3a7d7f5b88f204d57a7f0e25913064c756a91f665ce3906830d6a92bf484455c"
	MaxPlanBytes                  = 96 << 10
	MaxValidity                   = time.Hour
	MaxGlobalsDumpBytes           = uint64(1 << 30)
	MaxDatabaseDumpBytes          = uint64(1 << 40)
)

var (
	lowerHex64    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifier    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	isolatedRunID = regexp.MustCompile(`^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$`)
)

var fieldNames = [...]string{
	"format", "status", "transition", "release_id", "release_run_id", "attempt_id", "architecture",
	"profile_id", "profile_sha256",
	"credential_source_descriptor_sha256",
	"pathtrust_binary_sha256", "pathtrust_chain_sha256", "pathtrust_device", "pathtrust_mode",
	"trust_capsule_sha256", "attestation_core_sha256", "attestation_core_chain_sha256",
	"attestation_core_device", "attestation_core_mode", "attestation_sha256", "expected_sha256",
	"attestation_public_key_sha256", "external_manifest_sha256",
	"bash_binary_sha256", "bash_chain_sha256", "bash_device", "bash_mode",
	"docker_client_sha256", "docker_client_chain_sha256", "docker_client_device", "docker_client_mode",
	"runtime_closure_manifest_sha256",
	"manifest_verifier_sha256", "preflight_runner_sha256", "migration_runner_sha256",
	"goose_binary_sha256", "goose_version", "goose_build_info_sha256",
	"migration_set_sha256", "client_auth_00042_sha256",
	"artifact_storage_profile", "artifact_storage_descriptor_sha256",
	"globals_dump_sha256", "globals_dump_size_bytes", "database_dump_sha256", "database_dump_size_bytes",
	"postgres_image_sha256", "release_journal_head_sha256", "release_journal_snapshot_sha256",
	"ledger_namespace", "ledger_directory_sha256",
	"source_container_id", "source_system_identifier", "source_database", "source_database_oid",
	"source_database_owner_oid", "source_database_owner_name", "source_goose_waterline",
	"isolated_container_id", "isolated_system_identifier", "isolated_network_id", "isolated_database",
	"isolated_database_oid", "isolated_image_id", "isolated_run_id",
	"not_before_epoch", "not_after_epoch",
}

type Plan struct {
	ReleaseID, ReleaseRunID, AttemptID, Architecture                                      string
	ProfileID, ProfileSHA256                                                              string
	CredentialSourceDescriptorSHA256                                                      string
	PathtrustBinarySHA256, PathtrustChainSHA256                                           string
	PathtrustDevice                                                                       uint64
	TrustCapsuleSHA256                                                                    string
	AttestationCoreSHA256, AttestationCoreChainSHA256                                     string
	AttestationCoreDevice                                                                 uint64
	AttestationSHA256, ExpectedSHA256, AttestationPublicKeySHA256, ExternalManifestSHA256 string
	BashBinarySHA256, BashChainSHA256                                                     string
	BashDevice                                                                            uint64
	DockerClientSHA256, DockerClientChainSHA256                                           string
	DockerClientDevice                                                                    uint64
	RuntimeClosureManifestSHA256                                                          string
	ManifestVerifierSHA256, PreflightRunnerSHA256, MigrationRunnerSHA256                  string
	GooseBinarySHA256, GooseVersion, GooseBuildInfoSHA256                                 string
	MigrationSetSHA256, ClientAuth00042SHA256                                             string
	ArtifactStorageProfile, ArtifactStorageDescriptorSHA256                               string
	GlobalsDumpSHA256, DatabaseDumpSHA256                                                 string
	GlobalsDumpSizeBytes, DatabaseDumpSizeBytes                                           uint64
	PostgresImageSHA256, ReleaseJournalHeadSHA256, ReleaseJournalSnapshotSHA256           string
	SourceContainerID, SourceSystemIdentifier, SourceDatabase, SourceDatabaseOID          string
	SourceDatabaseOwnerOID, SourceDatabaseOwner                                           string
	IsolatedContainerID, IsolatedSystemIdentifier, IsolatedNetworkID                      string
	IsolatedDatabase, IsolatedDatabaseOID, IsolatedImageID, IsolatedRunID                 string
	NotBefore, NotAfter                                                                   time.Time
	SHA256                                                                                [sha256.Size]byte
	parsed                                                                                bool
	canonical                                                                             []byte
	parseArchitecture                                                                     string
	parseNow                                                                              time.Time
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte, architecture string, now time.Time) (Plan, error) {
	var empty Plan
	if len(data) == 0 || len(data) > MaxPlanBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("execution plan v2 envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("execution plan v2 identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("execution plan v2 field count invalid")
	}
	values := make(map[string]string, len(fieldNames))
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("execution plan v2 field order invalid: %s", name)
		}
		values[name] = string(lines[index][len(prefix):])
	}
	if values["format"] != Format || values["status"] != Status || values["transition"] != Transition ||
		values["architecture"] != architecture || (architecture != "amd64" && architecture != "arm64") ||
		values["profile_id"] != RequiredProfileID || values["profile_sha256"] != RequiredProfileSHA256 ||
		values["pathtrust_mode"] != RequiredAttemptExecutableMode ||
		values["attestation_core_mode"] != RequiredAttemptExecutableMode ||
		values["bash_mode"] != RequiredSystemExecutableMode || values["docker_client_mode"] != RequiredSystemExecutableMode ||
		values["ledger_namespace"] != LedgerNamespace || values["ledger_directory_sha256"] != LedgerDirectorySHA256 ||
		values["source_goose_waterline"] != "41" {
		return empty, errors.New("execution plan v2 fixed field mismatch")
	}
	for _, name := range []string{"release_id", "release_run_id", "attempt_id", "profile_id"} {
		if !safeToken.MatchString(values[name]) {
			return empty, fmt.Errorf("execution plan v2 token invalid: %s", name)
		}
	}
	hashFields := []string{
		"profile_sha256",
		"credential_source_descriptor_sha256", "pathtrust_binary_sha256", "pathtrust_chain_sha256",
		"trust_capsule_sha256", "attestation_core_sha256", "attestation_core_chain_sha256",
		"attestation_sha256", "expected_sha256", "attestation_public_key_sha256", "external_manifest_sha256",
		"bash_binary_sha256", "bash_chain_sha256", "docker_client_sha256", "docker_client_chain_sha256",
		"runtime_closure_manifest_sha256", "manifest_verifier_sha256", "preflight_runner_sha256",
		"migration_runner_sha256", "goose_binary_sha256", "goose_build_info_sha256", "migration_set_sha256",
		"client_auth_00042_sha256", "artifact_storage_descriptor_sha256", "globals_dump_sha256",
		"database_dump_sha256", "postgres_image_sha256", "release_journal_head_sha256",
		"release_journal_snapshot_sha256", "ledger_directory_sha256", "source_container_id",
		"isolated_container_id", "isolated_network_id",
	}
	for _, name := range hashFields {
		if !nonZeroHex64(values[name]) {
			return empty, fmt.Errorf("execution plan v2 SHA256 invalid: %s", name)
		}
	}
	if values["goose_version"] != RequiredGooseVersion {
		return empty, errors.New("execution plan v2 Goose version invalid")
	}
	if values["artifact_storage_profile"] != RequiredStorageProfile {
		return empty, errors.New("execution plan v2 storage profile invalid")
	}
	for _, name := range []string{"source_database", "source_database_owner_name", "isolated_database"} {
		if !identifier.MatchString(values[name]) {
			return empty, fmt.Errorf("execution plan v2 identifier invalid: %s", name)
		}
	}
	pathtrustDevice, err := parsePositiveUint(values["pathtrust_device"], 64)
	if err != nil {
		return empty, errors.New("execution plan v2 pathtrust device invalid")
	}
	coreDevice, err := parsePositiveUint(values["attestation_core_device"], 64)
	if err != nil {
		return empty, errors.New("execution plan v2 attestation core device invalid")
	}
	bashDevice, err := parsePositiveUint(values["bash_device"], 64)
	if err != nil {
		return empty, errors.New("execution plan v2 Bash device invalid")
	}
	dockerDevice, err := parsePositiveUint(values["docker_client_device"], 64)
	if err != nil {
		return empty, errors.New("execution plan v2 Docker device invalid")
	}
	for _, name := range []string{"source_system_identifier", "isolated_system_identifier"} {
		if _, err := parsePositiveUint(values[name], 64); err != nil {
			return empty, fmt.Errorf("execution plan v2 system identifier invalid: %s", name)
		}
	}
	for _, name := range []string{"source_database_oid", "source_database_owner_oid", "isolated_database_oid"} {
		if _, err := parsePositiveUint(values[name], 32); err != nil {
			return empty, fmt.Errorf("execution plan v2 OID invalid: %s", name)
		}
	}
	globalsSize, err := parsePositiveUint(values["globals_dump_size_bytes"], 64)
	if err != nil || globalsSize > MaxGlobalsDumpBytes {
		return empty, errors.New("execution plan v2 globals dump size invalid")
	}
	databaseSize, err := parsePositiveUint(values["database_dump_size_bytes"], 64)
	if err != nil || databaseSize > MaxDatabaseDumpBytes {
		return empty, errors.New("execution plan v2 database dump size invalid")
	}
	if values["client_auth_00042_sha256"] != ca42manifest.FrozenMigrationSHA256 ||
		values["isolated_image_id"] != "sha256:"+values["postgres_image_sha256"] ||
		!isolatedRunID.MatchString(values["isolated_run_id"]) ||
		values["source_container_id"] == values["isolated_container_id"] ||
		values["source_system_identifier"] == values["isolated_system_identifier"] ||
		values["source_database"] != values["isolated_database"] {
		return empty, errors.New("execution plan v2 source/isolated identity invalid")
	}
	if hasDuplicate(values, []string{
		"credential_source_descriptor_sha256", "pathtrust_binary_sha256", "trust_capsule_sha256",
		"attestation_core_sha256", "attestation_sha256", "expected_sha256", "attestation_public_key_sha256",
		"external_manifest_sha256", "bash_binary_sha256", "docker_client_sha256",
		"runtime_closure_manifest_sha256", "manifest_verifier_sha256", "preflight_runner_sha256",
		"migration_runner_sha256", "goose_binary_sha256", "migration_set_sha256", "client_auth_00042_sha256",
		"artifact_storage_descriptor_sha256", "globals_dump_sha256", "database_dump_sha256",
	}) {
		return empty, errors.New("execution plan v2 artifact separation invalid")
	}
	notBeforeEpoch, err := parsePositiveInt(values["not_before_epoch"])
	if err != nil {
		return empty, errors.New("execution plan v2 not-before invalid")
	}
	notAfterEpoch, err := parsePositiveInt(values["not_after_epoch"])
	if err != nil || notAfterEpoch <= notBeforeEpoch || notAfterEpoch-notBeforeEpoch > int64(MaxValidity/time.Second) {
		return empty, errors.New("execution plan v2 validity invalid")
	}
	nowEpoch := now.UTC().Unix()
	if nowEpoch < notBeforeEpoch || nowEpoch >= notAfterEpoch {
		return empty, errors.New("execution plan v2 outside validity window")
	}
	return Plan{
		ReleaseID: values["release_id"], ReleaseRunID: values["release_run_id"], AttemptID: values["attempt_id"], Architecture: values["architecture"],
		ProfileID: values["profile_id"], ProfileSHA256: values["profile_sha256"],
		CredentialSourceDescriptorSHA256: values["credential_source_descriptor_sha256"],
		PathtrustBinarySHA256:            values["pathtrust_binary_sha256"], PathtrustChainSHA256: values["pathtrust_chain_sha256"], PathtrustDevice: pathtrustDevice,
		TrustCapsuleSHA256: values["trust_capsule_sha256"], AttestationCoreSHA256: values["attestation_core_sha256"], AttestationCoreChainSHA256: values["attestation_core_chain_sha256"], AttestationCoreDevice: coreDevice,
		AttestationSHA256: values["attestation_sha256"], ExpectedSHA256: values["expected_sha256"], AttestationPublicKeySHA256: values["attestation_public_key_sha256"], ExternalManifestSHA256: values["external_manifest_sha256"],
		BashBinarySHA256: values["bash_binary_sha256"], BashChainSHA256: values["bash_chain_sha256"], BashDevice: bashDevice,
		DockerClientSHA256: values["docker_client_sha256"], DockerClientChainSHA256: values["docker_client_chain_sha256"], DockerClientDevice: dockerDevice,
		RuntimeClosureManifestSHA256: values["runtime_closure_manifest_sha256"], ManifestVerifierSHA256: values["manifest_verifier_sha256"],
		PreflightRunnerSHA256: values["preflight_runner_sha256"], MigrationRunnerSHA256: values["migration_runner_sha256"],
		GooseBinarySHA256: values["goose_binary_sha256"], GooseVersion: values["goose_version"], GooseBuildInfoSHA256: values["goose_build_info_sha256"],
		MigrationSetSHA256: values["migration_set_sha256"], ClientAuth00042SHA256: values["client_auth_00042_sha256"],
		ArtifactStorageProfile: values["artifact_storage_profile"], ArtifactStorageDescriptorSHA256: values["artifact_storage_descriptor_sha256"],
		GlobalsDumpSHA256: values["globals_dump_sha256"], GlobalsDumpSizeBytes: globalsSize, DatabaseDumpSHA256: values["database_dump_sha256"], DatabaseDumpSizeBytes: databaseSize,
		PostgresImageSHA256: values["postgres_image_sha256"], ReleaseJournalHeadSHA256: values["release_journal_head_sha256"], ReleaseJournalSnapshotSHA256: values["release_journal_snapshot_sha256"],
		SourceContainerID: values["source_container_id"], SourceSystemIdentifier: values["source_system_identifier"], SourceDatabase: values["source_database"], SourceDatabaseOID: values["source_database_oid"],
		SourceDatabaseOwnerOID: values["source_database_owner_oid"], SourceDatabaseOwner: values["source_database_owner_name"],
		IsolatedContainerID: values["isolated_container_id"], IsolatedSystemIdentifier: values["isolated_system_identifier"], IsolatedNetworkID: values["isolated_network_id"],
		IsolatedDatabase: values["isolated_database"], IsolatedDatabaseOID: values["isolated_database_oid"], IsolatedImageID: values["isolated_image_id"], IsolatedRunID: values["isolated_run_id"],
		NotBefore: time.Unix(notBeforeEpoch, 0).UTC(), NotAfter: time.Unix(notAfterEpoch, 0).UTC(), SHA256: digest, parsed: true,
		canonical: append([]byte(nil), data...), parseArchitecture: architecture, parseNow: now.UTC(),
	}, nil
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != len(fieldNames) {
		return nil, errors.New("execution plan v2 field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("execution plan v2 value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

// CanonicalFieldNames returns a copy of the fixed wire schema for offline
// release tooling. Mutating the returned slice cannot alter parser behavior.
func CanonicalFieldNames() []string {
	return append([]string(nil), fieldNames[:]...)
}

func SHA256Hex(plan Plan) (string, error) {
	trusted, err := plan.VerifiedCopy()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(trusted.SHA256[:]), nil
}

// VerifiedCopy reparses the private canonical bytes and never trusts the
// mutable public projection. It is not an admission decision.
func (plan Plan) VerifiedCopy() (Plan, error) {
	return plan.VerifiedCopyAt(plan.parseNow)
}

func (plan Plan) VerifiedCopyAt(now time.Time) (Plan, error) {
	if !plan.parsed || len(plan.canonical) == 0 || plan.parseArchitecture == "" || plan.parseNow.IsZero() {
		return Plan{}, errors.New("execution plan v2 parsed capability invalid")
	}
	digest := sha256.Sum256(plan.canonical)
	if digest != plan.SHA256 {
		return Plan{}, errors.New("execution plan v2 projection identity changed")
	}
	return Parse(plan.canonical, digest, plan.parseArchitecture, now)
}

func (plan Plan) IsParsed() bool {
	_, err := plan.VerifiedCopy()
	return err == nil
}

func nonZeroHex64(value string) bool {
	return lowerHex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func parsePositiveUint(value string, bits int) (uint64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("positive integer invalid")
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("positive integer invalid")
	}
	return parsed, nil
}

func parsePositiveInt(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("positive integer invalid")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("positive integer invalid")
	}
	return parsed, nil
}

func hasDuplicate(values map[string]string, names []string) bool {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		value := values[name]
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
