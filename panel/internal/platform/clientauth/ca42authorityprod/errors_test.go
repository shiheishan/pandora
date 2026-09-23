package ca42authorityprod

import (
	"errors"
	"testing"
)

func TestStableErrorClassifiersCannotBeReboundByCallers(t *testing.T) {
	foreign := errors.New("foreign")
	if !IsBusy(errors.Join(foreign, errLedgerBusy)) || IsBusy(foreign) {
		t.Fatal("busy error classification changed")
	}
	if !IsCommitAmbiguous(errors.Join(foreign, errLedgerCommitAmbiguous)) || IsCommitAmbiguous(foreign) {
		t.Fatal("ambiguous commit classification changed")
	}
}
