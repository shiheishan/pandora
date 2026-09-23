package ca42authorityprod

import "errors"

var (
	errLedgerBusy            = errors.New("authority ledger busy")
	errLedgerCommitAmbiguous = errors.New("authority ledger commit ambiguous")
)

// IsBusy reports canonical nonblocking ledger lock contention without
// exposing a rebindable package sentinel.
func IsBusy(err error) bool {
	return errors.Is(err, errLedgerBusy)
}

// IsCommitAmbiguous reports a durability outcome that must be reconciled by
// exact replay or recovery and must never be treated as a clean failure.
func IsCommitAmbiguous(err error) bool {
	return errors.Is(err, errLedgerCommitAmbiguous)
}
