package dbbackup

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func retentionFixture(id string, sequence uint64) RetentionArtifact {
	created, err := time.Parse("20060102T150405Z", id)
	if err != nil {
		panic(err)
	}
	prefix := "aegis-postgres-" + id
	return RetentionArtifact{
		BackupID: id, Sequence: sequence, CreatedAt: created,
		Archive:          ArtifactRef{Name: prefix + ".dump.age", Bytes: 100, SHA256: strings.Repeat("a", 64)},
		Checksum:         ArtifactRef{Name: prefix + ".dump.age.sha256", Bytes: 10, SHA256: strings.Repeat("b", 64)},
		Manifest:         ArtifactRef{Name: prefix + ".manifest.json", Bytes: 20, SHA256: strings.Repeat("c", 64)},
		ManifestVerified: true,
	}
}

func TestPlanRetentionProtectsEverySafetyClass(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	items := []RetentionArtifact{
		retentionFixture("20260701T120000Z", 1),
		retentionFixture("20260702T120000Z", 2),
		retentionFixture("20260703T120000Z", 3),
		retentionFixture("20260704T120000Z", 4),
		retentionFixture("20260705T120000Z", 5),
		retentionFixture("20260801T120000Z", 6),
		retentionFixture("20260802T120000Z", 7),
	}
	items[1].Pinned = true
	items[2].SuccessfulDrillAt = now.Add(-time.Hour)
	items[3].CheckpointAuthorized = true
	items[4].RetryPending = true

	plan, err := PlanRetention(now, RetentionPolicy{Days: 2, MinCopies: 1}, items)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, item := range plan.Protected {
		got[item.BackupID] = item.Reasons
	}
	checks := map[string]string{
		"20260702T120000Z": RetentionReasonPinned,
		"20260703T120000Z": RetentionReasonLastSuccessfulDrill,
		"20260704T120000Z": RetentionReasonCheckpoint,
		"20260705T120000Z": RetentionReasonRetryPending,
		"20260801T120000Z": RetentionReasonAgeWindow,
		"20260802T120000Z": RetentionReasonNewest,
	}
	for id, reason := range checks {
		if !containsRetentionReason(got[id], reason) {
			t.Fatalf("%s missing protection %s in %v", id, reason, got[id])
		}
	}
	if len(plan.Delete) != 1 || plan.Delete[0].BackupID != "20260701T120000Z" {
		t.Fatalf("unexpected deletion plan: %#v", plan.Delete)
	}
}

func TestPlanRetentionDeletionOrderAndOldestFirst(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	items := []RetentionArtifact{
		retentionFixture("20260802T120000Z", 3),
		retentionFixture("20260701T120000Z", 1),
		retentionFixture("20260702T120000Z", 2),
	}
	plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Delete) != 2 || plan.Delete[0].Sequence != 1 || plan.Delete[1].Sequence != 2 {
		t.Fatalf("deletions are not oldest first: %#v", plan.Delete)
	}
	got := plan.Delete[0].OrderedObjects()
	want := [3]ArtifactRef{items[1].Manifest, items[1].Checksum, items[1].Archive}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsafe delete order: got %v want %v", got, want)
	}
}

func TestPlanRetentionFailsClosedOnIncompleteOrUnverifiedTriplet(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	valid := retentionFixture("20260701T120000Z", 1)
	baseInvalid := retentionFixture("20260702T120000Z", 2)
	for name, mutate := range map[string]func(*RetentionArtifact){
		"unverified": func(item *RetentionArtifact) { item.ManifestVerified = false },
		"missing":    func(item *RetentionArtifact) { item.Checksum = ArtifactRef{} },
		"bad digest": func(item *RetentionArtifact) { item.Manifest.SHA256 = strings.Repeat("A", 64) },
		"bad name":   func(item *RetentionArtifact) { item.Archive.Name = "other.dump.age" },
	} {
		t.Run(name, func(t *testing.T) {
			item := baseInvalid
			mutate(&item)
			plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, []RetentionArtifact{valid, item})
			if err == nil || len(plan.Delete) != 0 || len(plan.Protected) != 0 {
				t.Fatalf("expected empty fail-closed plan, got %#v err=%v", plan, err)
			}
		})
	}
}

func TestPlanRetentionRejectsIdentityClockAndSizeDefects(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	base := retentionFixture("20260701T120000Z", 1)
	for name, mutate := range map[string]func(*RetentionArtifact){
		"malformed id":      func(item *RetentionArtifact) { item.BackupID = "bad" },
		"created mismatch":  func(item *RetentionArtifact) { item.CreatedAt = item.CreatedAt.Add(time.Second) },
		"zero sequence":     func(item *RetentionArtifact) { item.Sequence = 0 },
		"negative size":     func(item *RetentionArtifact) { item.Archive.Bytes = -1 },
		"zero size":         func(item *RetentionArtifact) { item.Checksum.Bytes = 0 },
		"oversize checksum": func(item *RetentionArtifact) { item.Checksum.Bytes = 257 },
		"oversize manifest": func(item *RetentionArtifact) { item.Manifest.Bytes = maxManifestBytes + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			item := base
			mutate(&item)
			plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, []RetentionArtifact{item})
			if err == nil || len(plan.Delete) != 0 || len(plan.Protected) != 0 {
				t.Fatalf("defective identity or object accepted: %#v err=%v", plan, err)
			}
		})
	}
	if plan, err := PlanRetention(time.Time{}, RetentionPolicy{Days: 1, MinCopies: 1}, []RetentionArtifact{base}); err == nil || len(plan.Delete) != 0 {
		t.Fatalf("zero clock accepted: %#v err=%v", plan, err)
	}
}

func TestPlanRetentionWarnsWhenHardProtectionExceedsCapacity(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	item := retentionFixture("20260802T120000Z", 1)
	plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1, MaxBytes: 50}, []RetentionArtifact{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != RetentionWarningCapacityUnsatisfied ||
		plan.Warnings[0].ProjectedBytes != 130 || plan.Warnings[0].ShortfallBytes != 80 {
		t.Fatalf("missing capacity warning: %#v", plan.Warnings)
	}
}

func TestPlanRetentionNoWarningWhenCapacityIsAchievable(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	items := []RetentionArtifact{
		retentionFixture("20260701T120000Z", 1),
		retentionFixture("20260802T120000Z", 2),
	}
	plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1, MaxBytes: 130}, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 0 || plan.ProjectedBytes != 130 || plan.ReclaimBytes != 130 {
		t.Fatalf("achievable capacity was misreported: %#v", plan)
	}
}

func TestPlanRetentionRejectsAggregateInventoryOverflow(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	first := retentionFixture("20260701T120000Z", 1)
	second := retentionFixture("20260702T120000Z", 2)
	first.Archive.Bytes = math.MaxInt64 - first.Checksum.Bytes - first.Manifest.Bytes
	plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, []RetentionArtifact{first, second})
	if err == nil || len(plan.Delete) != 0 || plan.TotalBytes != 0 {
		t.Fatalf("aggregate overflow accepted: %#v err=%v", plan, err)
	}
}

func TestPlanRetentionRejectsAmbiguousInventoryWithoutPartialPlan(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	base := retentionFixture("20260701T120000Z", 1)
	cases := map[string][]RetentionArtifact{
		"duplicate id":       {base, base},
		"duplicate sequence": {base, retentionFixture("20260702T120000Z", 1)},
		"reversed sequence":  {retentionFixture("20260701T120000Z", 3), retentionFixture("20260702T120000Z", 2), retentionFixture("20260703T120000Z", 1)},
		"future":             {retentionFixture("20260803T120000Z", 2)},
		"overflow": {func() RetentionArtifact {
			v := base
			v.Archive.Bytes = math.MaxInt64
			return v
		}()},
	}
	for name, artifacts := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, artifacts)
			if err == nil || len(plan.Delete) != 0 || plan.TotalBytes != 0 {
				t.Fatalf("expected empty fail-closed result, got plan=%#v err=%v", plan, err)
			}
		})
	}
}

func TestPlanRetentionSelectsOnlyLatestSuccessfulDrillAndTies(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	items := []RetentionArtifact{
		retentionFixture("20260701T120000Z", 1),
		retentionFixture("20260702T120000Z", 2),
		retentionFixture("20260703T120000Z", 3),
		retentionFixture("20260802T120000Z", 4),
	}
	items[0].SuccessfulDrillAt = now.Add(-2 * time.Hour)
	items[1].SuccessfulDrillAt = now.Add(-time.Hour)
	items[2].SuccessfulDrillAt = now.Add(-time.Hour)
	plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, items)
	if err != nil {
		t.Fatal(err)
	}
	protected := map[string][]string{}
	for _, item := range plan.Protected {
		protected[item.BackupID] = item.Reasons
	}
	if containsRetentionReason(protected[items[0].BackupID], RetentionReasonLastSuccessfulDrill) ||
		!containsRetentionReason(protected[items[1].BackupID], RetentionReasonLastSuccessfulDrill) ||
		!containsRetentionReason(protected[items[2].BackupID], RetentionReasonLastSuccessfulDrill) {
		t.Fatalf("latest drill tie selection is wrong: %#v", protected)
	}
}

func TestPlanRetentionHugeMinimumCopiesAndInclusiveCutoff(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	item := retentionFixture("20260801T120000Z", 1)
	plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: math.MaxInt}, []RetentionArtifact{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Protected) != 1 || !containsRetentionReason(plan.Protected[0].Reasons, RetentionReasonMinimumCopies) ||
		!containsRetentionReason(plan.Protected[0].Reasons, RetentionReasonAgeWindow) {
		t.Fatalf("huge minimum or cutoff was unsafe: %#v", plan)
	}
}

func TestPlanRetentionInputAndPolicyValidation(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	for name, policy := range map[string]RetentionPolicy{
		"negative days":  {Days: -1, MinCopies: 1},
		"zero days":      {Days: 0, MinCopies: 1},
		"excessive days": {Days: maxRetentionDays + 1, MinCopies: 1},
		"zero copies":    {Days: 1, MinCopies: 0},
		"negative cap":   {Days: 1, MinCopies: 1, MaxBytes: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if plan, err := PlanRetention(now, policy, nil); err == nil || len(plan.Delete) != 0 {
				t.Fatalf("invalid policy accepted: %#v %v", plan, err)
			}
		})
	}
	if plan, err := PlanRetention(now, RetentionPolicy{Days: 1, MinCopies: 1}, nil); err != nil ||
		len(plan.Delete) != 0 || !plan.EvaluatedAt.Equal(now) {
		t.Fatalf("empty inventory should be a valid empty plan: %#v %v", plan, err)
	}
}

func containsRetentionReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
