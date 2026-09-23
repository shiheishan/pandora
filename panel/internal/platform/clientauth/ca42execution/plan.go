package ca42execution

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
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
)

const (
	Format                 = "pandora-ca42-execution-plan-v1"
	Status                 = "READY_FOR_ROOT_RUNNER"
	Transition             = "goose-41-to-42"
	LedgerNamespace        = "client-auth-00042-v1"
	LedgerDirectorySHA256  = "e2080312e2443e215abfe18900bbcc8fb9b78a53d10121aaa80c1d0ada396283"
	RequiredExecutableMode = "0500"
	MaxPlanBytes           = 64 << 10
	MaxValidity            = time.Hour
)

var (
	lowerHex64    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifier    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	isolatedRunID = regexp.MustCompile(`^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$`)
)

const (
	idxFormat = iota
	idxStatus
	idxTransition
	idxReleaseID
	idxReleaseRunID
	idxAttemptID
	idxArchitecture
	idxPathtrustSHA
	idxPathtrustChainSHA
	idxPathtrustDevice
	idxPathtrustMode
	idxTrustCapsuleSHA
	idxAttestationCoreSHA
	idxAttestationCoreChainSHA
	idxAttestationCoreDevice
	idxAttestationCoreMode
	idxAttestationSHA
	idxExpectedSHA
	idxAttestationPublicKeySHA
	idxExternalManifestSHA
	idxManifestVerifierSHA
	idxPreflightRunnerSHA
	idxMigrationRunnerSHA
	idxGooseSHA
	idxMigrationSetSHA
	idxClientAuth00042SHA
	idxGlobalsDumpSHA
	idxDatabaseDumpSHA
	idxPostgresImageSHA
	idxReleaseJournalHeadSHA
	idxReleaseJournalSnapshotSHA
	idxLedgerNamespace
	idxLedgerDirectorySHA
	idxSourceContainerID
	idxSourceSystemID
	idxSourceDatabase
	idxSourceDatabaseOID
	idxSourceOwnerOID
	idxSourceOwnerName
	idxSourceWaterline
	idxIsolatedContainerID
	idxIsolatedSystemID
	idxIsolatedNetworkID
	idxIsolatedDatabase
	idxIsolatedDatabaseOID
	idxIsolatedImageID
	idxIsolatedRunID
	idxNotBefore
	idxNotAfter
	planFieldCount
)

var fieldNames = [...]string{
	"format", "status", "transition", "release_id", "release_run_id", "attempt_id", "architecture",
	"pathtrust_binary_sha256", "pathtrust_chain_sha256", "pathtrust_device", "pathtrust_mode",
	"trust_capsule_sha256", "attestation_core_sha256", "attestation_core_chain_sha256",
	"attestation_core_device", "attestation_core_mode", "attestation_sha256", "expected_sha256",
	"attestation_public_key_sha256", "external_manifest_sha256", "manifest_verifier_sha256",
	"preflight_runner_sha256", "migration_runner_sha256", "goose_binary_sha256", "migration_set_sha256",
	"client_auth_00042_sha256", "globals_dump_sha256", "database_dump_sha256", "postgres_image_sha256",
	"release_journal_head_sha256", "release_journal_snapshot_sha256", "ledger_namespace",
	"ledger_directory_sha256", "source_container_id", "source_system_identifier", "source_database",
	"source_database_oid", "source_database_owner_oid", "source_database_owner_name", "source_goose_waterline",
	"isolated_container_id", "isolated_system_identifier", "isolated_network_id", "isolated_database",
	"isolated_database_oid", "isolated_image_id", "isolated_run_id", "not_before_epoch", "not_after_epoch",
}

type Plan struct {
	ReleaseID, ReleaseRunID, AttemptID, Architecture                                  string
	PathtrustBinarySHA256, PathtrustChainSHA256                                       string
	PathtrustDevice                                                                   uint64
	TrustCapsuleSHA256, AttestationCoreSHA256, AttestationCoreChainSHA256             string
	AttestationCoreDevice                                                             uint64
	AttestationSHA256, ExpectedSHA256, AttestationPublicKeySHA256                     string
	ExternalManifestSHA256, ManifestVerifierSHA256, PreflightRunnerSHA256             string
	MigrationRunnerSHA256, GooseBinarySHA256, MigrationSetSHA256                      string
	ClientAuth00042SHA256, GlobalsDumpSHA256, DatabaseDumpSHA256, PostgresImageSHA256 string
	ReleaseJournalHeadSHA256, ReleaseJournalSnapshotSHA256                            string
	SourceContainerID, SourceSystemIdentifier, SourceDatabase, SourceDatabaseOID      string
	SourceDatabaseOwnerOID, SourceDatabaseOwner                                       string
	IsolatedContainerID, IsolatedSystemIdentifier, IsolatedNetworkID                  string
	IsolatedDatabase, IsolatedDatabaseOID, IsolatedImageID, IsolatedRunID             string
	NotBefore, NotAfter                                                               time.Time
	SHA256                                                                            [sha256.Size]byte
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte, architecture string, now time.Time) (Plan, error) {
	var empty Plan
	if len(data) == 0 || len(data) > MaxPlanBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("execution plan envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("execution plan identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != planFieldCount {
		return empty, errors.New("execution plan field count invalid")
	}
	values := make([]string, len(lines))
	for index, line := range lines {
		prefix := fieldNames[index] + "="
		if !bytes.HasPrefix(line, []byte(prefix)) || len(line) == len(prefix) {
			return empty, fmt.Errorf("execution plan field order invalid: %s", fieldNames[index])
		}
		values[index] = string(line[len(prefix):])
	}
	if values[idxFormat] != Format || values[idxStatus] != Status || values[idxTransition] != Transition ||
		values[idxArchitecture] != architecture || (architecture != "amd64" && architecture != "arm64") ||
		values[idxPathtrustMode] != RequiredExecutableMode || values[idxAttestationCoreMode] != RequiredExecutableMode ||
		values[idxLedgerNamespace] != LedgerNamespace || values[idxLedgerDirectorySHA] != LedgerDirectorySHA256 ||
		values[idxSourceWaterline] != "41" {
		return empty, errors.New("execution plan fixed field mismatch")
	}
	for _, index := range []int{idxReleaseID, idxReleaseRunID, idxAttemptID} {
		if !safeToken.MatchString(values[index]) {
			return empty, fmt.Errorf("execution plan token invalid: %s", fieldNames[index])
		}
	}
	for _, index := range []int{
		idxPathtrustSHA, idxPathtrustChainSHA, idxTrustCapsuleSHA, idxAttestationCoreSHA,
		idxAttestationCoreChainSHA, idxAttestationSHA, idxExpectedSHA, idxAttestationPublicKeySHA,
		idxExternalManifestSHA, idxManifestVerifierSHA, idxPreflightRunnerSHA, idxMigrationRunnerSHA,
		idxGooseSHA, idxMigrationSetSHA, idxClientAuth00042SHA, idxGlobalsDumpSHA, idxDatabaseDumpSHA,
		idxPostgresImageSHA, idxReleaseJournalHeadSHA, idxReleaseJournalSnapshotSHA,
		idxLedgerDirectorySHA, idxSourceContainerID, idxIsolatedContainerID, idxIsolatedNetworkID,
	} {
		if !nonZeroHex64(values[index]) {
			return empty, fmt.Errorf("execution plan SHA256 invalid: %s", fieldNames[index])
		}
	}
	for _, index := range []int{idxSourceDatabase, idxSourceOwnerName, idxIsolatedDatabase} {
		if !identifier.MatchString(values[index]) {
			return empty, fmt.Errorf("execution plan identifier invalid: %s", fieldNames[index])
		}
	}
	pathtrustDevice, err := parsePositiveUint(values[idxPathtrustDevice])
	if err != nil {
		return empty, errors.New("execution plan pathtrust device invalid")
	}
	coreDevice, err := parsePositiveUint(values[idxAttestationCoreDevice])
	if err != nil {
		return empty, errors.New("execution plan attestation core device invalid")
	}
	for _, index := range []int{idxSourceSystemID, idxSourceDatabaseOID, idxSourceOwnerOID, idxIsolatedSystemID, idxIsolatedDatabaseOID} {
		if _, err := parsePositiveUint(values[index]); err != nil {
			return empty, fmt.Errorf("execution plan positive integer invalid: %s", fieldNames[index])
		}
	}
	for _, index := range []int{idxSourceDatabaseOID, idxSourceOwnerOID, idxIsolatedDatabaseOID} {
		if parsed, _ := strconv.ParseUint(values[index], 10, 64); parsed > uint64(^uint32(0)) {
			return empty, fmt.Errorf("execution plan OID out of range: %s", fieldNames[index])
		}
	}
	if values[idxIsolatedImageID] != "sha256:"+values[idxPostgresImageSHA] ||
		values[idxClientAuth00042SHA] != ca42manifest.FrozenMigrationSHA256 ||
		!isolatedRunID.MatchString(values[idxIsolatedRunID]) ||
		values[idxSourceContainerID] == values[idxIsolatedContainerID] ||
		values[idxSourceSystemID] == values[idxIsolatedSystemID] ||
		values[idxSourceDatabase] != values[idxIsolatedDatabase] {
		return empty, errors.New("execution plan source/isolated identity invalid")
	}
	if values[idxPathtrustSHA] == values[idxAttestationCoreSHA] ||
		values[idxTrustCapsuleSHA] == values[idxAttestationSHA] ||
		values[idxTrustCapsuleSHA] == values[idxExpectedSHA] || values[idxAttestationSHA] == values[idxExpectedSHA] {
		return empty, errors.New("execution plan artifact separation invalid")
	}
	notBefore, err := parsePositiveInt(values[idxNotBefore])
	if err != nil {
		return empty, errors.New("execution plan not-before invalid")
	}
	notAfter, err := parsePositiveInt(values[idxNotAfter])
	if err != nil || notAfter <= notBefore || notAfter-notBefore > int64(MaxValidity/time.Second) {
		return empty, errors.New("execution plan validity invalid")
	}
	nowEpoch := now.UTC().Unix()
	if nowEpoch < notBefore || nowEpoch >= notAfter {
		return empty, errors.New("execution plan outside validity window")
	}
	return Plan{
		ReleaseID: values[idxReleaseID], ReleaseRunID: values[idxReleaseRunID], AttemptID: values[idxAttemptID], Architecture: values[idxArchitecture],
		PathtrustBinarySHA256: values[idxPathtrustSHA], PathtrustChainSHA256: values[idxPathtrustChainSHA], PathtrustDevice: pathtrustDevice,
		TrustCapsuleSHA256: values[idxTrustCapsuleSHA], AttestationCoreSHA256: values[idxAttestationCoreSHA], AttestationCoreChainSHA256: values[idxAttestationCoreChainSHA], AttestationCoreDevice: coreDevice,
		AttestationSHA256: values[idxAttestationSHA], ExpectedSHA256: values[idxExpectedSHA], AttestationPublicKeySHA256: values[idxAttestationPublicKeySHA],
		ExternalManifestSHA256: values[idxExternalManifestSHA], ManifestVerifierSHA256: values[idxManifestVerifierSHA], PreflightRunnerSHA256: values[idxPreflightRunnerSHA],
		MigrationRunnerSHA256: values[idxMigrationRunnerSHA], GooseBinarySHA256: values[idxGooseSHA], MigrationSetSHA256: values[idxMigrationSetSHA], ClientAuth00042SHA256: values[idxClientAuth00042SHA],
		GlobalsDumpSHA256: values[idxGlobalsDumpSHA], DatabaseDumpSHA256: values[idxDatabaseDumpSHA], PostgresImageSHA256: values[idxPostgresImageSHA],
		ReleaseJournalHeadSHA256: values[idxReleaseJournalHeadSHA], ReleaseJournalSnapshotSHA256: values[idxReleaseJournalSnapshotSHA],
		SourceContainerID: values[idxSourceContainerID], SourceSystemIdentifier: values[idxSourceSystemID], SourceDatabase: values[idxSourceDatabase], SourceDatabaseOID: values[idxSourceDatabaseOID],
		SourceDatabaseOwnerOID: values[idxSourceOwnerOID], SourceDatabaseOwner: values[idxSourceOwnerName], IsolatedContainerID: values[idxIsolatedContainerID],
		IsolatedSystemIdentifier: values[idxIsolatedSystemID], IsolatedNetworkID: values[idxIsolatedNetworkID], IsolatedDatabase: values[idxIsolatedDatabase],
		IsolatedDatabaseOID: values[idxIsolatedDatabaseOID], IsolatedImageID: values[idxIsolatedImageID], IsolatedRunID: values[idxIsolatedRunID],
		NotBefore: time.Unix(notBefore, 0).UTC(), NotAfter: time.Unix(notAfter, 0).UTC(), SHA256: digest,
	}, nil
}

func BindRelease(plan Plan, release ca42release.Manifest) error {
	if hex.EncodeToString(plan.SHA256[:]) != release.ExecutionPlanSHA256 ||
		plan.ReleaseID != release.ReleaseID || plan.ReleaseRunID != release.ReleaseRunID ||
		plan.AttemptID != release.AttemptID || plan.Architecture != release.Architecture ||
		plan.ManifestVerifierSHA256 != release.ManifestVerifierSHA256 ||
		plan.PreflightRunnerSHA256 != release.PreflightRunnerSHA256 ||
		plan.MigrationRunnerSHA256 != release.MigrationRunnerSHA256 ||
		plan.GooseBinarySHA256 != release.GooseBinarySHA256 || plan.MigrationSetSHA256 != release.MigrationSetSHA256 ||
		plan.ClientAuth00042SHA256 != release.ClientAuth00042SHA256 || plan.GlobalsDumpSHA256 != release.GlobalsDumpSHA256 ||
		plan.DatabaseDumpSHA256 != release.DatabaseDumpSHA256 || plan.PostgresImageSHA256 != release.PostgresImageSHA256 ||
		plan.AttestationPublicKeySHA256 != release.AttestationPublicKeySHA256 ||
		plan.ExternalManifestSHA256 != release.ExternalManifestSHA256 ||
		plan.ReleaseJournalHeadSHA256 != release.ReleaseJournalHeadSHA256 ||
		plan.SourceContainerID != release.ProductionSourceContainerID || plan.SourceSystemIdentifier != release.ProductionSourceSystemIdentifier ||
		plan.SourceDatabase != release.ProductionSourceDatabase || plan.SourceDatabaseOID != release.ProductionSourceDatabaseOID ||
		plan.SourceDatabaseOwnerOID != release.ProductionSourceDatabaseOwnerOID || plan.SourceDatabaseOwner != release.ProductionSourceDatabaseOwner ||
		plan.IsolatedContainerID != release.IsolatedTargetContainerID || plan.IsolatedSystemIdentifier != release.IsolatedTargetSystemIdentifier ||
		plan.IsolatedNetworkID != release.IsolatedTargetNetworkID || plan.IsolatedDatabase != release.IsolatedTargetDatabase ||
		plan.IsolatedDatabaseOID != release.IsolatedTargetDatabaseOID || plan.IsolatedImageID != release.IsolatedTargetImageID ||
		plan.IsolatedRunID != release.IsolatedTargetRunID || !plan.NotBefore.Equal(release.NotBefore) || !plan.NotAfter.Equal(release.NotAfter) {
		return errors.New("execution plan and release manifest binding mismatch")
	}
	return nil
}

func nonZeroHex64(value string) bool {
	return lowerHex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func parsePositiveUint(value string) (uint64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("noncanonical positive integer")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("noncanonical positive integer")
	}
	return parsed, nil
}

func parsePositiveInt(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("noncanonical positive integer")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("noncanonical positive integer")
	}
	return parsed, nil
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != planFieldCount {
		return nil, errors.New("execution plan field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("execution plan value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}
