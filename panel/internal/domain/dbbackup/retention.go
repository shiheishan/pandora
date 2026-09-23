package dbbackup

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	RetentionReasonNewest              = "newest"
	RetentionReasonMinimumCopies       = "minimum_copies"
	RetentionReasonAgeWindow           = "age_window"
	RetentionReasonPinned              = "pinned"
	RetentionReasonLastSuccessfulDrill = "last_successful_drill"
	RetentionReasonCheckpoint          = "checkpoint_authorized"
	RetentionReasonRetryPending        = "retry_pending"

	RetentionWarningCapacityUnsatisfied = "backup_retention_capacity_unsatisfied"
	maxRetentionDays                    = 36500
)

type RetentionPolicy struct {
	Days      int
	MinCopies int
	// MaxBytes is zero when no capacity ceiling is configured.
	MaxBytes int64
}

// RetentionArtifact is an immutable database inventory record admitted only
// after the signed manifest and all three remote objects were verified.
type RetentionArtifact struct {
	BackupID  string
	Sequence  uint64
	CreatedAt time.Time

	Archive  ArtifactRef
	Checksum ArtifactRef
	Manifest ArtifactRef

	ManifestVerified     bool
	Pinned               bool
	SuccessfulDrillAt    time.Time
	CheckpointAuthorized bool
	RetryPending         bool
}

type RetentionProtectedArtifact struct {
	BackupID string
	Sequence uint64
	Bytes    int64
	Reasons  []string
}

type RetentionDeleteTriplet struct {
	BackupID string
	Sequence uint64
	Bytes    int64
	Manifest ArtifactRef
	Checksum ArtifactRef
	Archive  ArtifactRef
}

// OrderedObjects returns the only safe deletion order. Removing the manifest
// first prevents a crash from leaving a manifest that advertises missing data.
func (d RetentionDeleteTriplet) OrderedObjects() [3]ArtifactRef {
	return [3]ArtifactRef{d.Manifest, d.Checksum, d.Archive}
}

type RetentionWarning struct {
	Code           string
	LimitBytes     int64
	ProjectedBytes int64
	ShortfallBytes int64
}

type RetentionPlan struct {
	EvaluatedAt    time.Time
	Cutoff         time.Time
	Protected      []RetentionProtectedArtifact
	Delete         []RetentionDeleteTriplet
	Warnings       []RetentionWarning
	TotalBytes     int64
	ProjectedBytes int64
	ReclaimBytes   int64
}

// PlanRetention is side-effect free. Any invalid, incomplete, unverified, or
// ambiguous inventory returns an empty plan and an error; callers must never
// execute remote deletion from an error result.
func PlanRetention(now time.Time, policy RetentionPolicy, artifacts []RetentionArtifact) (RetentionPlan, error) {
	if now.IsZero() {
		return RetentionPlan{}, errors.New("retention clock is required")
	}
	// Reject a zero/default duration so an uninitialized policy cannot collapse
	// the inventory to only MinCopies. The upper bound also keeps AddDate safe.
	if policy.Days < 1 || policy.Days > maxRetentionDays || policy.MinCopies < 1 || policy.MaxBytes < 0 {
		return RetentionPlan{}, errors.New("retention policy is invalid")
	}

	now = now.UTC()
	items := append([]RetentionArtifact(nil), artifacts...)
	seenIDs := make(map[string]struct{}, len(items))
	seenSequences := make(map[uint64]struct{}, len(items))
	seenObjects := make(map[string]struct{}, len(items)*3)
	var total int64
	var latestDrill time.Time
	for i := range items {
		item := &items[i]
		created, err := time.Parse("20060102T150405Z", item.BackupID)
		if err != nil || item.Sequence == 0 || item.CreatedAt.IsZero() {
			return RetentionPlan{}, fmt.Errorf("retention artifact %d identity is invalid", i)
		}
		item.CreatedAt = item.CreatedAt.UTC()
		if !item.CreatedAt.Equal(created.UTC()) || item.CreatedAt.After(now) {
			return RetentionPlan{}, fmt.Errorf("retention artifact %q clock is invalid", item.BackupID)
		}
		if _, exists := seenIDs[item.BackupID]; exists {
			return RetentionPlan{}, fmt.Errorf("duplicate retention backup id %q", item.BackupID)
		}
		if _, exists := seenSequences[item.Sequence]; exists {
			return RetentionPlan{}, fmt.Errorf("duplicate retention sequence %d", item.Sequence)
		}
		seenIDs[item.BackupID] = struct{}{}
		seenSequences[item.Sequence] = struct{}{}
		if !item.ManifestVerified {
			return RetentionPlan{}, fmt.Errorf("retention artifact %q manifest is not verified", item.BackupID)
		}

		prefix := "aegis-postgres-" + item.BackupID
		if err := validateRetentionRef(item.Archive, prefix+".dump.age", 0); err != nil {
			return RetentionPlan{}, fmt.Errorf("retention artifact %q archive: %w", item.BackupID, err)
		}
		if err := validateRetentionRef(item.Checksum, prefix+".dump.age.sha256", 256); err != nil {
			return RetentionPlan{}, fmt.Errorf("retention artifact %q checksum: %w", item.BackupID, err)
		}
		if err := validateRetentionRef(item.Manifest, prefix+".manifest.json", maxManifestBytes); err != nil {
			return RetentionPlan{}, fmt.Errorf("retention artifact %q manifest: %w", item.BackupID, err)
		}
		for _, ref := range [...]ArtifactRef{item.Archive, item.Checksum, item.Manifest} {
			if _, exists := seenObjects[ref.Name]; exists {
				return RetentionPlan{}, fmt.Errorf("duplicate retention object %q", ref.Name)
			}
			seenObjects[ref.Name] = struct{}{}
		}

		if !item.SuccessfulDrillAt.IsZero() {
			item.SuccessfulDrillAt = item.SuccessfulDrillAt.UTC()
			if item.SuccessfulDrillAt.Before(item.CreatedAt) || item.SuccessfulDrillAt.After(now) {
				return RetentionPlan{}, fmt.Errorf("retention artifact %q drill clock is invalid", item.BackupID)
			}
			if item.SuccessfulDrillAt.After(latestDrill) {
				latestDrill = item.SuccessfulDrillAt
			}
		}

		bytes, err := sumRetentionBytes(item.Archive.Bytes, item.Checksum.Bytes, item.Manifest.Bytes)
		if err != nil {
			return RetentionPlan{}, fmt.Errorf("retention artifact %q size overflow", item.BackupID)
		}
		total, err = addRetentionBytes(total, bytes)
		if err != nil {
			return RetentionPlan{}, errors.New("retention inventory size overflow")
		}
	}

	// Sequence is the canonical signed history. Gaps are permitted, but time
	// must move strictly forward as the sequence increases.
	sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	for i := 1; i < len(items); i++ {
		if !items[i].CreatedAt.After(items[i-1].CreatedAt) {
			return RetentionPlan{}, errors.New("retention inventory sequence and clock are not monotonic")
		}
	}

	minimumStart := 0
	if policy.MinCopies < len(items) {
		minimumStart = len(items) - policy.MinCopies
	}
	cutoff := now.AddDate(0, 0, -policy.Days)
	plan := RetentionPlan{EvaluatedAt: now, Cutoff: cutoff, TotalBytes: total}
	for i, item := range items {
		bytes, _ := sumRetentionBytes(item.Archive.Bytes, item.Checksum.Bytes, item.Manifest.Bytes)
		reasons := make([]string, 0, 7)
		if i == len(items)-1 {
			reasons = append(reasons, RetentionReasonNewest)
		}
		if i >= minimumStart {
			reasons = append(reasons, RetentionReasonMinimumCopies)
		}
		if !item.CreatedAt.Before(cutoff) {
			reasons = append(reasons, RetentionReasonAgeWindow)
		}
		if item.Pinned {
			reasons = append(reasons, RetentionReasonPinned)
		}
		if !latestDrill.IsZero() && item.SuccessfulDrillAt.Equal(latestDrill) {
			reasons = append(reasons, RetentionReasonLastSuccessfulDrill)
		}
		if item.CheckpointAuthorized {
			reasons = append(reasons, RetentionReasonCheckpoint)
		}
		if item.RetryPending {
			reasons = append(reasons, RetentionReasonRetryPending)
		}
		if len(reasons) > 0 {
			plan.Protected = append(plan.Protected, RetentionProtectedArtifact{
				BackupID: item.BackupID, Sequence: item.Sequence, Bytes: bytes, Reasons: reasons,
			})
			plan.ProjectedBytes, _ = addRetentionBytes(plan.ProjectedBytes, bytes)
			continue
		}
		plan.Delete = append(plan.Delete, RetentionDeleteTriplet{
			BackupID: item.BackupID, Sequence: item.Sequence, Bytes: bytes,
			Manifest: item.Manifest, Checksum: item.Checksum, Archive: item.Archive,
		})
		plan.ReclaimBytes, _ = addRetentionBytes(plan.ReclaimBytes, bytes)
	}
	if policy.MaxBytes > 0 && plan.ProjectedBytes > policy.MaxBytes {
		plan.Warnings = append(plan.Warnings, RetentionWarning{
			Code: RetentionWarningCapacityUnsatisfied, LimitBytes: policy.MaxBytes,
			ProjectedBytes: plan.ProjectedBytes, ShortfallBytes: plan.ProjectedBytes - policy.MaxBytes,
		})
	}
	return plan, nil
}

func validateRetentionRef(ref ArtifactRef, expectedName string, maxBytes int64) error {
	if ref.Name != expectedName || ref.Bytes <= 0 || !validLowerSHA256(ref.SHA256) {
		return errors.New("object binding is invalid")
	}
	if maxBytes > 0 && ref.Bytes > maxBytes {
		return errors.New("object size exceeds the allowed maximum")
	}
	return nil
}

func sumRetentionBytes(values ...int64) (int64, error) {
	var total int64
	var err error
	for _, value := range values {
		total, err = addRetentionBytes(total, value)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func addRetentionBytes(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("retention size overflow")
	}
	return left + right, nil
}
