package ca42executionv2

import (
	"encoding/hex"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
)

// BindCredentialDescriptor proves that independently parsed plan and
// credential descriptor bytes refer to the same release attempt and source
// database. It authorizes no execution.
func BindCredentialDescriptor(plan Plan, descriptor ca42credential.Descriptor, now time.Time) error {
	trustedPlan, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	trustedDescriptor, err := descriptor.VerifiedCopy(now)
	if err != nil {
		return err
	}
	plan, descriptor = trustedPlan, trustedDescriptor
	if plan.CredentialSourceDescriptorSHA256 != hex.EncodeToString(descriptor.SHA256[:]) ||
		plan.ReleaseID != descriptor.ReleaseID || plan.ReleaseRunID != descriptor.ReleaseRunID || plan.AttemptID != descriptor.AttemptID ||
		plan.SourceContainerID != descriptor.SourceContainerID || plan.SourceSystemIdentifier != descriptor.SourceSystemIdentifier ||
		plan.SourceDatabase != descriptor.SourceDatabase || plan.SourceDatabaseOID != descriptor.SourceDatabaseOID ||
		plan.SourceDatabaseOwner != descriptor.SourceDatabaseOwner || plan.SourceDatabaseOwnerOID != descriptor.SourceDatabaseOwnerOID ||
		descriptor.NotBefore.Before(plan.NotBefore) || descriptor.NotAfter.After(plan.NotAfter) {
		return errors.New("execution plan v2 credential descriptor binding mismatch")
	}
	return nil
}
