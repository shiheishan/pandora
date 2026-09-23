package ca42release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

const (
	ContractCoreFormat      = "pandora-client-auth-00042-release-contract-core-v1"
	ContractTransition      = "goose-41-to-42"
	ContractPostgreSQLMajor = "18"
	ContractTargetWaterline = "42"
)

var contractCoreFieldNames = [...]string{
	"format", "release_manifest_format", "signature_algorithm", "release_status", "transition",
	"authority_epoch", "authority_sequence", "authority_binding_sha256",
	"release_signer_key_id", "architecture", "release_id", "release_run_id", "attempt_id",
	"migration_runner_sha256", "manifest_verifier_sha256", "preflight_runner_sha256",
	"goose_binary_sha256", "migration_set_sha256", "client_auth_00042_sha256",
	"globals_dump_sha256", "database_dump_sha256", "postgres_image_sha256",
	"production_source_container_id", "production_source_system_identifier",
	"production_source_database", "production_source_database_oid",
	"production_source_database_owner_oid", "production_source_database_owner_name",
	"production_source_goose_waterline", "authorized_target_goose_waterline", "postgresql_major",
	"isolated_target_container_id",
	"isolated_target_system_identifier", "isolated_target_network_id",
	"isolated_target_database", "isolated_target_database_oid", "isolated_target_image_id",
	"isolated_target_run_id", "external_manifest_sha256", "external_object_manifest_sha256",
	"attestation_public_key_sha256", "not_before_epoch", "not_after_epoch",
}

// ContractCoreBytes returns the stable release identity that a journal v2 may
// bind before the journal head and execution plan exist. It deliberately omits
// release_journal_head_sha256, execution_plan_sha256, and signature_b64, which
// prevents a release -> plan -> journal -> release hash cycle.
func ContractCoreBytes(manifest Manifest) ([]byte, error) {
	if err := validateContractCore(manifest); err != nil {
		return nil, err
	}
	values := []string{
		ContractCoreFormat, Format, SignatureAlgorithm, Status, ContractTransition,
		strconv.FormatUint(manifest.AuthorityEpoch, 10), strconv.FormatUint(manifest.AuthoritySequence, 10),
		manifest.AuthorityBindingSHA256, manifest.ReleaseSignerKeyID, manifest.Architecture,
		manifest.ReleaseID, manifest.ReleaseRunID, manifest.AttemptID,
		manifest.MigrationRunnerSHA256, manifest.ManifestVerifierSHA256, manifest.PreflightRunnerSHA256,
		manifest.GooseBinarySHA256, manifest.MigrationSetSHA256, manifest.ClientAuth00042SHA256,
		manifest.GlobalsDumpSHA256, manifest.DatabaseDumpSHA256, manifest.PostgresImageSHA256,
		manifest.ProductionSourceContainerID, manifest.ProductionSourceSystemIdentifier,
		manifest.ProductionSourceDatabase, manifest.ProductionSourceDatabaseOID,
		manifest.ProductionSourceDatabaseOwnerOID, manifest.ProductionSourceDatabaseOwner,
		strconv.FormatUint(manifest.ProductionSourceGooseWaterline, 10), ContractTargetWaterline,
		ContractPostgreSQLMajor, manifest.IsolatedTargetContainerID,
		manifest.IsolatedTargetSystemIdentifier, manifest.IsolatedTargetNetworkID,
		manifest.IsolatedTargetDatabase, manifest.IsolatedTargetDatabaseOID, manifest.IsolatedTargetImageID,
		manifest.IsolatedTargetRunID, manifest.ExternalManifestSHA256, manifest.ExternalObjectManifestSHA256,
		manifest.AttestationPublicKeySHA256, strconv.FormatInt(manifest.NotBefore.UTC().Unix(), 10),
		strconv.FormatInt(manifest.NotAfter.UTC().Unix(), 10),
	}
	if len(values) != len(contractCoreFieldNames) {
		return nil, errors.New("release contract core field invariant failed")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("release contract core value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", contractCoreFieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func ContractCoreSHA256(manifest Manifest) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	data, err := ContractCoreBytes(manifest)
	if err != nil {
		return zero, err
	}
	return sha256.Sum256(data), nil
}

func ContractCoreSHA256Hex(manifest Manifest) (string, error) {
	digest, err := ContractCoreSHA256(manifest)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

func validateContractCore(manifest Manifest) error {
	if manifest.AuthorityEpoch == 0 || manifest.AuthoritySequence == 0 ||
		(manifest.Architecture != "amd64" && manifest.Architecture != "arm64") ||
		manifest.ProductionSourceGooseWaterline != 41 {
		return errors.New("release contract core fixed field invalid")
	}
	for _, value := range []string{manifest.ReleaseSignerKeyID, manifest.ReleaseID, manifest.ReleaseRunID, manifest.AttemptID} {
		if !safeToken.MatchString(value) {
			return errors.New("release contract core token invalid")
		}
	}
	for _, value := range []string{
		manifest.AuthorityBindingSHA256, manifest.MigrationRunnerSHA256, manifest.ManifestVerifierSHA256,
		manifest.PreflightRunnerSHA256, manifest.GooseBinarySHA256, manifest.MigrationSetSHA256,
		manifest.ClientAuth00042SHA256, manifest.GlobalsDumpSHA256, manifest.DatabaseDumpSHA256,
		manifest.PostgresImageSHA256, manifest.ProductionSourceContainerID, manifest.IsolatedTargetContainerID,
		manifest.IsolatedTargetNetworkID, manifest.ExternalManifestSHA256, manifest.ExternalObjectManifestSHA256,
		manifest.AttestationPublicKeySHA256,
	} {
		if !nonZeroHex64(value) {
			return errors.New("release contract core SHA256 invalid")
		}
	}
	for _, value := range []string{manifest.ProductionSourceDatabase, manifest.ProductionSourceDatabaseOwner, manifest.IsolatedTargetDatabase} {
		if !identifier.MatchString(value) {
			return errors.New("release contract core identifier invalid")
		}
	}
	for _, value := range []string{
		manifest.ProductionSourceSystemIdentifier, manifest.ProductionSourceDatabaseOID,
		manifest.ProductionSourceDatabaseOwnerOID, manifest.IsolatedTargetSystemIdentifier,
		manifest.IsolatedTargetDatabaseOID,
	} {
		if !canonicalPositiveDecimal(value) {
			return errors.New("release contract core integer invalid")
		}
	}
	for _, value := range []string{manifest.ProductionSourceDatabaseOID, manifest.ProductionSourceDatabaseOwnerOID, manifest.IsolatedTargetDatabaseOID} {
		if !canonicalOID(value) {
			return errors.New("release contract core OID invalid")
		}
	}
	if manifest.ClientAuth00042SHA256 != ca42manifest.FrozenMigrationSHA256 ||
		manifest.IsolatedTargetImageID != "sha256:"+manifest.PostgresImageSHA256 ||
		!isolatedRunID.MatchString(manifest.IsolatedTargetRunID) ||
		manifest.ProductionSourceContainerID == manifest.IsolatedTargetContainerID ||
		manifest.ProductionSourceSystemIdentifier == manifest.IsolatedTargetSystemIdentifier ||
		manifest.ProductionSourceDatabase != manifest.IsolatedTargetDatabase {
		return errors.New("release contract core identity invalid")
	}
	notBefore, notAfter := manifest.NotBefore.UTC(), manifest.NotAfter.UTC()
	notBeforeEpoch, notAfterEpoch := notBefore.Unix(), notAfter.Unix()
	if notBeforeEpoch <= 0 || notAfterEpoch <= notBeforeEpoch ||
		notAfterEpoch-notBeforeEpoch > int64(MaxValidity/time.Second) ||
		notBefore.Nanosecond() != 0 || notAfter.Nanosecond() != 0 {
		return errors.New("release contract core validity invalid")
	}
	return nil
}
