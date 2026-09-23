package dbbackup

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

var (
	ErrRetentionIntentRequired     = errors.New("retention deletion intent is required")
	ErrRetentionIntentInvalid      = errors.New("retention deletion intent is invalid")
	ErrRetentionRemoteAmbiguous    = errors.New("retention remote state is ambiguous")
	ErrRetentionRemoteNonPrefix    = errors.New("retention remote state is not a deletion prefix")
	ErrRetentionStoreAhead         = errors.New("retention durable progress is ahead of remote state")
	ErrRetentionRemoteTooFar       = errors.New("retention remote state is more than one step ahead")
	ErrRetentionTerminalDivergence = errors.New("retention terminal remote state diverged")
	ErrRetentionRemoteMismatch     = errors.New("retention remote object does not match the signed intent")
	ErrRetentionDeleteUnconfirmed  = errors.New("retention remote deletion could not be confirmed")
)

type RetentionProgress uint8

const (
	RetentionProgressPending RetentionProgress = iota
	RetentionProgressManifestDeleted
	RetentionProgressChecksumDeleted
	RetentionProgressArchiveDeleted
)

type RetentionRemoteObjectState uint8

const (
	RetentionRemoteUnknown RetentionRemoteObjectState = iota
	RetentionRemoteExact
	RetentionRemoteMissing
	RetentionRemoteMismatch
)

type RetentionRemoteState struct {
	Manifest RetentionRemoteObjectState
	Checksum RetentionRemoteObjectState
	Archive  RetentionRemoteObjectState
}

type RetentionDeleteIntent struct {
	ID           uuid.UUID
	Version      uint64
	PolicySHA256 string
	PlanSHA256   string
	Progress     RetentionProgress
	Triplet      RetentionDeleteTriplet
}

type RetentionReconcileAction string

const (
	RetentionActionVerifyDeleteManifest  RetentionReconcileAction = "verify_delete_manifest"
	RetentionActionVerifyDeleteChecksum  RetentionReconcileAction = "verify_delete_checksum"
	RetentionActionVerifyDeleteArchive   RetentionReconcileAction = "verify_delete_archive"
	RetentionActionRecordManifestDeleted RetentionReconcileAction = "record_manifest_deleted"
	RetentionActionRecordChecksumDeleted RetentionReconcileAction = "record_checksum_deleted"
	RetentionActionRecordArchiveDeleted  RetentionReconcileAction = "record_archive_deleted"
	RetentionActionComplete              RetentionReconcileAction = "complete"
)

type RetentionReconcileResult struct {
	Action          RetentionReconcileAction
	ExpectedVersion uint64
	NextProgress    RetentionProgress
	Object          ArtifactRef
	HasObject       bool
}

// ReconcileRetentionDeletion compares a durable intent with an exact remote
// observation. A remote deletion may be at most one step ahead of durable
// progress, covering a crash after DELETE+absence verification but before the
// progress checkpoint. The store may never be ahead of remote reality.
func ReconcileRetentionDeletion(intent *RetentionDeleteIntent, remote RetentionRemoteState) (RetentionReconcileResult, error) {
	if intent == nil {
		return RetentionReconcileResult{}, ErrRetentionIntentRequired
	}
	snapshot := *intent
	if err := validateRetentionIntent(snapshot); err != nil {
		return RetentionReconcileResult{}, err
	}
	if snapshot.Progress == RetentionProgressArchiveDeleted {
		states := [...]RetentionRemoteObjectState{remote.Manifest, remote.Checksum, remote.Archive}
		for _, state := range states {
			if state != RetentionRemoteExact && state != RetentionRemoteMissing {
				return RetentionReconcileResult{}, ErrRetentionRemoteAmbiguous
			}
		}
		if remote.Manifest != RetentionRemoteMissing || remote.Checksum != RetentionRemoteMissing ||
			remote.Archive != RetentionRemoteMissing {
			return RetentionReconcileResult{}, ErrRetentionTerminalDivergence
		}
		return deleteRetentionAction(snapshot)
	}
	remoteProgress, err := retentionRemoteProgress(remote)
	if err != nil {
		return RetentionReconcileResult{}, err
	}
	stored := int(snapshot.Progress)
	switch {
	case remoteProgress < stored:
		return RetentionReconcileResult{}, ErrRetentionStoreAhead
	case remoteProgress > stored+1:
		return RetentionReconcileResult{}, ErrRetentionRemoteTooFar
	case remoteProgress == stored+1:
		next := RetentionProgress(remoteProgress)
		return RetentionReconcileResult{
			Action: recordRetentionAction(next), ExpectedVersion: snapshot.Version, NextProgress: next,
		}, nil
	case remoteProgress == stored:
		return deleteRetentionAction(snapshot)
	default:
		return RetentionReconcileResult{}, ErrRetentionIntentInvalid
	}
}

func validateRetentionIntent(intent RetentionDeleteIntent) error {
	if intent.ID == uuid.Nil || intent.Version == 0 || intent.Version > math.MaxInt64 || !validLowerSHA256(intent.PolicySHA256) ||
		!validLowerSHA256(intent.PlanSHA256) || intent.Progress > RetentionProgressArchiveDeleted ||
		intent.Triplet.Sequence == 0 || !backupIDPattern.MatchString(intent.Triplet.BackupID) {
		return ErrRetentionIntentInvalid
	}
	return validateRetentionTriplet(intent.Triplet)
}

func validateRetentionTriplet(triplet RetentionDeleteTriplet) error {
	if triplet.Sequence == 0 || !backupIDPattern.MatchString(triplet.BackupID) {
		return ErrRetentionIntentInvalid
	}
	if _, err := time.Parse("20060102T150405Z", triplet.BackupID); err != nil {
		return fmt.Errorf("%w: backup id", ErrRetentionIntentInvalid)
	}
	prefix := "aegis-postgres-" + triplet.BackupID
	if err := validateRetentionRef(triplet.Manifest, prefix+".manifest.json", maxManifestBytes); err != nil {
		return fmt.Errorf("%w: manifest", ErrRetentionIntentInvalid)
	}
	if err := validateRetentionRef(triplet.Checksum, prefix+".dump.age.sha256", 256); err != nil {
		return fmt.Errorf("%w: checksum", ErrRetentionIntentInvalid)
	}
	if err := validateRetentionRef(triplet.Archive, prefix+".dump.age", 0); err != nil {
		return fmt.Errorf("%w: archive", ErrRetentionIntentInvalid)
	}
	bytes, err := sumRetentionBytes(triplet.Manifest.Bytes, triplet.Checksum.Bytes, triplet.Archive.Bytes)
	if err != nil || bytes != triplet.Bytes {
		return fmt.Errorf("%w: bytes", ErrRetentionIntentInvalid)
	}
	return nil
}

func retentionRemoteProgress(remote RetentionRemoteState) (int, error) {
	states := [...]RetentionRemoteObjectState{remote.Manifest, remote.Checksum, remote.Archive}
	missing := 0
	seenExact := false
	for _, state := range states {
		if state != RetentionRemoteExact && state != RetentionRemoteMissing {
			return 0, ErrRetentionRemoteAmbiguous
		}
		if state == RetentionRemoteExact {
			seenExact = true
			continue
		}
		if seenExact {
			return 0, ErrRetentionRemoteNonPrefix
		}
		missing++
	}
	return missing, nil
}

func deleteRetentionAction(intent RetentionDeleteIntent) (RetentionReconcileResult, error) {
	switch intent.Progress {
	case RetentionProgressPending:
		return RetentionReconcileResult{
			Action: RetentionActionVerifyDeleteManifest, ExpectedVersion: intent.Version, NextProgress: intent.Progress,
			Object: intent.Triplet.Manifest, HasObject: true,
		}, nil
	case RetentionProgressManifestDeleted:
		return RetentionReconcileResult{
			Action: RetentionActionVerifyDeleteChecksum, ExpectedVersion: intent.Version, NextProgress: intent.Progress,
			Object: intent.Triplet.Checksum, HasObject: true,
		}, nil
	case RetentionProgressChecksumDeleted:
		return RetentionReconcileResult{
			Action: RetentionActionVerifyDeleteArchive, ExpectedVersion: intent.Version, NextProgress: intent.Progress,
			Object: intent.Triplet.Archive, HasObject: true,
		}, nil
	case RetentionProgressArchiveDeleted:
		return RetentionReconcileResult{
			Action: RetentionActionComplete, ExpectedVersion: intent.Version, NextProgress: intent.Progress,
		}, nil
	default:
		return RetentionReconcileResult{}, ErrRetentionIntentInvalid
	}
}

func recordRetentionAction(progress RetentionProgress) RetentionReconcileAction {
	switch progress {
	case RetentionProgressManifestDeleted:
		return RetentionActionRecordManifestDeleted
	case RetentionProgressChecksumDeleted:
		return RetentionActionRecordChecksumDeleted
	case RetentionProgressArchiveDeleted:
		return RetentionActionRecordArchiveDeleted
	default:
		return ""
	}
}
