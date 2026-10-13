package dbbackup

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
)

var (
	ErrRetentionExecutorDependency = errors.New("retention executor dependency is unavailable")
	ErrRetentionIntentCASInvalid   = errors.New("retention intent compare-and-swap result is invalid")
)

// retentionIntentStore is intentionally sealed inside dbbackup. A production
// implementation must load an immutable signed intent and perform exact CAS
// updates; callers cannot substitute a name-only deletion authority.
type retentionIntentStore interface {
	loadRetentionIntent(context.Context, uuid.UUID) (RetentionDeleteIntent, error)
	compareAndSwapRetentionProgress(context.Context, RetentionDeleteIntent, RetentionProgress) (RetentionDeleteIntent, error)
	completeRetentionIntent(context.Context, RetentionDeleteIntent) (RetentionCompletionReceipt, error)
}

// retentionRemote is also sealed. The WebDAV implementation validates full
// names, sizes and digests before deletion and confirms absence afterwards.
type retentionRemote interface {
	inspectRetentionTriplet(context.Context, RetentionDeleteTriplet) (RetentionRemoteState, error)
	deleteRetentionObjectVerifiedMissing(context.Context, ArtifactRef) error
}

type RetentionExecutionResult struct {
	IntentID         uuid.UUID
	Action           RetentionReconcileAction
	PreviousVersion  uint64
	CurrentVersion   uint64
	PreviousProgress RetentionProgress
	CurrentProgress  RetentionProgress
	AbsenceConfirmed bool
	Completed        bool
}

type RetentionCompletionReceipt struct {
	IntentID        uuid.UUID
	PreviousVersion uint64
	CurrentVersion  uint64
	Completed       bool
	CompletedAt     time.Time
	EventID         uuid.UUID
}

// ExecuteRetentionDeletionStep performs at most one remote deletion and one
// durable transition. Re-running after any crash is safe because reconciliation
// accepts exactly one verified remote deletion ahead of durable progress.
func ExecuteRetentionDeletionStep(
	ctx context.Context, store retentionIntentStore, remote retentionRemote, intentID uuid.UUID,
) (RetentionExecutionResult, error) {
	if store == nil || remote == nil || intentID == uuid.Nil {
		return RetentionExecutionResult{}, ErrRetentionExecutorDependency
	}
	intent, err := store.loadRetentionIntent(ctx, intentID)
	if err != nil {
		return RetentionExecutionResult{}, err
	}
	if intent.ID != intentID {
		return RetentionExecutionResult{}, ErrRetentionIntentInvalid
	}
	if err := validateRetentionIntent(intent); err != nil {
		return RetentionExecutionResult{}, err
	}
	remoteState, err := remote.inspectRetentionTriplet(ctx, intent.Triplet)
	if err != nil {
		return RetentionExecutionResult{}, err
	}
	action, err := ReconcileRetentionDeletion(&intent, remoteState)
	if err != nil {
		return RetentionExecutionResult{}, err
	}
	result := RetentionExecutionResult{
		IntentID: intent.ID, Action: action.Action,
		PreviousVersion: intent.Version, CurrentVersion: intent.Version,
		PreviousProgress: intent.Progress, CurrentProgress: intent.Progress,
	}

	switch action.Action {
	case RetentionActionVerifyDeleteManifest, RetentionActionVerifyDeleteChecksum, RetentionActionVerifyDeleteArchive:
		if !action.HasObject || action.ExpectedVersion != intent.Version || intent.Progress >= RetentionProgressArchiveDeleted {
			return RetentionExecutionResult{}, ErrRetentionIntentInvalid
		}
		if err := remote.deleteRetentionObjectVerifiedMissing(ctx, action.Object); err != nil {
			return RetentionExecutionResult{}, err
		}
		next := intent.Progress + 1
		postState, err := remote.inspectRetentionTriplet(ctx, intent.Triplet)
		if err != nil {
			return RetentionExecutionResult{}, err
		}
		postAction, err := ReconcileRetentionDeletion(&intent, postState)
		if err != nil {
			return RetentionExecutionResult{}, err
		}
		if postAction.Action != recordRetentionAction(next) || postAction.HasObject ||
			postAction.ExpectedVersion != intent.Version || postAction.NextProgress != next {
			return RetentionExecutionResult{}, ErrRetentionDeleteUnconfirmed
		}
		updated, err := advanceRetentionIntent(ctx, store, intent, next)
		if err != nil {
			return RetentionExecutionResult{}, err
		}
		result.CurrentVersion = updated.Version
		result.CurrentProgress = updated.Progress
		result.AbsenceConfirmed = true
		return result, nil

	case RetentionActionRecordManifestDeleted, RetentionActionRecordChecksumDeleted, RetentionActionRecordArchiveDeleted:
		if action.HasObject || action.ExpectedVersion != intent.Version || action.NextProgress != intent.Progress+1 {
			return RetentionExecutionResult{}, ErrRetentionIntentInvalid
		}
		updated, err := advanceRetentionIntent(ctx, store, intent, action.NextProgress)
		if err != nil {
			return RetentionExecutionResult{}, err
		}
		result.CurrentVersion = updated.Version
		result.CurrentProgress = updated.Progress
		return result, nil

	case RetentionActionComplete:
		if action.HasObject || action.ExpectedVersion != intent.Version || intent.Progress != RetentionProgressArchiveDeleted {
			return RetentionExecutionResult{}, ErrRetentionIntentInvalid
		}
		receipt, err := store.completeRetentionIntent(ctx, intent)
		if err != nil {
			return RetentionExecutionResult{}, err
		}
		if !receipt.Completed || receipt.IntentID != intent.ID || receipt.PreviousVersion != intent.Version ||
			intent.Version >= math.MaxInt64 || receipt.CurrentVersion != intent.Version+1 ||
			receipt.CompletedAt.IsZero() || receipt.EventID == uuid.Nil {
			return RetentionExecutionResult{}, ErrRetentionIntentCASInvalid
		}
		result.CurrentVersion = receipt.CurrentVersion
		result.Completed = true
		return result, nil

	default:
		return RetentionExecutionResult{}, ErrRetentionIntentInvalid
	}
}

func advanceRetentionIntent(
	ctx context.Context, store retentionIntentStore, expected RetentionDeleteIntent, next RetentionProgress,
) (RetentionDeleteIntent, error) {
	if expected.Version >= math.MaxInt64 || next != expected.Progress+1 || next > RetentionProgressArchiveDeleted {
		return RetentionDeleteIntent{}, ErrRetentionIntentCASInvalid
	}
	updated, err := store.compareAndSwapRetentionProgress(ctx, expected, next)
	if err != nil {
		return RetentionDeleteIntent{}, err
	}
	if updated.ID != expected.ID || updated.Version != expected.Version+1 || updated.Progress != next ||
		updated.PolicySHA256 != expected.PolicySHA256 || updated.PlanSHA256 != expected.PlanSHA256 ||
		updated.Triplet != expected.Triplet {
		return RetentionDeleteIntent{}, ErrRetentionIntentCASInvalid
	}
	if err := validateRetentionIntent(updated); err != nil {
		return RetentionDeleteIntent{}, ErrRetentionIntentCASInvalid
	}
	return updated, nil
}
