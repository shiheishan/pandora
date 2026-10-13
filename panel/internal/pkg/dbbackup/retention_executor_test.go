package dbbackup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var errRetentionTestCAS = errors.New("retention test cas conflict")

type retentionStoreFake struct {
	intent       RetentionDeleteIntent
	loadErr      error
	casErr       error
	completeErr  error
	completeNoop bool
	drift        bool
	loads        int
	casWrites    int
	completes    int
}

func (s *retentionStoreFake) loadRetentionIntent(_ context.Context, _ uuid.UUID) (RetentionDeleteIntent, error) {
	s.loads++
	return s.intent, s.loadErr
}

func (s *retentionStoreFake) compareAndSwapRetentionProgress(
	_ context.Context, expected RetentionDeleteIntent, next RetentionProgress,
) (RetentionDeleteIntent, error) {
	s.casWrites++
	if s.casErr != nil {
		return RetentionDeleteIntent{}, s.casErr
	}
	updated := expected
	updated.Version++
	updated.Progress = next
	if s.drift {
		updated.PlanSHA256 = updated.PolicySHA256
	}
	s.intent = updated
	return updated, nil
}

func (s *retentionStoreFake) completeRetentionIntent(_ context.Context, expected RetentionDeleteIntent) (RetentionCompletionReceipt, error) {
	s.completes++
	if s.completeErr != nil {
		return RetentionCompletionReceipt{}, s.completeErr
	}
	if s.completeNoop {
		return RetentionCompletionReceipt{}, nil
	}
	return RetentionCompletionReceipt{
		IntentID: expected.ID, PreviousVersion: expected.Version,
		CurrentVersion: expected.Version + 1, Completed: true,
		CompletedAt: time.Date(2026, 8, 2, 4, 0, 0, 0, time.UTC),
		EventID:     uuid.MustParse("22222222-2222-4222-8222-222222222222"),
	}, nil
}

type retentionRemoteFake struct {
	state       RetentionRemoteState
	states      []RetentionRemoteState
	inspectErr  error
	deleteErr   error
	inspections int
	deletions   []ArtifactRef
}

func (r *retentionRemoteFake) inspectRetentionTriplet(
	_ context.Context, _ RetentionDeleteTriplet,
) (RetentionRemoteState, error) {
	r.inspections++
	if len(r.states) > 0 {
		state := r.states[0]
		r.states = r.states[1:]
		return state, r.inspectErr
	}
	return r.state, r.inspectErr
}

func (r *retentionRemoteFake) deleteRetentionObjectVerifiedMissing(_ context.Context, ref ArtifactRef) error {
	r.deletions = append(r.deletions, ref)
	return r.deleteErr
}

func TestExecuteRetentionDeletionStepDeletesThenCheckpoints(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	store := &retentionStoreFake{intent: intent}
	remote := &retentionRemoteFake{states: []RetentionRemoteState{remotePrefix(0), remotePrefix(1)}}
	result, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != RetentionActionVerifyDeleteManifest || !result.AbsenceConfirmed || result.Completed ||
		result.PreviousVersion != 7 || result.CurrentVersion != 8 ||
		result.PreviousProgress != RetentionProgressPending || result.CurrentProgress != RetentionProgressManifestDeleted {
		t.Fatalf("result=%#v", result)
	}
	if len(remote.deletions) != 1 || remote.deletions[0] != intent.Triplet.Manifest || store.casWrites != 1 {
		t.Fatalf("deletions=%#v cas=%d", remote.deletions, store.casWrites)
	}
}

func TestExecuteRetentionDeletionStepRecoversDeleteBeforeCAS(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	store := &retentionStoreFake{intent: intent}
	remote := &retentionRemoteFake{state: remotePrefix(1)}
	result, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != RetentionActionRecordManifestDeleted || result.AbsenceConfirmed ||
		result.CurrentProgress != RetentionProgressManifestDeleted || len(remote.deletions) != 0 || store.casWrites != 1 {
		t.Fatalf("result=%#v deletions=%d cas=%d", result, len(remote.deletions), store.casWrites)
	}
}

func TestExecuteRetentionDeletionStepStopsBeforeCASOnRemoteFailure(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	for _, remote := range []*retentionRemoteFake{
		{inspectErr: ErrRetentionRemoteAmbiguous},
		{state: remotePrefix(0), deleteErr: ErrRetentionRemoteMismatch},
	} {
		store := &retentionStoreFake{intent: intent}
		if _, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID); err == nil {
			t.Fatal("remote failure succeeded")
		}
		if store.casWrites != 0 || store.completes != 0 {
			t.Fatalf("remote failure wrote state: cas=%d completes=%d", store.casWrites, store.completes)
		}
	}
}

func TestExecuteRetentionDeletionStepLeavesCrashRecoveryAfterCASConflict(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	store := &retentionStoreFake{intent: intent, casErr: errRetentionTestCAS}
	remote := &retentionRemoteFake{states: []RetentionRemoteState{remotePrefix(0), remotePrefix(1)}}
	_, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID)
	if !errors.Is(err, errRetentionTestCAS) || len(remote.deletions) != 1 || store.casWrites != 1 {
		t.Fatalf("err=%v deletions=%d cas=%d", err, len(remote.deletions), store.casWrites)
	}
	// The next run observes the one-step-ahead remote prefix and records it
	// without issuing a second DELETE.
	store.casErr = nil
	remote.state = remotePrefix(1)
	remote.states = nil
	remote.deletions = nil
	result, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID)
	if err != nil || result.Action != RetentionActionRecordManifestDeleted || len(remote.deletions) != 0 {
		t.Fatalf("retry result=%#v err=%v deletions=%d", result, err, len(remote.deletions))
	}
}

func TestExecuteRetentionDeletionStepRejectsCASBindingDrift(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	store := &retentionStoreFake{intent: intent, drift: true}
	remote := &retentionRemoteFake{state: remotePrefix(1)}
	if _, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID); !errors.Is(err, ErrRetentionIntentCASInvalid) {
		t.Fatalf("err=%v, want CAS binding failure", err)
	}
}

func TestExecuteRetentionDeletionStepCompletesOnlyTerminalRemoteState(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressArchiveDeleted)
	store := &retentionStoreFake{intent: intent}
	remote := &retentionRemoteFake{state: remotePrefix(3)}
	result, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID)
	if err != nil || !result.Completed || result.Action != RetentionActionComplete || store.completes != 1 {
		t.Fatalf("result=%#v err=%v completes=%d", result, err, store.completes)
	}
	store.completes = 0
	remote.state = remotePrefix(2)
	if _, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID); !errors.Is(err, ErrRetentionTerminalDivergence) {
		t.Fatalf("err=%v, want terminal divergence", err)
	}
	if store.completes != 0 {
		t.Fatal("divergent terminal state was completed")
	}
	store.completeNoop = true
	remote.state = remotePrefix(3)
	if _, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID); !errors.Is(err, ErrRetentionIntentCASInvalid) {
		t.Fatalf("no-op completion err=%v", err)
	}
}

func TestExecuteRetentionDeletionStepRejectsConcurrentExtraLossBeforeCAS(t *testing.T) {
	tests := []struct {
		name     string
		progress RetentionProgress
		before   RetentionRemoteState
		after    RetentionRemoteState
	}{
		{name: "pending EEE to MME", progress: RetentionProgressPending, before: remotePrefix(0), after: remotePrefix(2)},
		{name: "manifest MEE to MMM", progress: RetentionProgressManifestDeleted, before: remotePrefix(1), after: remotePrefix(3)},
		{name: "post delete mismatch", progress: RetentionProgressPending, before: remotePrefix(0), after: RetentionRemoteState{
			Manifest: RetentionRemoteMissing, Checksum: RetentionRemoteMismatch, Archive: RetentionRemoteExact,
		}},
		{name: "post delete non prefix", progress: RetentionProgressPending, before: remotePrefix(0), after: RetentionRemoteState{
			Manifest: RetentionRemoteExact, Checksum: RetentionRemoteMissing, Archive: RetentionRemoteExact,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := retentionIntentFixture(test.progress)
			store := &retentionStoreFake{intent: intent}
			remote := &retentionRemoteFake{states: []RetentionRemoteState{test.before, test.after}}
			if _, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, intent.ID); err == nil {
				t.Fatal("unsafe post-delete state succeeded")
			}
			if len(remote.deletions) != 1 || store.casWrites != 0 || store.completes != 0 {
				t.Fatalf("deletions=%d cas=%d completes=%d", len(remote.deletions), store.casWrites, store.completes)
			}
		})
	}
}

func TestExecuteRetentionDeletionStepRejectsMissingDependencies(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	store := &retentionStoreFake{intent: intent}
	remote := &retentionRemoteFake{state: remotePrefix(0)}
	if _, err := ExecuteRetentionDeletionStep(context.Background(), nil, remote, intent.ID); !errors.Is(err, ErrRetentionExecutorDependency) {
		t.Fatalf("nil store err=%v", err)
	}
	if _, err := ExecuteRetentionDeletionStep(context.Background(), store, nil, intent.ID); !errors.Is(err, ErrRetentionExecutorDependency) {
		t.Fatalf("nil remote err=%v", err)
	}
	if _, err := ExecuteRetentionDeletionStep(context.Background(), store, remote, uuid.Nil); !errors.Is(err, ErrRetentionExecutorDependency) {
		t.Fatalf("nil id err=%v", err)
	}
}
