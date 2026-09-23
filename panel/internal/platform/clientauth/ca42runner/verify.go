package ca42runner

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsule"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
)

type VerifiedBundle struct {
	Authority      ca42authority.Descriptor
	Release        ca42release.Manifest
	Execution      ca42execution.Plan
	Capsule        ca42capsule.Capsule
	PreviousLedger *ca42authority.Ledger
	Ledger         ca42authority.Ledger
	ExactRetry     bool
}

func validateAdmissionTimeWindow(bundle VerifiedBundle, verificationTime, now time.Time) error {
	now = now.UTC()
	if now.IsZero() || now.Before(verificationTime.UTC()) || now.Before(bundle.Authority.ClockFloor) ||
		now.Before(bundle.Authority.NotBefore) || !now.Before(bundle.Authority.NotAfter) ||
		now.Before(bundle.Release.NotBefore) || !now.Before(bundle.Release.NotAfter) ||
		now.Before(bundle.Execution.NotBefore) || !now.Before(bundle.Execution.NotAfter) {
		return errors.New("CA42 admission trusted time denied")
	}
	return nil
}

func VerifyBundle(
	authorityBytes, releaseBytes, executionBytes, capsuleBytes, externalBytes []byte,
	rootKeys ca42authority.RootKeyset,
	architecture string,
	hostIdentity, runningRunnerSHA256 [sha256.Size]byte,
	expectedAttemptID string,
	previousLedger *ca42authority.Ledger,
	now time.Time,
) (VerifiedBundle, error) {
	var empty VerifiedBundle
	if !attemptIDPattern.MatchString(expectedAttemptID) {
		return empty, errors.New("runner attempt ID invalid")
	}
	authority, err := ca42authority.ParseAndVerify(authorityBytes, rootKeys, architecture, hostIdentity, now)
	if err != nil {
		return empty, err
	}
	if authority.AttemptID != expectedAttemptID {
		return empty, errors.New("runner attempt ID binding mismatch")
	}
	if subtle.ConstantTimeCompare(authority.RootRunnerSHA256[:], runningRunnerSHA256[:]) != 1 {
		return empty, errors.New("running root runner identity mismatch")
	}
	binding := ca42release.AuthorityBinding{
		PublicKey: authority.ReleaseSignerKey, ManifestSHA256: authority.ReleaseManifestSHA256,
		SignerSHA256: authority.ReleaseSignerSHA256, Epoch: authority.AuthorityEpoch,
		Sequence: authority.AuthoritySequence, BindingSHA256: authority.BindingSHA256,
		ReleaseSignerKeyID: authority.ReleaseSignerKeyID,
	}
	release, err := ca42release.ParseAndVerify(releaseBytes, binding, architecture, now, externalBytes)
	if err != nil {
		return empty, err
	}
	if release.ReleaseID != authority.ReleaseID || release.ReleaseRunID != authority.ReleaseRunID ||
		release.AttemptID != authority.AttemptID || release.Architecture != authority.Architecture {
		return empty, errors.New("authority and release identity mismatch")
	}
	if release.NotBefore.Before(authority.NotBefore) || release.NotAfter.After(authority.NotAfter) {
		return empty, errors.New("release validity escapes authority window")
	}
	executionDigestBytes, err := hex.DecodeString(release.ExecutionPlanSHA256)
	if err != nil || len(executionDigestBytes) != sha256.Size {
		return empty, errors.New("release execution plan identity invalid")
	}
	var executionDigest [sha256.Size]byte
	copy(executionDigest[:], executionDigestBytes)
	execution, err := ca42execution.Parse(executionBytes, executionDigest, architecture, now)
	if err != nil {
		return empty, err
	}
	if err := ca42execution.BindRelease(execution, release); err != nil {
		return empty, err
	}
	capsuleDigestBytes, err := hex.DecodeString(execution.TrustCapsuleSHA256)
	if err != nil || len(capsuleDigestBytes) != sha256.Size {
		return empty, errors.New("execution plan trust capsule identity invalid")
	}
	var capsuleDigest [sha256.Size]byte
	copy(capsuleDigest[:], capsuleDigestBytes)
	capsule, err := ca42capsule.Parse(capsuleBytes, capsuleDigest)
	if err != nil {
		return empty, err
	}
	if err := ca42capsule.BindPlan(capsule, execution); err != nil {
		return empty, err
	}
	nextLedger, exactRetry, err := ca42authority.Advance(previousLedger, authority, now)
	if err != nil {
		return empty, err
	}
	var previousSnapshot *ca42authority.Ledger
	if previousLedger != nil {
		copyOfPrevious := *previousLedger
		previousSnapshot = &copyOfPrevious
	}
	return VerifiedBundle{
		Authority: authority, Release: release, Execution: execution, Capsule: capsule, PreviousLedger: previousSnapshot,
		Ledger: nextLedger, ExactRetry: exactRetry,
	}, nil
}
