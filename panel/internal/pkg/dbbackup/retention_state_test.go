package dbbackup

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func retentionIntentFixture(progress RetentionProgress) RetentionDeleteIntent {
	item := retentionFixture("20260701T120000Z", 1)
	return RetentionDeleteIntent{
		ID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Version: 7,
		PolicySHA256: strings.Repeat("d", 64), PlanSHA256: strings.Repeat("e", 64), Progress: progress,
		Triplet: RetentionDeleteTriplet{
			BackupID: item.BackupID, Sequence: item.Sequence, Bytes: 130,
			Manifest: item.Manifest, Checksum: item.Checksum, Archive: item.Archive,
		},
	}
}

func remotePrefix(missing int) RetentionRemoteState {
	states := [3]RetentionRemoteObjectState{RetentionRemoteExact, RetentionRemoteExact, RetentionRemoteExact}
	for i := 0; i < missing; i++ {
		states[i] = RetentionRemoteMissing
	}
	return RetentionRemoteState{Manifest: states[0], Checksum: states[1], Archive: states[2]}
}

func TestReconcileRetentionDeletionExhaustivePrefixTable(t *testing.T) {
	type key struct {
		progress RetentionProgress
		missing  int
	}
	want := map[key]RetentionReconcileAction{
		{RetentionProgressPending, 0}:         RetentionActionVerifyDeleteManifest,
		{RetentionProgressPending, 1}:         RetentionActionRecordManifestDeleted,
		{RetentionProgressManifestDeleted, 1}: RetentionActionVerifyDeleteChecksum,
		{RetentionProgressManifestDeleted, 2}: RetentionActionRecordChecksumDeleted,
		{RetentionProgressChecksumDeleted, 2}: RetentionActionVerifyDeleteArchive,
		{RetentionProgressChecksumDeleted, 3}: RetentionActionRecordArchiveDeleted,
		{RetentionProgressArchiveDeleted, 3}:  RetentionActionComplete,
	}
	for progress := RetentionProgressPending; progress <= RetentionProgressArchiveDeleted; progress++ {
		for missing := 0; missing <= 3; missing++ {
			t.Run(string(rune('0'+progress))+string(rune('0'+missing)), func(t *testing.T) {
				intent := retentionIntentFixture(progress)
				got, err := ReconcileRetentionDeletion(&intent, remotePrefix(missing))
				action, ok := want[key{progress, missing}]
				if ok {
					if err != nil || got.Action != action {
						t.Fatalf("valid state got=%#v err=%v want=%s", got, err, action)
					}
					if got.ExpectedVersion != intent.Version {
						t.Fatalf("accepted state lost CAS version: %#v", got)
					}
					switch action {
					case RetentionActionRecordManifestDeleted, RetentionActionRecordChecksumDeleted, RetentionActionRecordArchiveDeleted:
						if got.NextProgress != RetentionProgress(missing) || got.HasObject || got.Object != (ArtifactRef{}) {
							t.Fatalf("record action payload is unsafe: %#v", got)
						}
					case RetentionActionComplete:
						if got.NextProgress != progress || got.HasObject || got.Object != (ArtifactRef{}) {
							t.Fatalf("terminal action payload is unsafe: %#v", got)
						}
					default:
						if got.NextProgress != progress || !got.HasObject || got.Object == (ArtifactRef{}) {
							t.Fatalf("delete action payload is unsafe: %#v", got)
						}
					}
					return
				}
				if err == nil || got.Action != "" {
					t.Fatalf("unsafe state accepted: %#v err=%v", got, err)
				}
			})
		}
	}
}

func TestReconcileRetentionDeletionReturnsExactPlannedObjects(t *testing.T) {
	for progress, want := range map[RetentionProgress]ArtifactRef{
		RetentionProgressPending:         retentionIntentFixture(0).Triplet.Manifest,
		RetentionProgressManifestDeleted: retentionIntentFixture(0).Triplet.Checksum,
		RetentionProgressChecksumDeleted: retentionIntentFixture(0).Triplet.Archive,
	} {
		intent := retentionIntentFixture(progress)
		got, err := ReconcileRetentionDeletion(&intent, remotePrefix(int(progress)))
		if err != nil || !got.HasObject || !reflect.DeepEqual(got.Object, want) ||
			got.NextProgress != progress || got.ExpectedVersion != intent.Version {
			t.Fatalf("progress %d returned wrong object: %#v err=%v", progress, got, err)
		}
	}
	intent := retentionIntentFixture(RetentionProgressArchiveDeleted)
	got, err := ReconcileRetentionDeletion(&intent, remotePrefix(3))
	if err != nil || got.HasObject || got.Action != RetentionActionComplete {
		t.Fatalf("terminal state is not idempotent: %#v err=%v", got, err)
	}
}

func TestReconcileRetentionDeletionClassifiesAheadStates(t *testing.T) {
	storeAhead := retentionIntentFixture(RetentionProgressManifestDeleted)
	if _, err := ReconcileRetentionDeletion(&storeAhead, remotePrefix(0)); !errors.Is(err, ErrRetentionStoreAhead) {
		t.Fatalf("store-ahead state not refused: %v", err)
	}
	remoteAhead := retentionIntentFixture(RetentionProgressPending)
	if _, err := ReconcileRetentionDeletion(&remoteAhead, remotePrefix(2)); !errors.Is(err, ErrRetentionRemoteTooFar) {
		t.Fatalf("multi-step remote-ahead state not refused: %v", err)
	}
}

func TestReconcileRetentionDeletionRejectsTerminalDivergence(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressArchiveDeleted)
	for missing := 0; missing < 3; missing++ {
		if _, err := ReconcileRetentionDeletion(&intent, remotePrefix(missing)); !errors.Is(err, ErrRetentionTerminalDivergence) {
			t.Fatalf("terminal prefix %d did not diverge: %v", missing, err)
		}
	}
	nonPrefix := RetentionRemoteState{
		Manifest: RetentionRemoteExact, Checksum: RetentionRemoteMissing, Archive: RetentionRemoteExact,
	}
	if _, err := ReconcileRetentionDeletion(&intent, nonPrefix); !errors.Is(err, ErrRetentionTerminalDivergence) {
		t.Fatalf("terminal non-prefix did not diverge: %v", err)
	}
	for _, ambiguous := range []RetentionRemoteObjectState{RetentionRemoteUnknown, RetentionRemoteMismatch} {
		state := remotePrefix(3)
		state.Manifest = ambiguous
		if _, err := ReconcileRetentionDeletion(&intent, state); !errors.Is(err, ErrRetentionRemoteAmbiguous) {
			t.Fatalf("terminal ambiguous state had wrong precedence: %v", err)
		}
	}
}

func TestReconcileRetentionDeletionRejectsAllNonPrefixStates(t *testing.T) {
	intent := retentionIntentFixture(RetentionProgressPending)
	states := map[string]RetentionRemoteState{
		"EEM": {Manifest: RetentionRemoteExact, Checksum: RetentionRemoteExact, Archive: RetentionRemoteMissing},
		"EME": {Manifest: RetentionRemoteExact, Checksum: RetentionRemoteMissing, Archive: RetentionRemoteExact},
		"EMM": {Manifest: RetentionRemoteExact, Checksum: RetentionRemoteMissing, Archive: RetentionRemoteMissing},
		"MEM": {Manifest: RetentionRemoteMissing, Checksum: RetentionRemoteExact, Archive: RetentionRemoteMissing},
	}
	for name, state := range states {
		t.Run(name, func(t *testing.T) {
			if _, err := ReconcileRetentionDeletion(&intent, state); !errors.Is(err, ErrRetentionRemoteNonPrefix) {
				t.Fatalf("non-prefix state had wrong refusal: %v", err)
			}
		})
	}
}

func TestReconcileRetentionDeletionRejectsMissingIntentInvalidIntentAndAmbiguousRemote(t *testing.T) {
	if _, err := ReconcileRetentionDeletion(nil, remotePrefix(0)); !errors.Is(err, ErrRetentionIntentRequired) {
		t.Fatalf("missing durable intent accepted: %v", err)
	}
	for name, mutate := range map[string]func(*RetentionDeleteIntent){
		"zero id":          func(intent *RetentionDeleteIntent) { intent.ID = uuid.Nil },
		"zero version":     func(intent *RetentionDeleteIntent) { intent.Version = 0 },
		"version overflow": func(intent *RetentionDeleteIntent) { intent.Version = math.MaxUint64 },
		"bad policy hash":  func(intent *RetentionDeleteIntent) { intent.PolicySHA256 = "bad" },
		"bad plan hash":    func(intent *RetentionDeleteIntent) { intent.PlanSHA256 = "bad" },
		"bad progress":     func(intent *RetentionDeleteIntent) { intent.Progress = 9 },
		"bad backup id":    func(intent *RetentionDeleteIntent) { intent.Triplet.BackupID = "20261399T999999Z" },
		"bad object":       func(intent *RetentionDeleteIntent) { intent.Triplet.Manifest.SHA256 = "bad" },
		"byte mismatch":    func(intent *RetentionDeleteIntent) { intent.Triplet.Bytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			intent := retentionIntentFixture(RetentionProgressPending)
			mutate(&intent)
			if got, err := ReconcileRetentionDeletion(&intent, remotePrefix(0)); !errors.Is(err, ErrRetentionIntentInvalid) || got.Action != "" {
				t.Fatalf("invalid intent accepted: %#v err=%v", got, err)
			}
		})
	}
	intent := retentionIntentFixture(RetentionProgressPending)
	for _, state := range []RetentionRemoteState{
		{},
		{Manifest: RetentionRemoteMismatch, Checksum: RetentionRemoteExact, Archive: RetentionRemoteExact},
		{Manifest: RetentionRemoteUnknown, Checksum: RetentionRemoteExact, Archive: RetentionRemoteExact},
		{Manifest: 99, Checksum: RetentionRemoteExact, Archive: RetentionRemoteExact},
	} {
		if got, err := ReconcileRetentionDeletion(&intent, state); !errors.Is(err, ErrRetentionRemoteAmbiguous) || got.Action != "" {
			t.Fatalf("ambiguous remote state accepted: %#v err=%v", got, err)
		}
	}
}
