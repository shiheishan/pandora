// Package ca42artifactsv2 verifies the complete CA42 v3 retained-artifact
// graph. The package path is retained for internal source compatibility. A Set
// is evidence only: it deliberately carries no execution or admission
// authority.
package ca42artifactsv2

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42attestationv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42gooseinfo"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runtimeclosure"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

const (
	Format        = "client-auth-00042-artifact-set-v3"
	bindingDomain = "PANDORA\x00CA42-ARTIFACT-SET\x00V3\x00"
)

// Inputs are already parsed protocol capabilities. New re-verifies every
// capability at now and binds the entire graph before returning a Set.
type Inputs struct {
	Release              ca42releasev3.Manifest
	Plan                 ca42executionv2.Plan
	Capsule              ca42capsulev2.Capsule
	Expected             ca42expectedv2.Expected
	Attestation          ca42attestationv3.Attestation
	ExternalManifest     []byte
	CredentialDescriptor ca42credential.Descriptor
	RuntimeClosure       ca42runtimeclosure.Manifest
	GooseBuildInfo       ca42gooseinfo.BuildInfo
	StorageDescriptor    ca42storage.Descriptor
}

// Snapshot is a forgeable, value-only diagnostic projection. It must never be
// accepted as journal, nonce-store, execution, or admission authority. Those
// consumers must accept Set or ConsumptionClaim and reverify it at trusted
// time.
type Snapshot struct {
	Format                           string
	ProfileID                        string
	ProfileSHA256                    string
	ReleaseID                        string
	ReleaseRunID                     string
	AttemptID                        string
	Architecture                     string
	ReleaseManifestSHA256            string
	ExecutionPlanSHA256              string
	TrustCapsuleSHA256               string
	ExpectedSHA256                   string
	AttestationSHA256                string
	ExternalManifestSHA256           string
	CredentialSourceDescriptorSHA256 string
	RuntimeClosureManifestSHA256     string
	GooseBuildInfoSHA256             string
	ArtifactStorageDescriptorSHA256  string
	CredentialCommitmentKeyID        string
	AttestationNonce                 string
	PlanNotBefore                    time.Time
	PlanNotAfter                     time.Time
	AttestationNotBefore             time.Time
	AttestationNotAfter              time.Time
	CredentialNotBefore              time.Time
	CredentialNotAfter               time.Time
	EffectiveNotBefore               time.Time
	EffectiveNotAfter                time.Time
	BindingSHA256                    string
}

const journalBindingDomain = "PANDORA\x00CA42-JOURNAL-BINDING\x00V1\x00"

// JournalBinding is an opaque, time-bound capability minted only from a fully
// reverified Set. It retains the Set so every consumer can replay the complete
// graph rather than trusting detached hashes.
type JournalBinding struct {
	set         Set
	values      JournalBindingValues
	fingerprint [sha256.Size]byte
	mintedAt    time.Time
	parsed      bool
}

// JournalBindingValues is a forgeable diagnostic projection. Journal and
// admission mutators must accept JournalBinding, never this value.
type JournalBindingValues struct {
	AttemptID                    string
	ReleaseContractCoreSHA256    string
	ReleaseJournalHeadSHA256     string
	ReleaseJournalSnapshotSHA256 string
	ArtifactSetBindingSHA256     string
}

// Set is opaque and can only be obtained through New.
type Set struct {
	release        ca42releasev3.Manifest
	plan           ca42executionv2.Plan
	capsule        ca42capsulev2.Capsule
	expected       ca42expectedv2.Expected
	attestation    ca42attestationv3.Attestation
	credential     ca42credential.Descriptor
	runtime        ca42runtimeclosure.Manifest
	goose          ca42gooseinfo.BuildInfo
	storage        ca42storage.Descriptor
	storageBinding ca42storage.BoundDescriptor
	external       []byte
	snapshot       Snapshot
	parsed         bool
}

type binding struct {
	name string
	run  func() error
}

var requiredBindingNames = [...]string{
	"release_to_plan",
	"capsule_to_plan",
	"expected_to_plan",
	"expected_to_release",
	"attestation_to_plan",
	"attestation_to_release",
	"attestation_to_capsule",
	"attestation_to_external",
	"credential_to_plan",
	"runtime_closure_to_plan",
	"goose_build_info_to_plan",
	"storage_descriptor_to_plan",
}

// New creates a fail-closed aggregate capability. The external manifest is
// copied before any verification so caller-owned storage is never retained.
func New(input Inputs, now time.Time) (Set, error) {
	var empty Set
	if now.IsZero() || len(input.ExternalManifest) == 0 || len(input.ExternalManifest) > ca42manifest.MaxManifestBytes {
		return empty, errors.New("CA42 artifact set v3 input invalid")
	}
	external := append([]byte(nil), input.ExternalManifest...)

	release, err := input.Release.VerifiedCopyAt(now)
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 release invalid: %w", err)
	}
	plan, err := input.Plan.VerifiedCopyAt(now)
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 plan invalid: %w", err)
	}
	capsule, err := input.Capsule.VerifiedCopy()
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 capsule invalid: %w", err)
	}
	expected, err := input.Expected.VerifiedCopyAt(now)
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 expected invalid: %w", err)
	}
	attestation, err := input.Attestation.VerifiedCopyAt(now)
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 attestation invalid: %w", err)
	}
	credential, err := input.CredentialDescriptor.VerifiedCopy(now)
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 credential invalid: %w", err)
	}
	runtimeClosure, err := input.RuntimeClosure.VerifiedCopy()
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 runtime closure invalid: %w", err)
	}
	goose, err := input.GooseBuildInfo.VerifiedCopy()
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 Goose build info invalid: %w", err)
	}
	storage, err := input.StorageDescriptor.VerifiedCopy()
	if err != nil {
		return empty, fmt.Errorf("CA42 artifact set v3 storage descriptor invalid: %w", err)
	}

	trusted := Inputs{
		Release: release, Plan: plan, Capsule: capsule, Expected: expected,
		Attestation: attestation, ExternalManifest: external,
		CredentialDescriptor: credential, RuntimeClosure: runtimeClosure,
		GooseBuildInfo: goose, StorageDescriptor: storage,
	}
	var storageBinding ca42storage.BoundDescriptor
	if err := runBindings(bindingsFor(trusted, now, &storageBinding)); err != nil {
		return empty, err
	}
	snapshot, err := buildSnapshot(trusted, now)
	if err != nil {
		return empty, err
	}
	return Set{
		release: release, plan: plan, capsule: capsule, expected: expected,
		attestation: attestation, credential: credential, runtime: runtimeClosure,
		goose: goose, storage: storage, storageBinding: storageBinding,
		external: external, snapshot: snapshot, parsed: true,
	}, nil
}

func bindingsFor(input Inputs, now time.Time, storageBinding *ca42storage.BoundDescriptor) []binding {
	return []binding{
		{name: requiredBindingNames[0], run: func() error { return ca42releasev3.BindExecutionPlan(input.Release, input.Plan, now) }},
		{name: requiredBindingNames[1], run: func() error { return ca42capsulev2.BindPlan(input.Capsule, input.Plan, now) }},
		{name: requiredBindingNames[2], run: func() error { return ca42expectedv2.BindPlan(input.Expected, input.Plan, now) }},
		{name: requiredBindingNames[3], run: func() error { return ca42expectedv2.BindRelease(input.Expected, input.Release, now) }},
		{name: requiredBindingNames[4], run: func() error { return ca42attestationv3.BindPlan(input.Attestation, input.Plan, now) }},
		{name: requiredBindingNames[5], run: func() error { return ca42attestationv3.BindRelease(input.Attestation, input.Release, now) }},
		{name: requiredBindingNames[6], run: func() error { return ca42attestationv3.BindCapsule(input.Attestation, input.Capsule, now) }},
		{name: requiredBindingNames[7], run: func() error { return ca42attestationv3.BindExternal(input.Attestation, input.ExternalManifest, now) }},
		{name: requiredBindingNames[8], run: func() error {
			return ca42executionv2.BindCredentialDescriptor(input.Plan, input.CredentialDescriptor, now)
		}},
		{name: requiredBindingNames[9], run: func() error { return ca42runtimeclosure.BindPlan(input.RuntimeClosure, input.Plan, now) }},
		{name: requiredBindingNames[10], run: func() error { return ca42gooseinfo.BindPlan(input.GooseBuildInfo, input.Plan, now) }},
		{name: requiredBindingNames[11], run: func() error {
			if storageBinding == nil {
				return errors.New("CA42 artifact set v3 storage binding output missing")
			}
			bound, err := ca42storage.BindPlan(input.StorageDescriptor, input.Plan, now)
			if err == nil {
				*storageBinding = bound
			}
			return err
		}},
	}
}

func runBindings(bindings []binding) error {
	if len(bindings) != len(requiredBindingNames) {
		return errors.New("CA42 artifact set v3 binding inventory invalid")
	}
	for index, candidate := range bindings {
		if candidate.name != requiredBindingNames[index] || candidate.run == nil {
			return errors.New("CA42 artifact set v3 binding order invalid")
		}
		if err := candidate.run(); err != nil {
			return fmt.Errorf("CA42 artifact set v3 %s binding failed: %w", candidate.name, err)
		}
	}
	return nil
}

func buildSnapshot(input Inputs, now time.Time) (Snapshot, error) {
	releaseSnapshot, err := input.Release.SnapshotAt(now)
	if err != nil {
		return Snapshot{}, err
	}
	attestationSnapshot, err := input.Attestation.SnapshotAt(now)
	if err != nil {
		return Snapshot{}, err
	}
	releaseSHA, err := input.Release.SHA256Hex(now)
	if err != nil {
		return Snapshot{}, err
	}
	planSHA, err := ca42executionv2.SHA256Hex(input.Plan)
	if err != nil {
		return Snapshot{}, err
	}
	capsuleSHA, err := input.Capsule.SHA256Hex()
	if err != nil {
		return Snapshot{}, err
	}
	expectedSHA, err := input.Expected.SHA256Hex(now)
	if err != nil {
		return Snapshot{}, err
	}
	attestationSHA, err := input.Attestation.SHA256Hex(now)
	if err != nil {
		return Snapshot{}, err
	}
	externalDigest := sha256.Sum256(input.ExternalManifest)
	externalSHA := hex.EncodeToString(externalDigest[:])
	credentialSHA := hex.EncodeToString(input.CredentialDescriptor.SHA256[:])
	runtimeSHA := hex.EncodeToString(input.RuntimeClosure.SHA256[:])
	gooseSHA := hex.EncodeToString(input.GooseBuildInfo.SHA256[:])
	storageSHA := hex.EncodeToString(input.StorageDescriptor.SHA256[:])
	attestationNotBeforeEpoch, err := strconv.ParseInt(attestationSnapshot.Value("issued_at"), 10, 64)
	if err != nil || attestationNotBeforeEpoch <= 0 {
		return Snapshot{}, errors.New("CA42 artifact set v3 attestation issued-at invalid")
	}
	attestationNotAfterEpoch, err := strconv.ParseInt(attestationSnapshot.Value("expires_at"), 10, 64)
	if err != nil || attestationNotAfterEpoch <= attestationNotBeforeEpoch {
		return Snapshot{}, errors.New("CA42 artifact set v3 attestation expires-at invalid")
	}
	attestationNotBefore := time.Unix(attestationNotBeforeEpoch, 0).UTC()
	attestationNotAfter := time.Unix(attestationNotAfterEpoch, 0).UTC()
	credentialNotBefore := input.CredentialDescriptor.NotBefore.UTC()
	credentialNotAfter := input.CredentialDescriptor.NotAfter.UTC()
	effectiveNotBefore, effectiveNotAfter, err := intersectValidity(
		input.Plan.NotBefore.UTC(), input.Plan.NotAfter.UTC(),
		attestationNotBefore, attestationNotAfter,
		credentialNotBefore, credentialNotAfter,
	)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Format:    Format,
		ProfileID: input.Plan.ProfileID, ProfileSHA256: input.Plan.ProfileSHA256,
		ReleaseID: input.Plan.ReleaseID, ReleaseRunID: input.Plan.ReleaseRunID,
		AttemptID: input.Plan.AttemptID, Architecture: input.Plan.Architecture,
		ReleaseManifestSHA256: releaseSHA, ExecutionPlanSHA256: planSHA,
		TrustCapsuleSHA256: capsuleSHA, ExpectedSHA256: expectedSHA,
		AttestationSHA256: attestationSHA, ExternalManifestSHA256: externalSHA,
		CredentialSourceDescriptorSHA256: credentialSHA,
		RuntimeClosureManifestSHA256:     runtimeSHA,
		GooseBuildInfoSHA256:             gooseSHA,
		ArtifactStorageDescriptorSHA256:  storageSHA,
		CredentialCommitmentKeyID:        input.CredentialDescriptor.CommitmentKeyID,
		AttestationNonce:                 attestationSnapshot.Value("nonce"),
		PlanNotBefore:                    input.Plan.NotBefore.UTC(), PlanNotAfter: input.Plan.NotAfter.UTC(),
		AttestationNotBefore: attestationNotBefore, AttestationNotAfter: attestationNotAfter,
		CredentialNotBefore: credentialNotBefore, CredentialNotAfter: credentialNotAfter,
		EffectiveNotBefore: effectiveNotBefore, EffectiveNotAfter: effectiveNotAfter,
	}
	if releaseSnapshot.Value(ca42releasev3.FieldExecutionPlanSHA256) != snapshot.ExecutionPlanSHA256 ||
		releaseSnapshot.Value(ca42releasev3.FieldTrustCapsuleSHA256) != snapshot.TrustCapsuleSHA256 ||
		releaseSnapshot.Value(ca42releasev3.FieldExpectedSHA256) != snapshot.ExpectedSHA256 ||
		releaseSnapshot.Value(ca42releasev3.FieldAttestationSHA256) != snapshot.AttestationSHA256 ||
		releaseSnapshot.Value(ca42releasev3.FieldExternalManifestSHA256) != snapshot.ExternalManifestSHA256 ||
		input.Plan.CredentialSourceDescriptorSHA256 != snapshot.CredentialSourceDescriptorSHA256 ||
		input.Plan.RuntimeClosureManifestSHA256 != snapshot.RuntimeClosureManifestSHA256 ||
		input.Plan.GooseBuildInfoSHA256 != snapshot.GooseBuildInfoSHA256 ||
		input.Plan.ArtifactStorageDescriptorSHA256 != snapshot.ArtifactStorageDescriptorSHA256 {
		return Snapshot{}, errors.New("CA42 artifact set v3 snapshot identity mismatch")
	}
	snapshot.BindingSHA256 = bindingSHA256(snapshot)
	return snapshot, nil
}

func intersectValidity(planNotBefore, planNotAfter, attestationNotBefore, attestationNotAfter, credentialNotBefore, credentialNotAfter time.Time) (time.Time, time.Time, error) {
	effectiveNotBefore := planNotBefore.UTC()
	for _, candidate := range []time.Time{attestationNotBefore.UTC(), credentialNotBefore.UTC()} {
		if candidate.After(effectiveNotBefore) {
			effectiveNotBefore = candidate
		}
	}
	effectiveNotAfter := planNotAfter.UTC()
	for _, candidate := range []time.Time{attestationNotAfter.UTC(), credentialNotAfter.UTC()} {
		if candidate.Before(effectiveNotAfter) {
			effectiveNotAfter = candidate
		}
	}
	if !effectiveNotAfter.After(effectiveNotBefore) {
		return time.Time{}, time.Time{}, errors.New("CA42 artifact set v3 effective validity invalid")
	}
	return effectiveNotBefore, effectiveNotAfter, nil
}

func bindingSHA256(snapshot Snapshot) string {
	values := []string{
		"format=" + snapshot.Format,
		"profile_id=" + snapshot.ProfileID,
		"profile_sha256=" + snapshot.ProfileSHA256,
		"release_id=" + snapshot.ReleaseID,
		"release_run_id=" + snapshot.ReleaseRunID,
		"attempt_id=" + snapshot.AttemptID,
		"architecture=" + snapshot.Architecture,
		"release_manifest_sha256=" + snapshot.ReleaseManifestSHA256,
		"execution_plan_sha256=" + snapshot.ExecutionPlanSHA256,
		"trust_capsule_sha256=" + snapshot.TrustCapsuleSHA256,
		"expected_sha256=" + snapshot.ExpectedSHA256,
		"attestation_sha256=" + snapshot.AttestationSHA256,
		"external_manifest_sha256=" + snapshot.ExternalManifestSHA256,
		"credential_source_descriptor_sha256=" + snapshot.CredentialSourceDescriptorSHA256,
		"runtime_closure_manifest_sha256=" + snapshot.RuntimeClosureManifestSHA256,
		"goose_build_info_sha256=" + snapshot.GooseBuildInfoSHA256,
		"artifact_storage_descriptor_sha256=" + snapshot.ArtifactStorageDescriptorSHA256,
		"credential_commitment_key_id=" + snapshot.CredentialCommitmentKeyID,
		"attestation_nonce=" + snapshot.AttestationNonce,
		"plan_not_before_epoch=" + strconv.FormatInt(snapshot.PlanNotBefore.Unix(), 10),
		"plan_not_after_epoch=" + strconv.FormatInt(snapshot.PlanNotAfter.Unix(), 10),
		"attestation_not_before_epoch=" + strconv.FormatInt(snapshot.AttestationNotBefore.Unix(), 10),
		"attestation_not_after_epoch=" + strconv.FormatInt(snapshot.AttestationNotAfter.Unix(), 10),
		"credential_not_before_epoch=" + strconv.FormatInt(snapshot.CredentialNotBefore.Unix(), 10),
		"credential_not_after_epoch=" + strconv.FormatInt(snapshot.CredentialNotAfter.Unix(), 10),
		"effective_not_before_epoch=" + strconv.FormatInt(snapshot.EffectiveNotBefore.Unix(), 10),
		"effective_not_after_epoch=" + strconv.FormatInt(snapshot.EffectiveNotAfter.Unix(), 10),
	}
	message := make([]byte, 0, len(bindingDomain)+1024)
	message = append(message, bindingDomain...)
	message = append(message, strings.Join(values, "\n")...)
	message = append(message, '\n')
	digest := sha256.Sum256(message)
	return hex.EncodeToString(digest[:])
}

// VerifiedCopyAt rechecks the full graph and current validity window.
func (set Set) VerifiedCopyAt(now time.Time) (Set, error) {
	if !set.parsed || len(set.external) == 0 || set.snapshot.BindingSHA256 == "" ||
		bindingSHA256(set.snapshot) != set.snapshot.BindingSHA256 {
		return Set{}, errors.New("CA42 artifact set v3 capability invalid")
	}
	verified, err := New(Inputs{
		Release: set.release, Plan: set.plan, Capsule: set.capsule,
		Expected: set.expected, Attestation: set.attestation,
		ExternalManifest:     set.external,
		CredentialDescriptor: set.credential, RuntimeClosure: set.runtime,
		GooseBuildInfo: set.goose, StorageDescriptor: set.storage,
	}, now)
	if err != nil {
		return Set{}, err
	}
	if verified.snapshot.BindingSHA256 != set.snapshot.BindingSHA256 {
		return Set{}, errors.New("CA42 artifact set v3 identity changed")
	}
	return verified, nil
}

// VerifiedCopyBoundToAttestationCoreAt binds the opaque graph to measurements
// obtained from the retained production attestation-core object. It deliberately
// returns no expected Plan projection: callers can only supply actual retained
// kernel evidence and receive the same fully reverified graph capability.
func (set Set) VerifiedCopyBoundToAttestationCoreAt(now time.Time, attemptID string, digest, absoluteChain [sha256.Size]byte, device uint64, mode uint32) (Set, error) {
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return Set{}, err
	}
	expectedDigest, err := strictSHA256(verified.plan.AttestationCoreSHA256)
	if err != nil {
		return Set{}, errors.New("CA42 artifact set v3 attestation core digest invalid")
	}
	expectedChain, err := strictSHA256(verified.plan.AttestationCoreChainSHA256)
	if err != nil {
		return Set{}, errors.New("CA42 artifact set v3 attestation core chain invalid")
	}
	if attemptID == "" || attemptID != verified.snapshot.AttemptID ||
		subtle.ConstantTimeCompare(digest[:], expectedDigest[:]) != 1 ||
		subtle.ConstantTimeCompare(absoluteChain[:], expectedChain[:]) != 1 ||
		device != verified.plan.AttestationCoreDevice || mode != 0o500 {
		return Set{}, errors.New("CA42 artifact set v3 attestation core binding mismatch")
	}
	return verified, nil
}

func strictSHA256(value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if len(value) != sha256.Size*2 {
		return result, errors.New("SHA256 length invalid")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return result, errors.New("SHA256 encoding invalid")
	}
	copy(result[:], decoded)
	if result == ([sha256.Size]byte{}) {
		return [sha256.Size]byte{}, errors.New("SHA256 value invalid")
	}
	return result, nil
}

// SnapshotAt returns a value-only copy after re-verifying the complete set.
func (set Set) SnapshotAt(now time.Time) (Snapshot, error) {
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return Snapshot{}, err
	}
	return verified.snapshot, nil
}

// JournalBindingAt derives an opaque Journal v3 prepared/layout capability
// from the Set's private verified release manifest.
func (set Set) JournalBindingAt(now time.Time) (JournalBinding, error) {
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return JournalBinding{}, err
	}
	values, err := journalBindingValuesAt(verified, now)
	if err != nil {
		return JournalBinding{}, err
	}
	mintedAt := now.UTC()
	fingerprint := journalBindingFingerprint(values, mintedAt)
	return JournalBinding{set: verified, values: values, fingerprint: fingerprint, mintedAt: mintedAt, parsed: true}, nil
}

func journalBindingValuesAt(verified Set, now time.Time) (JournalBindingValues, error) {
	coreSHA256, err := ca42releasev3.ContractCoreSHA256Hex(verified.release, now)
	if err != nil {
		return JournalBindingValues{}, err
	}
	releaseSnapshot, err := verified.release.SnapshotAt(now)
	if err != nil {
		return JournalBindingValues{}, err
	}
	journalHead := releaseSnapshot.Value(ca42releasev3.FieldReleaseJournalHeadSHA256)
	journalSnapshot := releaseSnapshot.Value(ca42releasev3.FieldReleaseJournalSnapshotSHA256)
	if verified.snapshot.AttemptID == "" || verified.snapshot.BindingSHA256 == "" || coreSHA256 == "" ||
		journalHead == "" || journalSnapshot == "" {
		return JournalBindingValues{}, errors.New("CA42 artifact set v3 journal binding invalid")
	}
	return JournalBindingValues{AttemptID: verified.snapshot.AttemptID,
		ReleaseContractCoreSHA256: coreSHA256, ReleaseJournalHeadSHA256: journalHead,
		ReleaseJournalSnapshotSHA256: journalSnapshot, ArtifactSetBindingSHA256: verified.snapshot.BindingSHA256}, nil
}

// VerifiedCopyAt replays the retained Set and compares the complete binding.
func (binding JournalBinding) VerifiedCopyAt(now time.Time) (JournalBinding, error) {
	if !binding.parsed || binding.mintedAt.IsZero() || now.UTC().Before(binding.mintedAt) ||
		binding.fingerprint == ([sha256.Size]byte{}) || journalBindingFingerprint(binding.values, binding.mintedAt) != binding.fingerprint {
		return JournalBinding{}, errors.New("CA42 artifact set v3 journal binding capability invalid")
	}
	verifiedSet, err := binding.set.VerifiedCopyAt(now)
	if err != nil {
		return JournalBinding{}, err
	}
	values, err := journalBindingValuesAt(verifiedSet, now)
	if err != nil || values != binding.values {
		return JournalBinding{}, errors.New("CA42 artifact set v3 journal binding changed")
	}
	return JournalBinding{set: verifiedSet, values: values, fingerprint: binding.fingerprint,
		mintedAt: binding.mintedAt, parsed: true}, nil
}

// ValuesAt returns a value copy only after re-verifying the opaque capability.
func (binding JournalBinding) ValuesAt(now time.Time) (JournalBindingValues, error) {
	verified, err := binding.VerifiedCopyAt(now)
	if err != nil {
		return JournalBindingValues{}, err
	}
	return verified.values, nil
}

func journalBindingFingerprint(values JournalBindingValues, mintedAt time.Time) [sha256.Size]byte {
	return sha256.Sum256([]byte(journalBindingDomain + values.AttemptID + "\x00" + values.ReleaseContractCoreSHA256 + "\x00" +
		values.ReleaseJournalHeadSHA256 + "\x00" + values.ReleaseJournalSnapshotSHA256 + "\x00" +
		values.ArtifactSetBindingSHA256 + "\x00" + strconv.FormatInt(mintedAt.UTC().Unix(), 10)))
}

// ExternalManifestBytesAt returns a defensive copy of the exact verified raw
// manifest. Future FD-only consumers must still retain and revalidate the file
// descriptor that supplied these bytes.
func (set Set) ExternalManifestBytesAt(now time.Time) ([]byte, error) {
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), verified.external...), nil
}

// CredentialKeyIDAt returns diagnostic routing data only. Verification still
// requires the Set capability and a retained kernel-keyring key capability.
func (set Set) CredentialKeyIDAt(now time.Time) (string, error) {
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return "", err
	}
	return verified.credential.CommitmentKeyID, nil
}

// VerifyCredentialAt verifies borrowed secret bytes through the Set's private
// descriptor. Neither the descriptor nor secret bytes are returned or stored.
func (set Set) VerifyCredentialAt(key *ca42credential.CommitmentKeyFD, secret []byte, now time.Time) error {
	if key == nil {
		return errors.New("CA42 artifact set v3 credential key invalid")
	}
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	return key.VerifyCredential(secret, verified.credential, now)
}

// RetainInventoryAt creates the runtime inventory only through the Set's
// private Plan-bound storage capability. It never returns a BoundDescriptor,
// child lease or raw FD.
func (set Set) RetainInventoryAt(ctx context.Context, sources []*os.File, now time.Time) (*ca42storage.InventoryLease, error) {
	if ctx == nil {
		return nil, errors.New("CA42 artifact set v3 inventory context invalid")
	}
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return nil, err
	}
	return ca42storage.RetainInventory(ctx, verified.storageBinding, sources)
}

// VerifiedCopyBoundToInventoryAt proves that inventory was retained from this
// Set's private Plan-bound storage descriptor without exporting that descriptor.
func (set Set) VerifiedCopyBoundToInventoryAt(ctx context.Context, inventory *ca42storage.InventoryLease, now time.Time) (Set, error) {
	if ctx == nil || inventory == nil {
		return Set{}, errors.New("CA42 artifact set v3 inventory binding invalid")
	}
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return Set{}, err
	}
	if err := inventory.RevalidateBoundDescriptor(ctx, verified.storageBinding); err != nil {
		return Set{}, err
	}
	return verified, nil
}

// VerifiedCopyClaimingInventoryAt is the one-shot ownership boundary used by a
// production handoff. A successful return means the InventoryLease has been
// atomically claimed for this exact Set and must no longer be reused.
func (set Set) VerifiedCopyClaimingInventoryAt(ctx context.Context, inventory *ca42storage.InventoryLease, now time.Time) (Set, error) {
	if ctx == nil || inventory == nil {
		return Set{}, errors.New("CA42 artifact set v3 inventory claim invalid")
	}
	return set.verifiedCopyClaimingInventoryAt(ctx, inventory, now)
}

type inventoryClaimLease interface {
	ClaimBoundDescriptorWithin(context.Context, ca42storage.BoundDescriptor, time.Time, time.Time) error
}

func (set Set) verifiedCopyClaimingInventoryAt(ctx context.Context, inventory inventoryClaimLease, now time.Time) (Set, error) {
	if ctx == nil || inventory == nil {
		return Set{}, errors.New("CA42 artifact set v3 inventory claim invalid")
	}
	verified, err := set.VerifiedCopyAt(now)
	if err != nil {
		return Set{}, err
	}
	if err := inventory.ClaimBoundDescriptorWithin(ctx, verified.storageBinding,
		verified.snapshot.EffectiveNotBefore, verified.snapshot.EffectiveNotAfter); err != nil {
		return Set{}, err
	}
	return verified, nil
}

// RetainProductionInventory opens and retains the exact descriptor inventory
// from fixed production roots. Unlike RetainInventoryAt it accepts no caller
// paths, source descriptors, or trusted-time value.
func (set Set) RetainProductionInventory(ctx context.Context) (*ca42storage.InventoryLease, error) {
	if ctx == nil {
		return nil, errors.New("CA42 artifact set v3 production inventory context invalid")
	}
	verified, err := set.VerifiedCopyAt(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	lease, err := ca42storage.RetainProductionInventory(ctx, verified.storageBinding)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*ca42storage.InventoryLease, error) {
		return nil, errors.Join(cause, lease.Close())
	}
	completed, err := set.VerifiedCopyAt(time.Now().UTC())
	if err != nil || completed.snapshot.BindingSHA256 != verified.snapshot.BindingSHA256 {
		return fail(errors.Join(errors.New("CA42 artifact set v3 expired during production inventory retention"), err))
	}
	if err := lease.RestrictValidity(completed.snapshot.EffectiveNotBefore, completed.snapshot.EffectiveNotAfter); err != nil {
		return fail(err)
	}
	if err := lease.Revalidate(ctx); err != nil {
		return fail(err)
	}
	finalSet, err := set.VerifiedCopyAt(time.Now().UTC())
	if err != nil || finalSet.snapshot.BindingSHA256 != completed.snapshot.BindingSHA256 {
		return fail(errors.Join(errors.New("CA42 artifact set v3 changed during production inventory publication"), err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return lease, nil
}
