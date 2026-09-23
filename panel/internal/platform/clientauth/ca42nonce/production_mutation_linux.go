//go:build linux && (amd64 || arm64)

package ca42nonce

import (
	"bytes"
	"errors"
	"runtime"
)

// ErrMutationAmbiguous marks a production nonce mutation whose durable result
// cannot be proven. The owning ProductionStore is poisoned and may only be
// closed; callers must enter cross-store recovery rather than retry in place.
var ErrMutationAmbiguous = errors.New("CA42 nonce production mutation outcome ambiguous")

// reserveProduction is deliberately package-private. It keeps the canonical
// capability locked across prevalidation, mutation/reconciliation and
// postvalidation, closes the child session before returning, and never exposes
// mutation authority. A future cross-package bridge must supply fields only by
// revalidating opaque live capabilities, never caller scalars.
func (s *ProductionStore) reserveProduction(fields reservationFields) (Snapshot, bool, error) {
	if s == nil || s.lease == nil {
		return Snapshot{}, false, errors.New("CA42 nonce production store closed")
	}
	expected, _, err := reservationRecordBytes(fields)
	if err != nil {
		return Snapshot{}, false, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !s.lease.mu.TryLock() {
		return Snapshot{}, false, ErrStoreBusy
	}
	defer s.lease.mu.Unlock()
	if s.lease.closed || s.capability == nil || s.capability.state == nil || s.store == nil {
		return Snapshot{}, false, errors.New("CA42 nonce production store closed")
	}
	if s.lease.poisoned != nil {
		return Snapshot{}, false, s.lease.poisoned
	}
	capabilityState := s.capability.state
	if !capabilityState.mu.TryLock() {
		return Snapshot{}, false, ErrStoreBusy
	}
	defer capabilityState.mu.Unlock()
	if err := s.capability.validateLocked(); err != nil {
		return Snapshot{}, false, err
	}
	if err := validateProductionRetainedStore(s.store); err != nil {
		return Snapshot{}, false, err
	}
	session, recovered, mutationErr := s.store.reserve(fields)
	if mutationErr != nil {
		if errors.Is(mutationErr, ErrStoreBusy) {
			return Snapshot{}, false, mutationErr
		}
		return s.reconcileProductionReserve(fields, expected, mutationErr)
	}
	return s.finishProductionReserve(session, recovered, expected)
}

func (s *ProductionStore) reconcileProductionReserve(fields reservationFields, expected []byte, mutationErr error) (Snapshot, bool, error) {
	session, openErr := s.store.openExisting(fields.NonceID, fields.TransactionID)
	if errors.Is(openErr, ErrReservationIncomplete) {
		return Snapshot{}, false, ErrReservationIncomplete
	}
	if openErr != nil {
		return Snapshot{}, false, s.poisonProductionMutation(errors.Join(mutationErr, openErr))
	}
	snapshot, _, finishErr := s.finishProductionReserve(session, true, expected)
	if finishErr != nil {
		return Snapshot{}, false, finishErr
	}
	return snapshot, true, nil
}

func (s *ProductionStore) finishProductionReserve(session *nonceSession, recovered bool, expected []byte) (Snapshot, bool, error) {
	if session == nil {
		return Snapshot{}, false, s.poisonProductionMutation(errors.New("CA42 nonce production reservation session missing"))
	}
	snapshot, inspectErr := session.inspect()
	exact := inspectErr == nil && snapshot.parsed && snapshot.State() == Reserved &&
		bytes.Equal(snapshot.records[ReservedRecordName], expected)
	closeErr := session.close()
	storeErr := validateProductionRetainedStore(s.store)
	capabilityErr := s.capability.validateLocked()
	if !exact || inspectErr != nil || closeErr != nil || storeErr != nil || capabilityErr != nil {
		return Snapshot{}, false, s.poisonProductionMutation(errors.Join(inspectErr, closeErr, storeErr, capabilityErr))
	}
	return snapshot, recovered, nil
}

func validateProductionRetainedStore(store *retainedStore) error {
	if store == nil || store.lease == nil {
		return errors.New("CA42 nonce production store closed")
	}
	if !store.lease.mu.TryLock() {
		return ErrStoreBusy
	}
	defer store.lease.mu.Unlock()
	return store.validateLocked()
}

func (s *ProductionStore) poisonProductionMutation(cause error) error {
	err := errors.Join(ErrMutationAmbiguous, cause)
	if s != nil && s.lease != nil && s.lease.poisoned == nil {
		s.lease.poisoned = err
	}
	return err
}
