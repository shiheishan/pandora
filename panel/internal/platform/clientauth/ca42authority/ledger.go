package ca42authority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

// ErrLedgerCASConflict classifies a durable-ledger snapshot that changed
// between verification and acquisition of the exclusive reservation lock.
var ErrLedgerCASConflict = errors.New("authority ledger compare-and-swap conflict")

const LedgerFormat = "pandora-ca42-authority-ledger-v1"
const MaxLedgerBytes = 16 << 10

var ledgerFieldNames = [...]string{
	"format", "ledger_id", "root_keyset_id", "authority_epoch", "authority_sequence",
	"descriptor_sha256", "previous_descriptor_sha256", "last_trusted_epoch",
	"last_attempt_id", "last_manifest_sha256", "record_sha256",
}

var zeroSHA256 = [sha256.Size]byte{}

type Ledger struct {
	LedgerID              string
	RootKeysetID          string
	AuthorityEpoch        uint64
	AuthoritySequence     uint64
	DescriptorSHA256      [sha256.Size]byte
	PreviousDescriptorSHA [sha256.Size]byte
	LastTrustedEpoch      int64
	LastAttemptID         string
	LastManifestSHA256    [sha256.Size]byte
	RecordSHA256          [sha256.Size]byte
}

func Advance(previous *Ledger, descriptor Descriptor, now time.Time) (Ledger, bool, error) {
	var empty Ledger
	nowEpoch := now.UTC().Unix()
	if nowEpoch <= 0 || nowEpoch < descriptor.ClockFloor.Unix() {
		return empty, false, errors.New("authority ledger trusted clock denied")
	}
	if previous == nil {
		if descriptor.AuthorityEpoch != 1 || descriptor.AuthoritySequence != 1 ||
			descriptor.PreviousDescriptor != zeroSHA256 || descriptor.AuthorizationMode != ModeNormal {
			return empty, false, errors.New("authority ledger genesis denied")
		}
		return newLedger(descriptor, nowEpoch), false, nil
	}
	if err := previous.Validate(); err != nil {
		return empty, false, err
	}
	if previous.LedgerID != descriptor.LedgerID || previous.RootKeysetID != descriptor.RootKeysetID {
		return empty, false, errors.New("authority ledger identity mismatch")
	}
	if nowEpoch < previous.LastTrustedEpoch {
		return empty, false, errors.New("authority ledger clock rollback denied")
	}
	if previous.AuthorityEpoch == descriptor.AuthorityEpoch &&
		previous.AuthoritySequence == descriptor.AuthoritySequence {
		if previous.DescriptorSHA256 == descriptor.SHA256 && previous.LastAttemptID == descriptor.AttemptID &&
			previous.LastManifestSHA256 == descriptor.ReleaseManifestSHA256 {
			return *previous, true, nil
		}
		return empty, false, errors.New("authority ledger divergent retry denied")
	}
	if descriptor.PreviousDescriptor != previous.DescriptorSHA256 {
		return empty, false, errors.New("authority ledger previous descriptor mismatch")
	}
	switch descriptor.AuthorizationMode {
	case ModeNormal:
		validSameEpoch := descriptor.AuthorityEpoch == previous.AuthorityEpoch &&
			descriptor.AuthoritySequence == previous.AuthoritySequence+1
		validNextEpoch := descriptor.AuthorityEpoch == previous.AuthorityEpoch+1 &&
			descriptor.AuthoritySequence == 1
		if !validSameEpoch && !validNextEpoch {
			return empty, false, errors.New("authority ledger normal sequence denied")
		}
	case ModeRecovery:
		if descriptor.AuthorityEpoch <= previous.AuthorityEpoch || descriptor.AuthoritySequence != 1 {
			return empty, false, errors.New("authority ledger recovery sequence denied")
		}
	default:
		return empty, false, errors.New("authority ledger mode denied")
	}
	if descriptor.AttemptID == previous.LastAttemptID || descriptor.ReleaseManifestSHA256 == previous.LastManifestSHA256 {
		return empty, false, errors.New("authority ledger replay identity denied")
	}
	return newLedger(descriptor, nowEpoch), false, nil
}

// ReserveTransition is the pure compare-and-swap policy used immediately
// before a durable ledger write. expected is the snapshot used during bundle
// verification; current is reread only after the exclusive ledger lock is held.
func ReserveTransition(expected, current *Ledger, descriptor Descriptor, now time.Time) (Ledger, bool, error) {
	if !sameLedgerSnapshot(expected, current) {
		return Ledger{}, false, ErrLedgerCASConflict
	}
	return Advance(current, descriptor, now)
}

func sameLedgerSnapshot(first, second *Ledger) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	if first.Validate() != nil || second.Validate() != nil {
		return false
	}
	return first.RecordSHA256 == second.RecordSHA256
}

func newLedger(descriptor Descriptor, nowEpoch int64) Ledger {
	result := Ledger{
		LedgerID: descriptor.LedgerID, RootKeysetID: descriptor.RootKeysetID,
		AuthorityEpoch: descriptor.AuthorityEpoch, AuthoritySequence: descriptor.AuthoritySequence,
		DescriptorSHA256: descriptor.SHA256, PreviousDescriptorSHA: descriptor.PreviousDescriptor,
		LastTrustedEpoch: nowEpoch, LastAttemptID: descriptor.AttemptID,
		LastManifestSHA256: descriptor.ReleaseManifestSHA256,
	}
	result.RecordSHA256 = sha256.Sum256(result.body())
	return result
}

func (l Ledger) Validate() error {
	if !lowerHex64.MatchString(l.LedgerID) || !safeToken.MatchString(l.RootKeysetID) ||
		l.AuthorityEpoch == 0 || l.AuthoritySequence == 0 || l.LastTrustedEpoch <= 0 ||
		!safeToken.MatchString(l.LastAttemptID) || l.DescriptorSHA256 == zeroSHA256 ||
		l.LastManifestSHA256 == zeroSHA256 {
		return errors.New("authority ledger fields invalid")
	}
	expected := sha256.Sum256(l.body())
	if expected != l.RecordSHA256 {
		return errors.New("authority ledger record hash mismatch")
	}
	return nil
}

func (l Ledger) CanonicalBytes() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	result := append([]byte(nil), l.body()...)
	result = append(result, []byte("record_sha256="+hex.EncodeToString(l.RecordSHA256[:])+"\n")...)
	return result, nil
}

func ParseLedger(data []byte) (Ledger, error) {
	var empty Ledger
	if len(data) == 0 || len(data) > MaxLedgerBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("authority ledger envelope invalid")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(ledgerFieldNames) {
		return empty, errors.New("authority ledger field count invalid")
	}
	values := make([]string, len(lines))
	for index, line := range lines {
		prefix := ledgerFieldNames[index] + "="
		if !bytes.HasPrefix(line, []byte(prefix)) || len(line) == len(prefix) {
			return empty, errors.New("authority ledger field order invalid")
		}
		values[index] = string(line[len(prefix):])
	}
	if values[0] != LedgerFormat || !lowerHex64.MatchString(values[1]) ||
		!safeToken.MatchString(values[2]) || !lowerHex64.MatchString(values[5]) ||
		!lowerHex64.MatchString(values[6]) || !safeToken.MatchString(values[8]) ||
		!lowerHex64.MatchString(values[9]) || !lowerHex64.MatchString(values[10]) {
		return empty, errors.New("authority ledger field invalid")
	}
	epoch, err := parsePositiveUint(values[3])
	if err != nil {
		return empty, errors.New("authority ledger epoch invalid")
	}
	sequence, err := parsePositiveUint(values[4])
	if err != nil {
		return empty, errors.New("authority ledger sequence invalid")
	}
	trustedEpoch, err := strconv.ParseInt(values[7], 10, 64)
	if err != nil || trustedEpoch <= 0 || strconv.FormatInt(trustedEpoch, 10) != values[7] {
		return empty, errors.New("authority ledger trusted epoch invalid")
	}
	descriptor, _ := decodeHex32(values[5])
	previous, _ := decodeHex32(values[6])
	manifest, _ := decodeHex32(values[9])
	record, _ := decodeHex32(values[10])
	result := Ledger{
		LedgerID: values[1], RootKeysetID: values[2], AuthorityEpoch: epoch,
		AuthoritySequence: sequence, DescriptorSHA256: descriptor,
		PreviousDescriptorSHA: previous, LastTrustedEpoch: trustedEpoch,
		LastAttemptID: values[8], LastManifestSHA256: manifest, RecordSHA256: record,
	}
	if err := result.Validate(); err != nil {
		return empty, err
	}
	canonical, err := result.CanonicalBytes()
	if err != nil || !bytes.Equal(canonical, data) {
		return empty, errors.New("authority ledger canonical encoding mismatch")
	}
	return result, nil
}

func (l Ledger) body() []byte {
	return []byte(fmt.Sprintf(
		"format=%s\nledger_id=%s\nroot_keyset_id=%s\nauthority_epoch=%d\nauthority_sequence=%d\n"+
			"descriptor_sha256=%s\nprevious_descriptor_sha256=%s\nlast_trusted_epoch=%d\n"+
			"last_attempt_id=%s\nlast_manifest_sha256=%s\n",
		LedgerFormat, l.LedgerID, l.RootKeysetID, l.AuthorityEpoch, l.AuthoritySequence,
		hex.EncodeToString(l.DescriptorSHA256[:]), hex.EncodeToString(l.PreviousDescriptorSHA[:]),
		l.LastTrustedEpoch, l.LastAttemptID, hex.EncodeToString(l.LastManifestSHA256[:]),
	))
}
