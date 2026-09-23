package ca42nonce

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
)

const (
	Format                  = "pandora-ca42-global-nonce-v1"
	ReservedRecordName      = "000.reserved.record"
	CommittedRecordName     = "010.committed.record"
	RecoveryRecordName      = "900.recovery_required.record"
	MaxRecordBytes          = 64 << 10
	MaxConsumerReceiptBytes = 1 << 20
	recordDomain            = "PANDORA\x00CA42-GLOBAL-NONCE-RECORD\x00V1\x00"
)

type State string

const (
	Reserved         State = "RESERVED"
	Committed        State = "COMMITTED"
	RecoveryRequired State = "RECOVERY_REQUIRED"
)

var (
	hex64RE                = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tokenRE                = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	allowedRecoveryReasons = map[string]bool{
		"claim_expired_before_attempt": true,
		"consumer_outcome_ambiguous":   true,
		"cross_store_state_divergent":  true,
		"durability_outcome_ambiguous": true,
		"trusted_clock_rollback":       true,
	}
)

// ErrReservationIncomplete means the nonce directory was durably allocated
// but no canonical reservation record exists. The allocation is fsynced before
// the store reports the directory-created crash boundary. The nonce remains
// fenced: ordinary reserve/open calls may neither fill, remove nor replace the
// directory. Only the future cross-store recovery coordinator may interpret
// retained journal evidence and move the attempt to a monotonic recovery state.
var ErrReservationIncomplete = errors.New("CA42 nonce reservation incomplete and permanently fenced")

type reservationFields struct {
	// These scalars are deliberately package-private. The future retained store
	// must derive them from a verified ConsumptionClaim plus retained Journal v3
	// and authority-ledger capabilities; it must never expose a scalar builder.
	NonceID, TransactionID, ClaimFormat, ClaimSHA256, ClaimCanonicalSHA256 string
	ArtifactSetFormat, ArtifactSetBindingSHA256, ProfileSHA256             string
	ReleaseID, ReleaseRunID, AttemptID, Architecture                       string
	JournalID, JournalBoundaryHeadSHA256, JournalBoundaryManifestSHA256    string
	LedgerBeforeSHA256, LedgerPlannedSHA256                                string
	AuthorityDescriptorSHA256                                              string
	AuthorityEpoch, AuthoritySequence                                      uint64
	EffectiveNotBeforeEpoch, EffectiveNotAfterEpoch, ReservedAtEpoch       int64
}

type committedFields struct {
	NonceID, TransactionID, ReservationSHA256                       string
	JournalVerifiedHeadSHA256, JournalVerifiedManifestSHA256        string
	LedgerAfterSHA256, ConsumerOperationID, ConsumerReceiptSHA256   string
	ConsumerReceiptSize, ConsumerCompletedAtEpoch, CommittedAtEpoch int64
}

type recoveryFields struct {
	NonceID, TransactionID, ReservationSHA256 string
	ReasonCode                                string
	OccurredAtEpoch                           int64
}

type record struct {
	name, kind, canonical, sha256 string
	values                        map[string]string
}

// Snapshot is a forgeable diagnostic replay of canonical phase records. It
// grants no execution, mutation, persistence, recovery or admission authority.
// Future stores must return a distinct retained capability and must never
// accept Snapshot as authority.
type Snapshot struct {
	records map[string][]byte
	state   State
	nonceID string
	txID    string
	headSHA string
	parsed  bool
}

func (r Snapshot) State() State          { return r.state }
func (r Snapshot) NonceID() string       { return r.nonceID }
func (r Snapshot) TransactionID() string { return r.txID }
func (r Snapshot) HeadSHA256() string    { return r.headSHA }

func (r Snapshot) VerifiedCopy() (Snapshot, error) {
	if !r.parsed || len(r.records) == 0 {
		return Snapshot{}, errors.New("CA42 nonce snapshot invalid")
	}
	copyRecords := make(map[string][]byte, len(r.records))
	for name, data := range r.records {
		copyRecords[name] = append([]byte(nil), data...)
	}
	verified, err := Parse(copyRecords)
	if err != nil {
		return Snapshot{}, err
	}
	if verified.state != r.state || verified.nonceID != r.nonceID || verified.txID != r.txID || verified.headSHA != r.headSHA {
		return Snapshot{}, errors.New("CA42 nonce snapshot identity changed")
	}
	return verified, nil
}

// Parse accepts exactly one RESERVED record and at most one terminal record.
// A reservation is never removed or interpreted as reusable.
func Parse(records map[string][]byte) (Snapshot, error) {
	if len(records) < 1 || len(records) > 2 {
		return Snapshot{}, errors.New("CA42 nonce record inventory invalid")
	}
	reservedData, ok := records[ReservedRecordName]
	if !ok {
		return Snapshot{}, errors.New("CA42 nonce reservation missing")
	}
	reserved, err := parseRecord(ReservedRecordName, reservedData, reservedFieldNames[:])
	if err != nil || reserved.kind != "reserved" || reserved.values["previous_record_sha256"] != strings.Repeat("0", 64) {
		return Snapshot{}, errors.New("CA42 nonce reservation invalid")
	}
	if err := validateReservationRecord(reserved.values); err != nil {
		return Snapshot{}, err
	}
	state, head := Reserved, reserved.sha256
	terminalName := ""
	if _, ok := records[CommittedRecordName]; ok {
		terminalName, state = CommittedRecordName, Committed
	}
	if _, ok := records[RecoveryRecordName]; ok {
		if terminalName != "" {
			return Snapshot{}, errors.New("CA42 nonce terminal fork invalid")
		}
		terminalName, state = RecoveryRecordName, RecoveryRequired
	}
	if len(records) == 2 && terminalName == "" {
		return Snapshot{}, errors.New("CA42 nonce record name invalid")
	}
	if terminalName != "" {
		fields := committedFieldNames[:]
		wantKind := "committed"
		if state == RecoveryRequired {
			fields, wantKind = recoveryFieldNames[:], "recovery_required"
		}
		terminal, terminalErr := parseRecord(terminalName, records[terminalName], fields)
		if terminalErr != nil || terminal.kind != wantKind ||
			terminal.values["nonce_id"] != reserved.values["nonce_id"] ||
			terminal.values["transaction_id"] != reserved.values["transaction_id"] ||
			terminal.values["reservation_sha256"] != reserved.sha256 ||
			terminal.values["previous_record_sha256"] != reserved.sha256 {
			return Snapshot{}, errors.New("CA42 nonce terminal binding invalid")
		}
		if state == Committed {
			if err := validateCommittedRecord(terminal.values); err != nil {
				return Snapshot{}, err
			}
		} else if err := validateRecoveryRecord(terminal.values); err != nil {
			return Snapshot{}, err
		}
		if state == Committed && terminal.values["ledger_after_sha256"] != reserved.values["ledger_planned_sha256"] {
			return Snapshot{}, errors.New("CA42 nonce committed ledger mismatch")
		}
		reservedAt, _ := canonicalPositiveInt64(reserved.values["reserved_at_epoch"])
		notAfter, _ := canonicalPositiveInt64(reserved.values["effective_not_after_epoch"])
		if state == Committed {
			completedAt, _ := canonicalPositiveInt64(terminal.values["consumer_completed_at_epoch"])
			committedAt, _ := canonicalPositiveInt64(terminal.values["committed_at_epoch"])
			if completedAt < reservedAt || completedAt >= notAfter || committedAt < completedAt {
				return Snapshot{}, errors.New("CA42 nonce committed time binding invalid")
			}
		} else {
			occurredAt, _ := canonicalPositiveInt64(terminal.values["occurred_at_epoch"])
			if occurredAt < reservedAt {
				return Snapshot{}, errors.New("CA42 nonce recovery time binding invalid")
			}
			if terminal.values["reason_code"] == "claim_expired_before_attempt" && occurredAt < notAfter {
				return Snapshot{}, errors.New("CA42 nonce expiry recovery reason invalid")
			}
		}
		head = terminal.sha256
	}
	owned := make(map[string][]byte, len(records))
	for name, data := range records {
		owned[name] = append([]byte(nil), data...)
	}
	return Snapshot{records: owned, state: state, nonceID: reserved.values["nonce_id"],
		txID: reserved.values["transaction_id"], headSHA: head, parsed: true}, nil
}

var reservedFieldNames = [...]string{
	"format", "record", "nonce_id", "transaction_id", "claim_format", "claim_sha256", "claim_canonical_sha256",
	"artifact_set_format", "artifact_set_binding_sha256", "profile_sha256", "release_id", "release_run_id", "attempt_id", "architecture",
	"journal_id", "journal_boundary_head_sha256", "journal_boundary_manifest_sha256", "ledger_before_sha256", "ledger_planned_sha256",
	"authority_descriptor_sha256", "authority_epoch", "authority_sequence",
	"effective_not_before_epoch", "effective_not_after_epoch", "reserved_at_epoch", "previous_record_sha256", "record_sha256",
}

var committedFieldNames = [...]string{
	"format", "record", "nonce_id", "transaction_id", "reservation_sha256", "journal_verified_head_sha256",
	"journal_verified_manifest_sha256", "ledger_after_sha256", "consumer_operation_id", "consumer_receipt_sha256",
	"consumer_receipt_size", "consumer_completed_at_epoch", "committed_at_epoch", "previous_record_sha256", "record_sha256",
}

var recoveryFieldNames = [...]string{
	"format", "record", "nonce_id", "transaction_id", "reservation_sha256", "reason_code", "occurred_at_epoch",
	"previous_record_sha256", "record_sha256",
}

func reservationRecordBytes(f reservationFields) ([]byte, string, error) {
	if !validSHA(f.NonceID, false) || !validSHA(f.TransactionID, false) || f.ClaimFormat != ca42artifactsv2.ConsumptionClaimFormat ||
		!validSHA(f.ClaimSHA256, false) || !validSHA(f.ClaimCanonicalSHA256, false) || f.ArtifactSetFormat != ca42artifactsv2.Format ||
		!validSHA(f.ArtifactSetBindingSHA256, false) || !validSHA(f.ProfileSHA256, false) ||
		!tokenRE.MatchString(f.ReleaseID) || !tokenRE.MatchString(f.ReleaseRunID) || !tokenRE.MatchString(f.AttemptID) ||
		(f.Architecture != "amd64" && f.Architecture != "arm64") || !validSHA(f.JournalID, false) ||
		!validSHA(f.JournalBoundaryHeadSHA256, false) || !validSHA(f.JournalBoundaryManifestSHA256, false) ||
		!validSHA(f.LedgerBeforeSHA256, true) || !validSHA(f.LedgerPlannedSHA256, false) || !validSHA(f.AuthorityDescriptorSHA256, false) ||
		f.AuthorityEpoch == 0 || f.AuthoritySequence == 0 ||
		((f.LedgerBeforeSHA256 == strings.Repeat("0", 64)) != (f.AuthorityEpoch == 1 && f.AuthoritySequence == 1)) ||
		f.EffectiveNotBeforeEpoch <= 0 || f.EffectiveNotAfterEpoch <= f.EffectiveNotBeforeEpoch ||
		f.ReservedAtEpoch < f.EffectiveNotBeforeEpoch || f.ReservedAtEpoch >= f.EffectiveNotAfterEpoch {
		return nil, "", errors.New("CA42 nonce reservation fields invalid")
	}
	values := []string{Format, "reserved", f.NonceID, f.TransactionID, f.ClaimFormat, f.ClaimSHA256, f.ClaimCanonicalSHA256,
		f.ArtifactSetFormat, f.ArtifactSetBindingSHA256, f.ProfileSHA256, f.ReleaseID, f.ReleaseRunID, f.AttemptID, f.Architecture,
		f.JournalID, f.JournalBoundaryHeadSHA256, f.JournalBoundaryManifestSHA256, f.LedgerBeforeSHA256, f.LedgerPlannedSHA256,
		f.AuthorityDescriptorSHA256, strconv.FormatUint(f.AuthorityEpoch, 10), strconv.FormatUint(f.AuthoritySequence, 10),
		strconv.FormatInt(f.EffectiveNotBeforeEpoch, 10), strconv.FormatInt(f.EffectiveNotAfterEpoch, 10), strconv.FormatInt(f.ReservedAtEpoch, 10), strings.Repeat("0", 64)}
	return buildRecord(reservedFieldNames[:], values)
}

func committedRecordBytes(f committedFields) ([]byte, string, error) {
	if !validSHA(f.NonceID, false) || !validSHA(f.TransactionID, false) || !validSHA(f.ReservationSHA256, false) ||
		!validSHA(f.JournalVerifiedHeadSHA256, false) || !validSHA(f.JournalVerifiedManifestSHA256, false) ||
		!validSHA(f.LedgerAfterSHA256, false) || !validSHA(f.ConsumerOperationID, false) ||
		!validSHA(f.ConsumerReceiptSHA256, false) || f.ConsumerReceiptSize <= 0 || f.ConsumerReceiptSize > MaxConsumerReceiptBytes ||
		f.ConsumerCompletedAtEpoch <= 0 || f.CommittedAtEpoch < f.ConsumerCompletedAtEpoch {
		return nil, "", errors.New("CA42 nonce committed fields invalid")
	}
	values := []string{Format, "committed", f.NonceID, f.TransactionID, f.ReservationSHA256,
		f.JournalVerifiedHeadSHA256, f.JournalVerifiedManifestSHA256, f.LedgerAfterSHA256, f.ConsumerOperationID,
		f.ConsumerReceiptSHA256, strconv.FormatInt(f.ConsumerReceiptSize, 10), strconv.FormatInt(f.ConsumerCompletedAtEpoch, 10),
		strconv.FormatInt(f.CommittedAtEpoch, 10), f.ReservationSHA256}
	return buildRecord(committedFieldNames[:], values)
}

func recoveryRecordBytes(f recoveryFields) ([]byte, string, error) {
	if !validSHA(f.NonceID, false) || !validSHA(f.TransactionID, false) || !validSHA(f.ReservationSHA256, false) ||
		!allowedRecoveryReasons[f.ReasonCode] || f.OccurredAtEpoch <= 0 {
		return nil, "", errors.New("CA42 nonce recovery fields invalid")
	}
	values := []string{Format, "recovery_required", f.NonceID, f.TransactionID, f.ReservationSHA256,
		f.ReasonCode, strconv.FormatInt(f.OccurredAtEpoch, 10), f.ReservationSHA256}
	return buildRecord(recoveryFieldNames[:], values)
}

func buildRecord(names, values []string) ([]byte, string, error) {
	if len(names) != len(values)+1 {
		return nil, "", errors.New("CA42 nonce record field count invalid")
	}
	var body strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00=") {
			return nil, "", errors.New("CA42 nonce record value invalid")
		}
		body.WriteString(names[index])
		body.WriteByte('=')
		body.WriteString(value)
		body.WriteByte('\n')
	}
	digest := domainSHA256([]byte(body.String()))
	data := append([]byte(body.String()), []byte(names[len(names)-1]+"="+digest+"\n")...)
	return data, digest, nil
}

func parseRecord(name string, data []byte, names []string) (record, error) {
	if len(data) == 0 || len(data) > MaxRecordBytes || data[len(data)-1] != '\n' || bytes.HasSuffix(data, []byte("\n\n")) ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return record{}, errors.New("CA42 nonce record envelope invalid")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(names) {
		return record{}, errors.New("CA42 nonce record field count invalid")
	}
	values := make(map[string]string, len(names))
	for index, field := range names {
		prefix := []byte(field + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return record{}, fmt.Errorf("CA42 nonce record field order invalid: %s", field)
		}
		values[field] = string(lines[index][len(prefix):])
	}
	if values["format"] != Format || !validSHA(values["record_sha256"], false) {
		return record{}, errors.New("CA42 nonce record identity invalid")
	}
	bodyLength := len(data) - len(names[len(names)-1]) - 1 - 64 - 1
	if bodyLength <= 0 || domainSHA256(data[:bodyLength]) != values["record_sha256"] {
		return record{}, errors.New("CA42 nonce record hash mismatch")
	}
	return record{name: name, kind: values["record"], canonical: string(data), sha256: values["record_sha256"], values: values}, nil
}

func validSHA(value string, allowZero bool) bool {
	return hex64RE.MatchString(value) && (allowZero || value != strings.Repeat("0", 64))
}

func validateReservationRecord(v map[string]string) error {
	notBefore, err1 := canonicalPositiveInt64(v["effective_not_before_epoch"])
	notAfter, err2 := canonicalPositiveInt64(v["effective_not_after_epoch"])
	reservedAt, err3 := canonicalPositiveInt64(v["reserved_at_epoch"])
	authorityEpoch, err4 := canonicalPositiveUint(v["authority_epoch"])
	authoritySequence, err5 := canonicalPositiveUint(v["authority_sequence"])
	if !validSHA(v["nonce_id"], false) || !validSHA(v["transaction_id"], false) || v["claim_format"] != ca42artifactsv2.ConsumptionClaimFormat ||
		!validSHA(v["claim_sha256"], false) || !validSHA(v["claim_canonical_sha256"], false) || v["artifact_set_format"] != ca42artifactsv2.Format ||
		!validSHA(v["artifact_set_binding_sha256"], false) || !validSHA(v["profile_sha256"], false) ||
		!tokenRE.MatchString(v["release_id"]) || !tokenRE.MatchString(v["release_run_id"]) || !tokenRE.MatchString(v["attempt_id"]) ||
		(v["architecture"] != "amd64" && v["architecture"] != "arm64") || !validSHA(v["journal_id"], false) ||
		!validSHA(v["journal_boundary_head_sha256"], false) || !validSHA(v["journal_boundary_manifest_sha256"], false) ||
		!validSHA(v["ledger_before_sha256"], true) || !validSHA(v["ledger_planned_sha256"], false) || !validSHA(v["authority_descriptor_sha256"], false) ||
		err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || authorityEpoch == 0 || authoritySequence == 0 ||
		((v["ledger_before_sha256"] == strings.Repeat("0", 64)) != (authorityEpoch == 1 && authoritySequence == 1)) ||
		notAfter <= notBefore || reservedAt < notBefore || reservedAt >= notAfter {
		return errors.New("CA42 nonce reservation semantics invalid")
	}
	return nil
}

func validateCommittedRecord(v map[string]string) error {
	size, err1 := canonicalPositiveInt64(v["consumer_receipt_size"])
	completedAt, err2 := canonicalPositiveInt64(v["consumer_completed_at_epoch"])
	committedAt, err3 := canonicalPositiveInt64(v["committed_at_epoch"])
	if !validSHA(v["nonce_id"], false) || !validSHA(v["transaction_id"], false) || !validSHA(v["reservation_sha256"], false) ||
		!validSHA(v["journal_verified_head_sha256"], false) || !validSHA(v["journal_verified_manifest_sha256"], false) ||
		!validSHA(v["ledger_after_sha256"], false) || !validSHA(v["consumer_operation_id"], false) ||
		!validSHA(v["consumer_receipt_sha256"], false) || err1 != nil || err2 != nil || err3 != nil ||
		size <= 0 || size > MaxConsumerReceiptBytes || completedAt <= 0 || committedAt < completedAt {
		return errors.New("CA42 nonce committed semantics invalid")
	}
	return nil
}

func validateRecoveryRecord(v map[string]string) error {
	occurredAt, err := canonicalPositiveInt64(v["occurred_at_epoch"])
	if !validSHA(v["nonce_id"], false) || !validSHA(v["transaction_id"], false) || !validSHA(v["reservation_sha256"], false) ||
		!allowedRecoveryReasons[v["reason_code"]] || err != nil || occurredAt <= 0 {
		return errors.New("CA42 nonce recovery semantics invalid")
	}
	return nil
}

func canonicalPositiveInt64(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("non-canonical positive integer")
	}
	return parsed, nil
}

func canonicalPositiveUint(value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("non-canonical positive integer")
	}
	return parsed, nil
}

func domainSHA256(body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(recordDomain))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
