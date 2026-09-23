package releasejournal

import "regexp"

type State = releaseState
type PreparedIdentity = preparedIdentity
type TransitionRecord = transitionRecord
type Snapshot = releaseJournalSnapshot

const (
	FormatV1                    = releaseJournalFormat
	FormatV2                    = releaseJournalFormatV2
	ReleaseCoreFormatV1         = releaseContractCoreFormatV1
	ControllerContractV1        = releaseControllerContractV1
	MaxRecordBytes              = maxRecordBytes
	Prepared              State = statePrepared
	IsolationAttempted    State = stateIsolationAttempted
	Isolated              State = stateIsolated
	BackupAttempted       State = stateBackupAttempted
	BackupVerified        State = stateBackupVerified
	LayoutSwitchAttempted State = stateLayoutSwitchAttempted
	LayoutSwitched        State = stateLayoutSwitched
	AdmissionAttempted    State = stateAdmissionAttempted
	AdmissionVerified     State = stateAdmissionVerified
	MigrationAttempted    State = stateMigrationAttempted
	Migrated              State = stateMigrated
	WritersStartAttempted State = stateWritersStartAttempted
	WritersReady          State = stateWritersReady
	ExposureAttempted     State = stateExposureAttempted
	Committed             State = stateCommitted
	RecoveryRequired      State = stateRecoveryRequired
)

var (
	SafeTokenRE = regexp.MustCompile(safeTokenRE.String())
	Hex64RE     = regexp.MustCompile(hex64RE.String())
	JournalRE   = regexp.MustCompile(journalRE.String())
)

func NormalTransitions() map[State]State {
	result := make(map[State]State, len(normalNext))
	for from, to := range normalNext {
		result[from] = to
	}
	return result
}

func StateSegments() map[State]string {
	result := make(map[State]string, len(stateSegment))
	for state, name := range stateSegment {
		result[state] = name
	}
	return result
}

func SHA256Bytes(value []byte) string { return sha256Bytes(value) }

func ValidatePreparedIdentity(identity PreparedIdentity) error {
	return validatePreparedIdentity(identity)
}

func PreparedRecordBytes(identity PreparedIdentity) ([]byte, string, error) {
	return preparedRecordBytes(identity)
}

func ValidateTransition(previous, next State) error { return validateTransition(previous, next) }

func TransitionRecordBytes(record TransitionRecord) ([]byte, string, error) {
	return transitionRecordBytes(record)
}

func Parse(segments map[string][]byte) (Snapshot, error) { return parseReleaseJournal(segments) }

func ParsePreparedRecord(data []byte) (PreparedIdentity, string, error) {
	return parsePreparedRecord(data)
}

func ParseTransitionRecord(data []byte, prior Snapshot) (TransitionRecord, string, error) {
	return parseTransitionRecord(data, prior)
}

func SnapshotPrefix(snapshot Snapshot, count int) (Snapshot, error) {
	return snapshotPrefix(snapshot, count)
}

func CanonicalPositive(value string) bool { return canonicalPositive(value) }

func CanonicalNonNegative(value string) bool { return canonicalNonNegative(value) }

func CompareCanonicalUint(left, right string) int { return compareCanonicalUint(left, right) }
