// Package ca42controlv3 freezes the shared on-disk contract between the CA42
// V3 control-bundle producer and the production root runner. It contains no
// opener, parser, fallback, or mutation authority.
package ca42controlv3

import (
	"errors"
	"regexp"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42attestationv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

const (
	Format   = "pandora-ca42-control-layout-v3"
	RootPath = "/var/lib/pandora/ca42-control-v3"

	ReleaseManifestRole           = "release_manifest_v3"
	ExecutionPlanRole             = "execution_plan_v2"
	TrustCapsuleRole              = "trust_capsule_v2"
	AttestationCoreRole           = "attestation_core"
	AttestationRole               = "attestation_v3"
	ExpectedRole                  = "expected_v2"
	ArtifactStorageDescriptorRole = "artifact_storage_descriptor_v2"

	ReleaseManifestName           = "release-manifest.v3"
	ExecutionPlanName             = "execution-plan.v2"
	TrustCapsuleName              = "trust-capsule.v2"
	AttestationCoreName           = "attestation-core"
	AttestationName               = "attestation.v3"
	ExpectedName                  = "expected.v2"
	ArtifactStorageDescriptorName = "artifact-storage-descriptor.v2"

	DirectoryMode  = 0o700
	DataMode       = 0o400
	ExecutableMode = 0o500

	MaxAttestationCoreBytes = uint64(4 << 20)
)

type Entry struct {
	Ordinal  int
	Role     string
	Name     string
	Mode     uint32
	MaxBytes uint64
}

var attemptID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

var entries = [...]Entry{
	{Ordinal: 1, Role: ReleaseManifestRole, Name: ReleaseManifestName, Mode: DataMode, MaxBytes: ca42releasev3.MaxManifestBytes},
	{Ordinal: 2, Role: ExecutionPlanRole, Name: ExecutionPlanName, Mode: DataMode, MaxBytes: ca42executionv2.MaxPlanBytes},
	{Ordinal: 3, Role: TrustCapsuleRole, Name: TrustCapsuleName, Mode: DataMode, MaxBytes: ca42capsulev2.MaxCapsuleBytes},
	{Ordinal: 4, Role: AttestationCoreRole, Name: AttestationCoreName, Mode: ExecutableMode, MaxBytes: MaxAttestationCoreBytes},
	{Ordinal: 5, Role: AttestationRole, Name: AttestationName, Mode: DataMode, MaxBytes: ca42attestationv3.MaxBytes},
	{Ordinal: 6, Role: ExpectedRole, Name: ExpectedName, Mode: DataMode, MaxBytes: ca42expectedv2.MaxExpectedBytes},
	{Ordinal: 7, Role: ArtifactStorageDescriptorRole, Name: ArtifactStorageDescriptorName, Mode: DataMode, MaxBytes: ca42storage.MaxDescriptorBytes},
}

func ValidateAttemptID(value string) error {
	if !attemptID.MatchString(value) {
		return errors.New("CA42 V3 control attempt ID invalid")
	}
	return nil
}

// Entries returns a value copy of the complete ordered layout. It is
// diagnostic producer input, not filesystem or verification authority.
func Entries() [7]Entry {
	return entries
}

// EntryForRole returns a value copy only for a frozen role. Consumers must
// never interpret an unknown role or legacy basename as a fallback.
func EntryForRole(role string) (Entry, error) {
	for _, entry := range entries {
		if entry.Role == role {
			return entry, nil
		}
	}
	return Entry{}, errors.New("CA42 V3 control role unknown")
}
