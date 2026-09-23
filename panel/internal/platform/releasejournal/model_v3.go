package releasejournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

// Journal v3 is deliberately parallel to the legacy v1/v2 model. The legacy
// parser cannot read these records and ParseV3 cannot read legacy records.
const (
	FormatV3         = ca42protocolv2.ReleaseJournalFormat
	ManifestFormatV3 = ca42protocolv2.ReleaseJournalManifestFormat
	v3ManifestDomain = "PANDORA\x00CA42-RELEASE-JOURNAL-MANIFEST\x00V3\x00"
)

type V3State string

const (
	V3Prepared              V3State = "PREPARED"
	V3IsolationAttempted    V3State = "ISOLATION_ATTEMPTED"
	V3Isolated              V3State = "ISOLATED"
	V3BackupAttempted       V3State = "BACKUP_ATTEMPTED"
	V3BackupVerified        V3State = "BACKUP_VERIFIED"
	V3LayoutSwitchAttempted V3State = "LAYOUT_SWITCH_ATTEMPTED"
	V3LayoutSwitched        V3State = "LAYOUT_SWITCHED"
	V3AdmissionReserved     V3State = "ADMISSION_RESERVED"
	V3AdmissionAttempted    V3State = "ADMISSION_ATTEMPTED"
	V3AdmissionVerified     V3State = "ADMISSION_VERIFIED"
	V3MigrationAttempted    V3State = "MIGRATION_ATTEMPTED"
	V3Migrated              V3State = "MIGRATED"
	V3WritersStartAttempted V3State = "WRITERS_START_ATTEMPTED"
	V3WritersReady          V3State = "WRITERS_READY"
	V3ExposureAttempted     V3State = "EXPOSURE_ATTEMPTED"
	V3Committed             V3State = "COMMITTED"
	V3RecoveryRequired      V3State = "RECOVERY_REQUIRED"
)

var v3NormalOrder = [...]V3State{
	V3Prepared, V3IsolationAttempted, V3Isolated, V3BackupAttempted, V3BackupVerified,
	V3LayoutSwitchAttempted, V3LayoutSwitched, V3AdmissionReserved, V3AdmissionAttempted,
	V3AdmissionVerified, V3MigrationAttempted, V3Migrated, V3WritersStartAttempted,
	V3WritersReady, V3ExposureAttempted, V3Committed,
}

var v3Segments = map[V3State]string{
	V3Prepared: "000.prepared.record", V3IsolationAttempted: "010.isolation_attempted.record",
	V3Isolated: "020.isolated.record", V3BackupAttempted: "030.backup_attempted.record",
	V3BackupVerified: "040.backup_verified.record", V3LayoutSwitchAttempted: "050.layout_switch_attempted.record",
	V3LayoutSwitched: "060.layout_switched.record", V3AdmissionReserved: "070.admission_reserved.record",
	V3AdmissionAttempted: "080.admission_attempted.record", V3AdmissionVerified: "090.admission_verified.record",
	V3MigrationAttempted: "100.migration_attempted.record", V3Migrated: "110.migrated.record",
	V3WritersStartAttempted: "120.writers_start_attempted.record", V3WritersReady: "130.writers_ready.record",
	V3ExposureAttempted: "140.exposure_attempted.record", V3Committed: "150.committed.record",
	V3RecoveryRequired: "900.recovery_required.record",
}

var v3RecoveryReasons = map[string]bool{
	"claim_expired_before_attempt": true, "consumer_outcome_ambiguous": true,
	"cross_store_state_divergent": true, "durability_outcome_ambiguous": true,
	"identity_binding_changed": true, "trusted_clock_rollback": true,
}

var v3TokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type v3PreparedFields struct {
	JournalID, AttemptID, ReleaseContractCoreSHA256, ControllerSHA256 string
	CreatedAtEpoch                                                    int64
}

type v3GenericFields struct {
	JournalID, AttemptID, EventID, PreviousRecordSHA256, EvidenceSHA256 string
	Sequence, EvidenceSize                                              uint64
	OccurredAtEpoch                                                     int64
	PreviousState, State                                                V3State
}

type v3ReservedFields struct {
	JournalID, AttemptID, TransactionID, NonceID                       string
	ProfileSHA256, ClaimSHA256, ClaimCanonicalSHA256                   string
	ArtifactSetBindingSHA256, NonceReservationSHA256                   string
	ReleaseID, ReleaseRunID, Architecture                              string
	BoundaryHeadSHA256, BoundaryManifestSHA256                         string
	LedgerBeforeSHA256, LedgerPlannedSHA256, AuthorityDescriptorSHA256 string
	AuthorityEpoch, AuthoritySequence                                  uint64
	EffectiveNotBeforeEpoch, EffectiveNotAfterEpoch, ReservedAtEpoch   int64
	PreviousRecordSHA256                                               string
}

type v3AttemptedFields struct {
	JournalID, AttemptID, TransactionID, NonceID, NonceReservationSHA256 string
	ConsumerOperationID, ConsumerInputSHA256, InventoryManifestSHA256    string
	AttemptedAtEpoch                                                     int64
	PreviousRecordSHA256                                                 string
}

type v3VerifiedFields struct {
	JournalID, AttemptID, TransactionID, NonceID, NonceReservationSHA256 string
	ConsumerOperationID, ConsumerReceiptSHA256, LedgerAfterSHA256        string
	ConsumerReceiptSize, ConsumerCompletedAtEpoch, VerifiedAtEpoch       int64
	PreviousRecordSHA256                                                 string
}

type v3RecoveryFields struct {
	JournalID, AttemptID, TransactionID, NonceID, ReasonCode string
	OccurredAtEpoch                                          int64
	PreviousState                                            V3State
	PreviousRecordSHA256                                     string
}

type v3Record struct {
	name, kind, sha256   string
	state, previousState V3State
	values               map[string]string
}

// V3Snapshot is forgeable diagnostic evidence only. It grants no mutation,
// recovery, persistence, or admission authority. Future retained sessions must
// use a distinct unexported capability type.
type V3Snapshot struct {
	records                                      map[string][]byte
	names                                        []string
	state                                        V3State
	journalID, attemptID, transactionID, nonceID string
	headSHA256, manifestSHA256                   string
	releaseCoreSHA256, controllerSHA256          string
	parsed                                       bool
}

func (s V3Snapshot) State() V3State                    { return s.state }
func (s V3Snapshot) JournalID() string                 { return s.journalID }
func (s V3Snapshot) AttemptID() string                 { return s.attemptID }
func (s V3Snapshot) TransactionID() string             { return s.transactionID }
func (s V3Snapshot) NonceID() string                   { return s.nonceID }
func (s V3Snapshot) HeadSHA256() string                { return s.headSHA256 }
func (s V3Snapshot) ManifestSHA256() string            { return s.manifestSHA256 }
func (s V3Snapshot) ReleaseContractCoreSHA256() string { return s.releaseCoreSHA256 }
func (s V3Snapshot) ControllerSHA256() string          { return s.controllerSHA256 }
func (s V3Snapshot) Sequence() uint64 {
	if len(s.names) == 0 {
		return 0
	}
	return uint64(len(s.names) - 1)
}

func (s V3Snapshot) VerifiedCopy() (V3Snapshot, error) {
	if !s.parsed || len(s.records) == 0 {
		return V3Snapshot{}, errors.New("release journal v3 snapshot invalid")
	}
	owned := make(map[string][]byte, len(s.records))
	for name, data := range s.records {
		owned[name] = append([]byte(nil), data...)
	}
	copySnapshot, err := ParseV3(owned)
	if err != nil {
		return V3Snapshot{}, err
	}
	if copySnapshot.state != s.state || copySnapshot.journalID != s.journalID || copySnapshot.attemptID != s.attemptID ||
		copySnapshot.transactionID != s.transactionID || copySnapshot.nonceID != s.nonceID ||
		copySnapshot.headSHA256 != s.headSHA256 || copySnapshot.manifestSHA256 != s.manifestSHA256 ||
		copySnapshot.releaseCoreSHA256 != s.releaseCoreSHA256 || copySnapshot.controllerSHA256 != s.controllerSHA256 {
		return V3Snapshot{}, errors.New("release journal v3 snapshot identity changed")
	}
	return copySnapshot, nil
}

func (s V3Snapshot) ManifestBytes() ([]byte, error) {
	verified, err := s.VerifiedCopy()
	if err != nil {
		return nil, err
	}
	return v3ManifestBytes(verified.names, verified.records, verified.headSHA256, verified.state, verified.journalID, verified.attemptID), nil
}

func (s V3Snapshot) LayoutSwitchedBoundary() (V3Snapshot, error) {
	if !s.parsed || len(s.names) < 7 {
		return V3Snapshot{}, errors.New("release journal v3 layout boundary missing")
	}
	prefix := make(map[string][]byte, 7)
	for _, name := range s.names[:7] {
		prefix[name] = append([]byte(nil), s.records[name]...)
	}
	boundary, err := ParseV3(prefix)
	if err != nil || boundary.state != V3LayoutSwitched {
		return V3Snapshot{}, errors.New("release journal v3 layout boundary invalid")
	}
	return boundary, nil
}

func ParseV3Bundle(manifest []byte, records map[string][]byte) (V3Snapshot, error) {
	if len(manifest) == 0 || len(manifest) > maxRecordBytes {
		return V3Snapshot{}, errors.New("release journal v3 manifest envelope invalid")
	}
	snapshot, err := ParseV3(records)
	if err != nil {
		return V3Snapshot{}, err
	}
	expected, err := snapshot.ManifestBytes()
	if err != nil || !bytes.Equal(manifest, expected) {
		return V3Snapshot{}, errors.New("release journal v3 manifest mismatch")
	}
	return snapshot, nil
}

var v3PreparedKeys = [...]string{"format", "record", "journal_namespace", "journal_id", "attempt_id", "sequence",
	"created_at_epoch", "release_contract_core_format", "release_contract_core_sha256", "controller_contract",
	"controller_sha256", "state", "previous_record_sha256", "record_sha256"}

var v3GenericKeys = [...]string{"format", "record", "journal_namespace", "journal_id", "attempt_id", "sequence", "event_id",
	"occurred_at_epoch", "previous_state", "state", "previous_record_sha256", "evidence_sha256", "evidence_size",
	"reason_code", "record_sha256"}

var v3ReservedKeys = [...]string{"format", "record", "journal_namespace", "journal_id", "attempt_id", "sequence",
	"previous_state", "state", "transaction_id", "nonce_id", "profile_id", "profile_sha256", "claim_format", "claim_sha256",
	"claim_canonical_sha256", "artifact_set_format", "artifact_set_binding_sha256", "release_id", "release_run_id", "architecture",
	"boundary_head_sha256", "boundary_manifest_sha256", "nonce_reservation_sha256", "ledger_before_sha256", "ledger_planned_sha256",
	"authority_descriptor_sha256", "authority_epoch", "authority_sequence", "effective_not_before_epoch", "effective_not_after_epoch",
	"reserved_at_epoch", "previous_record_sha256", "record_sha256"}

var v3AttemptedKeys = [...]string{"format", "record", "journal_namespace", "journal_id", "attempt_id", "sequence",
	"previous_state", "state", "transaction_id", "nonce_id", "nonce_reservation_sha256", "consumer_operation_id",
	"consumer_input_sha256", "inventory_manifest_sha256", "attempted_at_epoch", "previous_record_sha256", "record_sha256"}

var v3VerifiedKeys = [...]string{"format", "record", "journal_namespace", "journal_id", "attempt_id", "sequence",
	"previous_state", "state", "transaction_id", "nonce_id", "nonce_reservation_sha256", "consumer_operation_id",
	"consumer_receipt_sha256", "consumer_receipt_size", "consumer_completed_at_epoch", "ledger_after_sha256", "verified_at_epoch",
	"previous_record_sha256", "record_sha256"}

var v3RecoveryKeys = [...]string{"format", "record", "journal_namespace", "journal_id", "attempt_id", "sequence",
	"previous_state", "state", "transaction_id", "nonce_id", "reason_code", "occurred_at_epoch", "previous_record_sha256", "record_sha256"}

func v3PreparedRecordBytes(f v3PreparedFields) ([]byte, string, error) {
	if !v3SHA(f.JournalID, false) || !v3TokenRE.MatchString(f.AttemptID) || !v3AllSHA(false, f.ReleaseContractCoreSHA256, f.ControllerSHA256) || f.CreatedAtEpoch <= 0 {
		return nil, "", errors.New("release journal v3 prepared fields invalid")
	}
	return v3Build("prepared", v3PreparedKeys[:], []string{FormatV3, "prepared", ca42protocolv2.ReleaseJournalNamespace,
		f.JournalID, f.AttemptID, "0", strconv.FormatInt(f.CreatedAtEpoch, 10), ca42protocolv2.ReleaseContractCoreFormat,
		f.ReleaseContractCoreSHA256, ca42protocolv2.ControllerContract, f.ControllerSHA256, string(V3Prepared), strings.Repeat("0", 64)})
}

func v3GenericRecordBytes(f v3GenericFields) ([]byte, string, error) {
	if !v3SHA(f.JournalID, false) || !v3TokenRE.MatchString(f.AttemptID) || !v3AllSHA(false, f.EventID, f.PreviousRecordSHA256, f.EvidenceSHA256) ||
		f.Sequence == 0 || f.OccurredAtEpoch <= 0 || f.EvidenceSize == 0 || f.EvidenceSize > maxRecordBytes || !v3GenericEdge(f.PreviousState, f.State, f.Sequence) {
		return nil, "", errors.New("release journal v3 transition fields invalid")
	}
	return v3Build("transition", v3GenericKeys[:], []string{FormatV3, "transition", ca42protocolv2.ReleaseJournalNamespace,
		f.JournalID, f.AttemptID, strconv.FormatUint(f.Sequence, 10), f.EventID, strconv.FormatInt(f.OccurredAtEpoch, 10),
		string(f.PreviousState), string(f.State), f.PreviousRecordSHA256, f.EvidenceSHA256, strconv.FormatUint(f.EvidenceSize, 10), "none"})
}

func v3ReservedRecordBytes(f v3ReservedFields) ([]byte, string, error) {
	if !v3BaseAdmission(f.JournalID, f.AttemptID, f.TransactionID, f.NonceID, f.PreviousRecordSHA256) ||
		f.ProfileSHA256 != ca42executionv2.RequiredProfileSHA256 ||
		!v3AllSHA(false, f.ProfileSHA256, f.ClaimSHA256, f.ClaimCanonicalSHA256, f.ArtifactSetBindingSHA256,
			f.BoundaryHeadSHA256, f.BoundaryManifestSHA256, f.NonceReservationSHA256, f.LedgerPlannedSHA256, f.AuthorityDescriptorSHA256) ||
		!v3SHA(f.LedgerBeforeSHA256, true) || !v3TokenRE.MatchString(f.ReleaseID) || !v3TokenRE.MatchString(f.ReleaseRunID) ||
		(f.Architecture != "amd64" && f.Architecture != "arm64") || f.AuthorityEpoch == 0 || f.AuthoritySequence == 0 ||
		(isZeroHex64(f.LedgerBeforeSHA256) != (f.AuthorityEpoch == 1 && f.AuthoritySequence == 1)) ||
		f.EffectiveNotBeforeEpoch <= 0 || f.EffectiveNotAfterEpoch <= f.EffectiveNotBeforeEpoch ||
		f.ReservedAtEpoch < f.EffectiveNotBeforeEpoch || f.ReservedAtEpoch >= f.EffectiveNotAfterEpoch {
		return nil, "", errors.New("release journal v3 reservation fields invalid")
	}
	return v3Build("admission-reserved", v3ReservedKeys[:], []string{FormatV3, "admission_reserved", ca42protocolv2.ReleaseJournalNamespace,
		f.JournalID, f.AttemptID, "7", string(V3LayoutSwitched), string(V3AdmissionReserved), f.TransactionID, f.NonceID,
		ca42protocolv2.ProfileID, f.ProfileSHA256, ca42artifactsv2.ConsumptionClaimFormat, f.ClaimSHA256, f.ClaimCanonicalSHA256,
		ca42artifactsv2.Format, f.ArtifactSetBindingSHA256, f.ReleaseID, f.ReleaseRunID, f.Architecture, f.BoundaryHeadSHA256,
		f.BoundaryManifestSHA256, f.NonceReservationSHA256, f.LedgerBeforeSHA256, f.LedgerPlannedSHA256,
		f.AuthorityDescriptorSHA256, strconv.FormatUint(f.AuthorityEpoch, 10), strconv.FormatUint(f.AuthoritySequence, 10),
		strconv.FormatInt(f.EffectiveNotBeforeEpoch, 10), strconv.FormatInt(f.EffectiveNotAfterEpoch, 10), strconv.FormatInt(f.ReservedAtEpoch, 10), f.PreviousRecordSHA256})
}

func v3AttemptedRecordBytes(f v3AttemptedFields) ([]byte, string, error) {
	if !v3BaseAdmission(f.JournalID, f.AttemptID, f.TransactionID, f.NonceID, f.PreviousRecordSHA256) ||
		!v3AllSHA(false, f.NonceReservationSHA256, f.ConsumerOperationID, f.ConsumerInputSHA256, f.InventoryManifestSHA256) || f.AttemptedAtEpoch <= 0 {
		return nil, "", errors.New("release journal v3 attempt fields invalid")
	}
	return v3Build("admission-attempted", v3AttemptedKeys[:], []string{FormatV3, "admission_attempted", ca42protocolv2.ReleaseJournalNamespace,
		f.JournalID, f.AttemptID, "8", string(V3AdmissionReserved), string(V3AdmissionAttempted), f.TransactionID, f.NonceID,
		f.NonceReservationSHA256, f.ConsumerOperationID, f.ConsumerInputSHA256, f.InventoryManifestSHA256,
		strconv.FormatInt(f.AttemptedAtEpoch, 10), f.PreviousRecordSHA256})
}

func v3VerifiedRecordBytes(f v3VerifiedFields) ([]byte, string, error) {
	if !v3BaseAdmission(f.JournalID, f.AttemptID, f.TransactionID, f.NonceID, f.PreviousRecordSHA256) ||
		!v3AllSHA(false, f.NonceReservationSHA256, f.ConsumerOperationID, f.ConsumerReceiptSHA256, f.LedgerAfterSHA256) ||
		f.ConsumerReceiptSize <= 0 || f.ConsumerReceiptSize > maxRecordBytes || f.ConsumerCompletedAtEpoch <= 0 || f.VerifiedAtEpoch < f.ConsumerCompletedAtEpoch {
		return nil, "", errors.New("release journal v3 verification fields invalid")
	}
	return v3Build("admission-verified", v3VerifiedKeys[:], []string{FormatV3, "admission_verified", ca42protocolv2.ReleaseJournalNamespace,
		f.JournalID, f.AttemptID, "9", string(V3AdmissionAttempted), string(V3AdmissionVerified), f.TransactionID, f.NonceID,
		f.NonceReservationSHA256, f.ConsumerOperationID, f.ConsumerReceiptSHA256, strconv.FormatInt(f.ConsumerReceiptSize, 10),
		strconv.FormatInt(f.ConsumerCompletedAtEpoch, 10), f.LedgerAfterSHA256, strconv.FormatInt(f.VerifiedAtEpoch, 10), f.PreviousRecordSHA256})
}

func v3RecoveryRecordBytes(f v3RecoveryFields, sequence uint64) ([]byte, string, error) {
	zero := strings.Repeat("0", 64)
	if !v3SHA(f.JournalID, false) || !v3TokenRE.MatchString(f.AttemptID) || !v3SHA(f.PreviousRecordSHA256, false) ||
		!v3RecoveryReasonAllowed(f.PreviousState, f.ReasonCode) || f.OccurredAtEpoch <= 0 || f.PreviousState == V3Committed || f.PreviousState == V3RecoveryRequired ||
		sequence != uint64(v3StateSequence(f.PreviousState)+1) || sequence == 0 || sequence > 15 || (v3StateSequence(f.PreviousState) < 7 && (f.TransactionID != zero || f.NonceID != zero)) ||
		(v3StateSequence(f.PreviousState) >= 7 && !v3AllSHA(false, f.TransactionID, f.NonceID)) {
		return nil, "", errors.New("release journal v3 recovery fields invalid")
	}
	return v3Build("recovery-required", v3RecoveryKeys[:], []string{FormatV3, "recovery_required", ca42protocolv2.ReleaseJournalNamespace,
		f.JournalID, f.AttemptID, strconv.FormatUint(sequence, 10), string(f.PreviousState), string(V3RecoveryRequired),
		f.TransactionID, f.NonceID, f.ReasonCode, strconv.FormatInt(f.OccurredAtEpoch, 10), f.PreviousRecordSHA256})
}

func v3Build(kind string, keys, values []string) ([]byte, string, error) {
	if len(keys) != len(values)+1 || keys[len(keys)-1] != "record_sha256" {
		return nil, "", errors.New("release journal v3 schema invalid")
	}
	var body strings.Builder
	for i, value := range values {
		if value == "" || strings.ContainsAny(value, "=\r\n\x00") || !utf8.ValidString(value) {
			return nil, "", errors.New("release journal v3 value invalid")
		}
		body.WriteString(keys[i])
		body.WriteByte('=')
		body.WriteString(value)
		body.WriteByte('\n')
	}
	if body.Len() > maxRecordBytes {
		return nil, "", errors.New("release journal v3 record too large")
	}
	digest := v3RecordSHA(kind, []byte(body.String()))
	return append([]byte(body.String()), []byte("record_sha256="+digest+"\n")...), digest, nil
}

func v3RecordSHA(kind string, body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("PANDORA\x00CA42-RELEASE-JOURNAL-" + strings.ToUpper(kind) + "\x00V3\x00"))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func v3SHA(value string, zeroAllowed bool) bool {
	return hex64RE.MatchString(value) && (zeroAllowed || !isZeroHex64(value))
}
func v3AllSHA(zeroAllowed bool, values ...string) bool {
	for _, value := range values {
		if !v3SHA(value, zeroAllowed) {
			return false
		}
	}
	return true
}
func v3BaseAdmission(journal, attempt, tx, nonce, previous string) bool {
	return v3SHA(journal, false) && v3TokenRE.MatchString(attempt) && v3AllSHA(false, tx, nonce, previous)
}

func v3RecoveryReasonAllowed(previous V3State, reason string) bool {
	if !v3RecoveryReasons[reason] {
		return false
	}
	switch reason {
	case "claim_expired_before_attempt":
		return previous == V3AdmissionReserved
	case "consumer_outcome_ambiguous":
		return previous == V3AdmissionAttempted
	case "cross_store_state_divergent":
		return v3StateSequence(previous) >= 7
	default:
		return v3StateSequence(previous) >= 0 && previous != V3Committed
	}
}

func v3GenericEdge(previous, next V3State, sequence uint64) bool {
	if sequence >= uint64(len(v3NormalOrder)) {
		return false
	}
	if next == V3AdmissionReserved || next == V3AdmissionAttempted || next == V3AdmissionVerified {
		return false
	}
	return v3NormalOrder[sequence-1] == previous && v3NormalOrder[sequence] == next
}

func ParseV3(records map[string][]byte) (V3Snapshot, error) {
	if len(records) == 0 || len(records) > len(v3NormalOrder)+1 {
		return V3Snapshot{}, errors.New("release journal v3 inventory invalid")
	}
	ownedInput := make(map[string][]byte, len(records))
	for name, data := range records {
		ownedInput[name] = append([]byte(nil), data...)
	}
	records = ownedInput
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	if names[0] != v3Segments[V3Prepared] {
		return V3Snapshot{}, errors.New("release journal v3 prepared missing")
	}
	prepared, err := v3ParseRecord(names[0], records[names[0]], v3PreparedKeys[:], "prepared")
	if err != nil || prepared.state != V3Prepared {
		return V3Snapshot{}, errors.New("release journal v3 prepared invalid")
	}
	if err := v3ValidatePrepared(prepared.values); err != nil {
		return V3Snapshot{}, err
	}
	state, head := V3Prepared, prepared.sha256
	lastAt, _ := v3Positive(prepared.values["created_at_epoch"])
	txID, nonceID, reservedAt, notAfter := "", "", int64(0), int64(0)
	reservedValues, attemptedValues := map[string]string(nil), map[string]string(nil)
	seenEvents := map[string]bool{}
	ordered := []string{names[0]}
	for index, name := range names[1:] {
		if state == V3Committed || state == V3RecoveryRequired {
			return V3Snapshot{}, errors.New("release journal v3 bytes after terminal")
		}
		sequence := uint64(index + 1)
		keys, kind := v3GenericKeys[:], "transition"
		switch name {
		case v3Segments[V3AdmissionReserved]:
			keys, kind = v3ReservedKeys[:], "admission-reserved"
		case v3Segments[V3AdmissionAttempted]:
			keys, kind = v3AttemptedKeys[:], "admission-attempted"
		case v3Segments[V3AdmissionVerified]:
			keys, kind = v3VerifiedKeys[:], "admission-verified"
		case v3Segments[V3RecoveryRequired]:
			keys, kind = v3RecoveryKeys[:], "recovery-required"
		}
		record, parseErr := v3ParseRecord(name, records[name], keys, kind)
		if parseErr != nil || record.values["journal_id"] != prepared.values["journal_id"] || record.values["attempt_id"] != prepared.values["attempt_id"] ||
			record.values["sequence"] != strconv.FormatUint(sequence, 10) || record.previousState != state || record.values["previous_record_sha256"] != head || v3Segments[record.state] != name {
			return V3Snapshot{}, errors.New("release journal v3 transition binding invalid")
		}
		occurredKey := "occurred_at_epoch"
		if record.state == V3AdmissionReserved {
			occurredKey = "reserved_at_epoch"
		}
		if record.state == V3AdmissionAttempted {
			occurredKey = "attempted_at_epoch"
		}
		if record.state == V3AdmissionVerified {
			occurredKey = "verified_at_epoch"
		}
		at, timeErr := v3Positive(record.values[occurredKey])
		if timeErr != nil || at < lastAt {
			return V3Snapshot{}, errors.New("release journal v3 time rollback")
		}
		if record.state == V3AdmissionReserved && record.values["boundary_manifest_sha256"] !=
			v3ManifestSHA(ordered, records, head, state, prepared.values["journal_id"], prepared.values["attempt_id"]) {
			return V3Snapshot{}, errors.New("release journal v3 reservation boundary manifest invalid")
		}
		if validationErr := v3ValidateReplay(record, sequence, prepared.values, reservedValues, attemptedValues, reservedAt, notAfter); validationErr != nil {
			return V3Snapshot{}, validationErr
		}
		if record.kind == "transition" {
			eventID := record.values["event_id"]
			if seenEvents[eventID] {
				return V3Snapshot{}, errors.New("release journal v3 event reused")
			}
			seenEvents[eventID] = true
		}
		if record.state == V3AdmissionReserved {
			txID, nonceID = record.values["transaction_id"], record.values["nonce_id"]
			reservedValues = record.values
			reservedAt, _ = v3Positive(record.values["reserved_at_epoch"])
			notAfter, _ = v3Positive(record.values["effective_not_after_epoch"])
		}
		if (record.state == V3AdmissionAttempted || record.state == V3AdmissionVerified) &&
			(record.values["transaction_id"] != txID || record.values["nonce_id"] != nonceID) {
			return V3Snapshot{}, errors.New("release journal v3 transaction changed")
		}
		if record.state == V3AdmissionAttempted {
			attemptedValues = record.values
		}
		state, head, lastAt = record.state, record.sha256, at
		ordered = append(ordered, name)
	}
	if len(ordered) != len(records) {
		return V3Snapshot{}, errors.New("release journal v3 unknown record")
	}
	owned := make(map[string][]byte, len(records))
	for name, data := range records {
		owned[name] = append([]byte(nil), data...)
	}
	return V3Snapshot{records: owned, names: append([]string(nil), ordered...), state: state, journalID: prepared.values["journal_id"],
		attemptID: prepared.values["attempt_id"], transactionID: txID, nonceID: nonceID, headSHA256: head,
		releaseCoreSHA256: prepared.values["release_contract_core_sha256"], controllerSHA256: prepared.values["controller_sha256"],
		manifestSHA256: v3ManifestSHA(ordered, owned, head, state, prepared.values["journal_id"], prepared.values["attempt_id"]), parsed: true}, nil
}

func v3ParseRecord(name string, data []byte, keys []string, kind string) (v3Record, error) {
	if len(data) == 0 || len(data) > maxRecordBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 || bytes.Contains(data, []byte("\r")) || data[len(data)-1] != '\n' {
		return v3Record{}, errors.New("release journal v3 envelope invalid")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(keys) {
		return v3Record{}, errors.New("release journal v3 field count invalid")
	}
	values := make(map[string]string, len(keys))
	var body strings.Builder
	for i, key := range keys {
		if len(lines[i]) == 0 || len(lines[i]) > maxRecordLineBytes || !strings.HasPrefix(lines[i], key+"=") {
			return v3Record{}, errors.New("release journal v3 key order invalid")
		}
		value := strings.TrimPrefix(lines[i], key+"=")
		if value == "" || strings.Contains(value, "=") {
			return v3Record{}, errors.New("release journal v3 value invalid")
		}
		values[key] = value
		if key != "record_sha256" {
			body.WriteString(lines[i])
			body.WriteByte('\n')
		}
	}
	if values["format"] != FormatV3 || values["journal_namespace"] != ca42protocolv2.ReleaseJournalNamespace || !v3SHA(values["record_sha256"], false) || v3RecordSHA(kind, []byte(body.String())) != values["record_sha256"] {
		return v3Record{}, errors.New("release journal v3 canonical hash invalid")
	}
	return v3Record{name: name, kind: kind, state: V3State(values["state"]), previousState: V3State(values["previous_state"]), values: values, sha256: values["record_sha256"]}, nil
}

func v3ValidatePrepared(v map[string]string) error {
	if v["record"] != "prepared" || v["sequence"] != "0" || v["release_contract_core_format"] != ca42protocolv2.ReleaseContractCoreFormat || v["controller_contract"] != ca42protocolv2.ControllerContract || v["state"] != string(V3Prepared) || v["previous_record_sha256"] != strings.Repeat("0", 64) || !v3SHA(v["journal_id"], false) || !v3TokenRE.MatchString(v["attempt_id"]) || !v3AllSHA(false, v["release_contract_core_sha256"], v["controller_sha256"]) {
		return errors.New("release journal v3 prepared identity invalid")
	}
	if _, err := v3Positive(v["created_at_epoch"]); err != nil {
		return errors.New("release journal v3 prepared time invalid")
	}
	return nil
}

func v3ValidateReplay(r v3Record, sequence uint64, prepared, reserved, attempted map[string]string, reservedAt, notAfter int64) error {
	wantRecord := map[V3State]string{
		V3AdmissionReserved: "admission_reserved", V3AdmissionAttempted: "admission_attempted",
		V3AdmissionVerified: "admission_verified", V3RecoveryRequired: "recovery_required",
	}
	if expected, specialized := wantRecord[r.state]; specialized && r.values["record"] != expected {
		return errors.New("release journal v3 record kind invalid")
	}
	switch r.state {
	case V3AdmissionReserved:
		if sequence != 7 || !v3AllSHA(false, r.values["transaction_id"], r.values["nonce_id"], r.values["profile_sha256"], r.values["claim_sha256"], r.values["claim_canonical_sha256"], r.values["artifact_set_binding_sha256"], r.values["boundary_head_sha256"], r.values["boundary_manifest_sha256"], r.values["nonce_reservation_sha256"], r.values["ledger_planned_sha256"], r.values["authority_descriptor_sha256"]) || !v3SHA(r.values["ledger_before_sha256"], true) || r.values["profile_id"] != ca42protocolv2.ProfileID || r.values["profile_sha256"] != ca42executionv2.RequiredProfileSHA256 || r.values["claim_format"] != ca42artifactsv2.ConsumptionClaimFormat || r.values["artifact_set_format"] != ca42artifactsv2.Format || r.values["boundary_head_sha256"] != r.values["previous_record_sha256"] {
			return errors.New("release journal v3 reservation identity invalid")
		}
		if !v3TokenRE.MatchString(r.values["release_id"]) || !v3TokenRE.MatchString(r.values["release_run_id"]) ||
			(r.values["architecture"] != "amd64" && r.values["architecture"] != "arm64") {
			return errors.New("release journal v3 reservation release identity invalid")
		}
		epoch, e1 := v3PositiveUint(r.values["authority_epoch"])
		authoritySequence, e2 := v3PositiveUint(r.values["authority_sequence"])
		nbf, e3 := v3Positive(r.values["effective_not_before_epoch"])
		naf, e4 := v3Positive(r.values["effective_not_after_epoch"])
		at, e5 := v3Positive(r.values["reserved_at_epoch"])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || (isZeroHex64(r.values["ledger_before_sha256"]) != (epoch == 1 && authoritySequence == 1)) || naf <= nbf || at < nbf || at >= naf {
			return errors.New("release journal v3 reservation chronology invalid")
		}
	case V3AdmissionAttempted:
		at, err := v3Positive(r.values["attempted_at_epoch"])
		if sequence != 8 || reserved == nil || err != nil || at < reservedAt || at >= notAfter || !v3AllSHA(false, r.values["nonce_reservation_sha256"], r.values["consumer_operation_id"], r.values["consumer_input_sha256"], r.values["inventory_manifest_sha256"]) || r.values["nonce_reservation_sha256"] != reserved["nonce_reservation_sha256"] {
			return errors.New("release journal v3 attempt invalid")
		}
	case V3AdmissionVerified:
		completed, e1 := v3Positive(r.values["consumer_completed_at_epoch"])
		verified, e2 := v3Positive(r.values["verified_at_epoch"])
		size, e3 := v3Positive(r.values["consumer_receipt_size"])
		attemptedAt := int64(0)
		if attempted != nil {
			attemptedAt, _ = v3Positive(attempted["attempted_at_epoch"])
		}
		if sequence != 9 || reserved == nil || attempted == nil || e1 != nil || e2 != nil || e3 != nil || size > maxRecordBytes || completed < attemptedAt || completed >= notAfter || verified < completed || !v3AllSHA(false, r.values["nonce_reservation_sha256"], r.values["consumer_operation_id"], r.values["consumer_receipt_sha256"], r.values["ledger_after_sha256"]) || r.values["nonce_reservation_sha256"] != reserved["nonce_reservation_sha256"] || r.values["consumer_operation_id"] != attempted["consumer_operation_id"] || r.values["ledger_after_sha256"] != reserved["ledger_planned_sha256"] {
			return errors.New("release journal v3 verification invalid")
		}
	case V3RecoveryRequired:
		if !v3RecoveryReasonAllowed(r.previousState, r.values["reason_code"]) {
			return errors.New("release journal v3 recovery reason invalid")
		}
		at, err := v3Positive(r.values["occurred_at_epoch"])
		if err != nil {
			return errors.New("release journal v3 recovery time invalid")
		}
		zero := strings.Repeat("0", 64)
		if v3StateSequence(r.previousState) < 7 {
			if r.values["transaction_id"] != zero || r.values["nonce_id"] != zero {
				return errors.New("release journal v3 pre-admission recovery identity invalid")
			}
		} else if reserved == nil || r.values["transaction_id"] != reserved["transaction_id"] || r.values["nonce_id"] != reserved["nonce_id"] {
			return errors.New("release journal v3 recovery transaction invalid")
		}
		if r.values["reason_code"] == "claim_expired_before_attempt" && (r.previousState != V3AdmissionReserved || at < notAfter) {
			return errors.New("release journal v3 expiry recovery invalid")
		}
	default:
		if !v3GenericEdge(r.previousState, r.state, sequence) || r.values["record"] != "transition" || r.values["reason_code"] != "none" || !v3AllSHA(false, r.values["event_id"], r.values["evidence_sha256"]) {
			return errors.New("release journal v3 generic transition invalid")
		}
		size, e1 := v3Positive(r.values["evidence_size"])
		if e1 != nil || size > maxRecordBytes {
			return errors.New("release journal v3 evidence size invalid")
		}
	}
	return nil
}

func v3Positive(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("not canonical positive")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("not canonical positive")
	}
	return parsed, nil
}

func v3PositiveUint(value string) (uint64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("not canonical positive")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("not canonical positive")
	}
	return parsed, nil
}

func v3ManifestBytes(names []string, records map[string][]byte, head string, state V3State, journalID, attemptID string) []byte {
	var body strings.Builder
	body.WriteString("format=" + ManifestFormatV3 + "\n")
	body.WriteString("journal_format=" + FormatV3 + "\n")
	body.WriteString("journal_namespace=" + ca42protocolv2.ReleaseJournalNamespace + "\n")
	body.WriteString("journal_id=" + journalID + "\n")
	body.WriteString("attempt_id=" + attemptID + "\n")
	body.WriteString("record_count=" + strconv.Itoa(len(names)) + "\n")
	body.WriteString("head_sequence=" + strconv.Itoa(len(names)-1) + "\n")
	body.WriteString("head_state=" + string(state) + "\n")
	body.WriteString("head_record_sha256=" + head + "\n")
	for _, name := range names {
		body.WriteString("segment=" + name + "\n")
		body.WriteString("segment_sha256=" + sha256Bytes(records[name]) + "\n")
	}
	return []byte(body.String())
}

func v3ManifestSHA(names []string, records map[string][]byte, head string, state V3State, journalID, attemptID string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(v3ManifestDomain))
	_, _ = h.Write(v3ManifestBytes(names, records, head, state, journalID, attemptID))
	return hex.EncodeToString(h.Sum(nil))
}

func v3StateSequence(state V3State) int {
	for index, candidate := range v3NormalOrder {
		if candidate == state {
			return index
		}
	}
	return -1
}

func V3StateSegments() map[V3State]string {
	result := make(map[V3State]string, len(v3Segments))
	for state, name := range v3Segments {
		result[state] = name
	}
	return result
}
func V3RecoveryReasons() []string {
	return []string{"claim_expired_before_attempt", "consumer_outcome_ambiguous", "cross_store_state_divergent", "durability_outcome_ambiguous", "identity_binding_changed", "trusted_clock_rollback"}
}
