//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42attestationv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42gooseinfo"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runtimeclosure"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

var errProductionV3ComposerUnavailable = errors.New("CA42 V3 production composer unavailable")

// OpenProductionV3VerificationSession is the sole public V3 production
// composition entry. Its only caller-controlled routing value is attemptID;
// all authority, control, inventory and journal capabilities come from fixed
// production roots and are transferred as one owned aggregate.
func OpenProductionV3VerificationSession(ctx context.Context, attemptID string) (*V3VerificationSession, error) {
	if ctx == nil || ca42controlv3.ValidateAttemptID(attemptID) != nil {
		return nil, errProductionV3ComposerUnavailable
	}
	handoff, err := composeProductionV3Handoff(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	session, err := openV3VerificationSessionFromHandoff(ctx, handoff)
	if err != nil {
		return nil, errors.Join(err, handoff.Close())
	}
	return session, nil
}

func composeProductionV3Handoff(ctx context.Context, attemptID string) (_ *v3ArtifactHandoff, resultErr error) {
	if ctx == nil || ca42controlv3.ValidateAttemptID(attemptID) != nil {
		return nil, errProductionV3ComposerUnavailable
	}
	authority, err := openProductionAuthorityV3Lease(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	var control *productionV3ControlBundle
	var inventory *ca42storage.InventoryLease
	completed := false
	defer func() {
		if completed {
			return
		}
		if inventory != nil {
			resultErr = errors.Join(resultErr, inventory.Close())
		}
		if control != nil && control.bundle != nil {
			resultErr = errors.Join(resultErr, control.bundle.Close())
		}
		resultErr = errors.Join(resultErr, authority.Close())
	}()

	control, err = openProductionV3ControlBundle(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	authorityBinding, err := authority.releaseAuthorityBinding(ctx)
	if err != nil {
		return nil, err
	}
	var set ca42artifactsv2.Set
	err = control.withControlData(ctx, func(operationContext context.Context, data *v3ControlData) error {
		parsed, retained, parseErr := parseProductionV3Graph(operationContext, data, authorityBinding, attemptID)
		if parseErr != nil {
			return parseErr
		}
		set, inventory = parsed, retained
		return nil
	})
	if err != nil {
		return nil, err
	}
	if inventory == nil {
		return nil, errProductionV3ComposerUnavailable
	}
	boundControl, err := control.bindParsedGraph(ctx, set)
	if err != nil {
		return nil, err
	}
	// newV3ArtifactHandoff adopts all three leases immediately, including on
	// failure, so clear local rollback ownership before inspecting its result.
	ownedAuthority, ownedInventory, ownedControl := authority, inventory, boundControl
	authority, inventory, control = nil, nil, nil
	handoff, err := newV3ArtifactHandoff(ctx, ownedAuthority, ownedInventory, ownedControl)
	if err != nil {
		return nil, err
	}
	completed = true
	return handoff, nil
}

func parseProductionV3Graph(ctx context.Context, data *v3ControlData, authority ca42releasev3.AuthorityBinding,
	attemptID string) (_ ca42artifactsv2.Set, inventory *ca42storage.InventoryLease, resultErr error) {
	if ctx == nil || data == nil || ca42controlv3.ValidateAttemptID(attemptID) != nil {
		return ca42artifactsv2.Set{}, nil, errProductionV3ComposerUnavailable
	}
	now := time.Now().UTC()
	preflight, err := ca42releasev3.ParseAuthorityVerified(data.release.bytes, authority, runtime.GOARCH, now)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	planSHA, err := preflight.ExecutionPlanSHA256At(now)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	plan, err := ca42executionv2.Parse(data.plan.bytes, planSHA, runtime.GOARCH, now)
	if err != nil || plan.AttemptID != attemptID {
		return ca42artifactsv2.Set{}, nil, errors.Join(errProductionV3ComposerUnavailable, err)
	}
	storageSHA, err := productionV3SHA256(plan.ArtifactStorageDescriptorSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	storageDescriptor, err := ca42storage.Parse(data.storage.bytes, storageSHA)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	boundStorage, err := ca42storage.BindPlan(storageDescriptor, plan, now)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	inventory, err = ca42storage.RetainProductionInventory(ctx, boundStorage)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	success := false
	defer func() {
		if !success && inventory != nil {
			resultErr = errors.Join(resultErr, inventory.Close())
			inventory = nil
		}
	}()

	external, err := inventory.ReadRoleBytes(ctx, "external_manifest", ca42manifest.MaxManifestBytes)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	credentialBytes, err := inventory.ReadRoleBytes(ctx, "credential_source_descriptor", ca42credential.MaxDescriptorBytes)
	if err != nil {
		zeroBytes(external)
		return ca42artifactsv2.Set{}, nil, err
	}
	runtimeBytes, err := inventory.ReadRoleBytes(ctx, "runtime_closure_manifest", ca42runtimeclosure.MaxManifestBytes)
	if err != nil {
		zeroBytes(external)
		zeroBytes(credentialBytes)
		return ca42artifactsv2.Set{}, nil, err
	}
	gooseBytes, err := inventory.ReadRoleBytes(ctx, "goose_build_info", ca42gooseinfo.MaxBuildInfoBytes)
	if err != nil {
		zeroBytes(external)
		zeroBytes(credentialBytes)
		zeroBytes(runtimeBytes)
		return ca42artifactsv2.Set{}, nil, err
	}
	publicKeyBytes, err := inventory.ReadRoleBytes(ctx, "attestation_public_key", ca42attestationv3.MaxPublicKeyBytes)
	defer func() {
		zeroBytes(external)
		zeroBytes(credentialBytes)
		zeroBytes(runtimeBytes)
		zeroBytes(gooseBytes)
		zeroBytes(publicKeyBytes)
	}()
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}

	parseNow := time.Now().UTC()
	if parseNow.Before(now) {
		return ca42artifactsv2.Set{}, nil, errors.New("CA42 V3 production composer clock moved backwards")
	}
	release, err := preflight.BindExternal(external, parseNow)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	capsuleSHA, err := productionV3SHA256(plan.TrustCapsuleSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	capsule, err := ca42capsulev2.Parse(data.capsule.bytes, capsuleSHA)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	expectedSHA, err := productionV3SHA256(plan.ExpectedSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	expected, err := ca42expectedv2.Parse(data.expected.bytes, expectedSHA, runtime.GOARCH, parseNow)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	attestationSHA, err := productionV3SHA256(plan.AttestationSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	attestation, err := ca42attestationv3.ParseAndVerify(data.attestation.bytes, attestationSHA, expected, publicKeyBytes, parseNow)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	credentialSHA, err := productionV3SHA256(plan.CredentialSourceDescriptorSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	credential, err := ca42credential.Parse(credentialBytes, credentialSHA, parseNow)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	runtimeSHA, err := productionV3SHA256(plan.RuntimeClosureManifestSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	runtimeClosure, err := ca42runtimeclosure.Parse(runtimeBytes, runtimeSHA, runtime.GOARCH)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	gooseSHA, err := productionV3SHA256(plan.GooseBuildInfoSHA256)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	goose, err := ca42gooseinfo.Parse(gooseBytes, gooseSHA, plan.GooseBinarySHA256, runtime.GOARCH)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	set, err := ca42artifactsv2.New(ca42artifactsv2.Inputs{
		Release: release, Plan: plan, Capsule: capsule, Expected: expected, Attestation: attestation,
		ExternalManifest: external, CredentialDescriptor: credential, RuntimeClosure: runtimeClosure,
		GooseBuildInfo: goose, StorageDescriptor: storageDescriptor,
	}, parseNow)
	if err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	if err := authorityBindingMatchesAttempt(authority, release, attemptID, parseNow); err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	if err := inventory.Revalidate(ctx); err != nil {
		return ca42artifactsv2.Set{}, nil, err
	}
	success = true
	return set, inventory, nil
}

func authorityBindingMatchesAttempt(authority ca42releasev3.AuthorityBinding, release ca42releasev3.Manifest, attemptID string, now time.Time) error {
	snapshot, err := release.SnapshotAt(now)
	if err != nil || snapshot.Value(ca42releasev3.FieldAttemptID) != attemptID || authority.ManifestSHA256 == ([sha256.Size]byte{}) {
		return errors.Join(errProductionV3ComposerUnavailable, err)
	}
	return nil
}

func productionV3SHA256(value string) ([sha256.Size]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return [sha256.Size]byte{}, errProductionV3ComposerUnavailable
	}
	var result [sha256.Size]byte
	copy(result[:], decoded)
	return result, nil
}
