package ca42releasev3

import (
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

func BindExecutionPlan(manifest Manifest, plan ca42executionv2.Plan, now time.Time) error {
	snapshot, err := manifest.SnapshotAt(now)
	if err != nil {
		return err
	}
	trustedPlan, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	planSHA, err := ca42executionv2.SHA256Hex(trustedPlan)
	if err != nil {
		return err
	}
	pairs := []struct{ manifest, plan string }{
		{snapshot.Value(FieldReleaseID), trustedPlan.ReleaseID},
		{snapshot.Value(FieldReleaseRunID), trustedPlan.ReleaseRunID},
		{snapshot.Value(FieldAttemptID), trustedPlan.AttemptID},
		{snapshot.Value(FieldArchitecture), trustedPlan.Architecture},
		{snapshot.Value(FieldProfileID), trustedPlan.ProfileID},
		{snapshot.Value(FieldProfileSHA256), trustedPlan.ProfileSHA256},
		{snapshot.Value(FieldCredentialSourceDescriptorSHA256), trustedPlan.CredentialSourceDescriptorSHA256},
		{snapshot.Value(FieldPathtrustBinarySHA256), trustedPlan.PathtrustBinarySHA256},
		{snapshot.Value(FieldPathtrustChainSHA256), trustedPlan.PathtrustChainSHA256},
		{snapshot.Value(FieldPathtrustDevice), strconv.FormatUint(trustedPlan.PathtrustDevice, 10)},
		{snapshot.Value(FieldTrustCapsuleSHA256), trustedPlan.TrustCapsuleSHA256},
		{snapshot.Value(FieldAttestationCoreSHA256), trustedPlan.AttestationCoreSHA256},
		{snapshot.Value(FieldAttestationCoreChainSHA256), trustedPlan.AttestationCoreChainSHA256},
		{snapshot.Value(FieldAttestationCoreDevice), strconv.FormatUint(trustedPlan.AttestationCoreDevice, 10)},
		{snapshot.Value(FieldAttestationSHA256), trustedPlan.AttestationSHA256},
		{snapshot.Value(FieldExpectedSHA256), trustedPlan.ExpectedSHA256},
		{snapshot.Value(FieldAttestationPublicKeySHA256), trustedPlan.AttestationPublicKeySHA256},
		{snapshot.Value(FieldExternalManifestSHA256), trustedPlan.ExternalManifestSHA256},
		{snapshot.Value(FieldBashBinarySHA256), trustedPlan.BashBinarySHA256},
		{snapshot.Value(FieldBashChainSHA256), trustedPlan.BashChainSHA256},
		{snapshot.Value(FieldBashDevice), strconv.FormatUint(trustedPlan.BashDevice, 10)},
		{snapshot.Value(FieldDockerClientSHA256), trustedPlan.DockerClientSHA256},
		{snapshot.Value(FieldDockerClientChainSHA256), trustedPlan.DockerClientChainSHA256},
		{snapshot.Value(FieldDockerClientDevice), strconv.FormatUint(trustedPlan.DockerClientDevice, 10)},
		{snapshot.Value(FieldRuntimeClosureManifestSHA256), trustedPlan.RuntimeClosureManifestSHA256},
		{snapshot.Value(FieldManifestVerifierSHA256), trustedPlan.ManifestVerifierSHA256},
		{snapshot.Value(FieldPreflightRunnerSHA256), trustedPlan.PreflightRunnerSHA256},
		{snapshot.Value(FieldMigrationRunnerSHA256), trustedPlan.MigrationRunnerSHA256},
		{snapshot.Value(FieldGooseBinarySHA256), trustedPlan.GooseBinarySHA256},
		{snapshot.Value(FieldGooseVersion), trustedPlan.GooseVersion},
		{snapshot.Value(FieldGooseBuildInfoSHA256), trustedPlan.GooseBuildInfoSHA256},
		{snapshot.Value(FieldMigrationSetSHA256), trustedPlan.MigrationSetSHA256},
		{snapshot.Value(FieldClientAuth00042SHA256), trustedPlan.ClientAuth00042SHA256},
		{snapshot.Value(FieldArtifactStorageProfile), trustedPlan.ArtifactStorageProfile},
		{snapshot.Value(FieldArtifactStorageDescriptorSHA256), trustedPlan.ArtifactStorageDescriptorSHA256},
		{snapshot.Value(FieldGlobalsDumpSHA256), trustedPlan.GlobalsDumpSHA256},
		{snapshot.Value(FieldGlobalsDumpSizeBytes), strconv.FormatUint(trustedPlan.GlobalsDumpSizeBytes, 10)},
		{snapshot.Value(FieldDatabaseDumpSHA256), trustedPlan.DatabaseDumpSHA256},
		{snapshot.Value(FieldDatabaseDumpSizeBytes), strconv.FormatUint(trustedPlan.DatabaseDumpSizeBytes, 10)},
		{snapshot.Value(FieldPostgresImageSHA256), trustedPlan.PostgresImageSHA256},
		{snapshot.Value(FieldProductionSourceContainerID), trustedPlan.SourceContainerID},
		{snapshot.Value(FieldProductionSourceSystemIdentifier), trustedPlan.SourceSystemIdentifier},
		{snapshot.Value(FieldProductionSourceDatabase), trustedPlan.SourceDatabase},
		{snapshot.Value(FieldProductionSourceDatabaseOID), trustedPlan.SourceDatabaseOID},
		{snapshot.Value(FieldProductionSourceDatabaseOwnerOID), trustedPlan.SourceDatabaseOwnerOID},
		{snapshot.Value(FieldProductionSourceDatabaseOwnerName), trustedPlan.SourceDatabaseOwner},
		{snapshot.Value(FieldProductionSourceGooseWaterline), "41"},
		{snapshot.Value(FieldIsolatedTargetContainerID), trustedPlan.IsolatedContainerID},
		{snapshot.Value(FieldIsolatedTargetSystemIdentifier), trustedPlan.IsolatedSystemIdentifier},
		{snapshot.Value(FieldIsolatedTargetNetworkID), trustedPlan.IsolatedNetworkID},
		{snapshot.Value(FieldIsolatedTargetDatabase), trustedPlan.IsolatedDatabase},
		{snapshot.Value(FieldIsolatedTargetDatabaseOID), trustedPlan.IsolatedDatabaseOID},
		{snapshot.Value(FieldIsolatedTargetImageID), trustedPlan.IsolatedImageID},
		{snapshot.Value(FieldIsolatedTargetRunID), trustedPlan.IsolatedRunID},
		{snapshot.Value(FieldLedgerNamespace), ca42executionv2.LedgerNamespace},
		{snapshot.Value(FieldLedgerDirectorySHA256), ca42executionv2.LedgerDirectorySHA256},
		{snapshot.Value(FieldReleaseJournalHeadSHA256), trustedPlan.ReleaseJournalHeadSHA256},
		{snapshot.Value(FieldReleaseJournalSnapshotSHA256), trustedPlan.ReleaseJournalSnapshotSHA256},
		{snapshot.Value(FieldExecutionPlanFormat), ca42executionv2.Format},
		{snapshot.Value(FieldExecutionPlanSHA256), planSHA},
		{snapshot.Value(FieldNotBeforeEpoch), strconv.FormatInt(trustedPlan.NotBefore.Unix(), 10)},
		{snapshot.Value(FieldNotAfterEpoch), strconv.FormatInt(trustedPlan.NotAfter.Unix(), 10)},
	}
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		return err
	}
	pairs = append(pairs,
		struct{ manifest, plan string }{snapshot.Value(FieldProfileID), ca42protocolv2.ProfileID},
		struct{ manifest, plan string }{snapshot.Value(FieldProfileSHA256), hex.EncodeToString(profileSHA[:])},
	)
	for _, pair := range pairs {
		if pair.manifest != pair.plan {
			return errors.New("release manifest v3 and execution plan v2 binding mismatch")
		}
	}
	return nil
}
