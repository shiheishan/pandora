//go:build linux && (amd64 || arm64)

package ca42authorityprod

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

// Root is the opaque canonical production ledger root. It cannot be opened
// from a caller path, directory descriptor or expected ledger snapshot.
type Root struct {
	retained         *ledgerRootCapability
	productionOrigin bool
}

// Reservation retains the root capability and exclusive ledger lock across
// plan, commit, exact retry and admission revalidation.
type Reservation struct {
	state *reservationState
}

type reservationState struct {
	mu              sync.Mutex
	retained        *boundLedgerReservation
	clockFloorEpoch int64
	committed       bool
	binding         *bindingState
	closed          bool
}

// Binding is the only ledger capability intended for the future Admission
// coordinator. A zero value and a caller-created Binding are invalid.
type Binding struct {
	state *bindingState
}

type bindingState struct {
	reservation *reservationState
	values      BindingValues
	parsed      bool
}

// BindingValues is a forgeable diagnostic/encoding projection. It grants no
// root, reservation, mutation, recovery or admission authority.
type BindingValues struct {
	LedgerBeforeSHA256       [sha256.Size]byte
	LedgerPlannedSHA256      [sha256.Size]byte
	LedgerCommittedSHA256    [sha256.Size]byte
	DescriptorSHA256         [sha256.Size]byte
	PreviousDescriptorSHA256 [sha256.Size]byte
	ManifestSHA256           [sha256.Size]byte
	AuthorityEpoch           uint64
	AuthoritySequence        uint64
	TrustedEpoch             int64
	ClockFloorEpoch          int64
	ExactPlan                bool
}

// OpenProductionRoot opens only RootPath and binds it to the current
// supervisor mount/user namespaces. No alternate production opener exists.
func OpenProductionRoot() (*Root, error) {
	retained, err := openProductionLedgerRootCapability()
	if err != nil {
		return nil, err
	}
	return &Root{retained: retained, productionOrigin: true}, nil
}

// beginCurrent remains package-private until its descriptor and trusted time
// come from an unforgeable compiled-root authority capability. Exporting this
// scalar boundary would let a sibling internal package bypass that provenance.
func (root *Root) beginCurrent(descriptor ca42authority.Descriptor, now time.Time) (*Reservation, error) {
	if root == nil || root.retained == nil || !root.productionOrigin || now.IsZero() {
		return nil, errors.New("authority ledger production root invalid")
	}
	retained, err := root.retained.beginCurrentReservation(descriptor, now.UTC())
	if err != nil {
		return nil, err
	}
	return &Reservation{state: &reservationState{retained: retained, clockFloorEpoch: descriptor.ClockFloor.UTC().Unix()}}, nil
}

func (root *Root) Revalidate() error {
	if root == nil || root.retained == nil || !root.productionOrigin {
		return errors.New("authority ledger production root invalid")
	}
	return root.retained.validate()
}

func (root *Root) Close() error {
	if root == nil || root.retained == nil {
		return nil
	}
	return root.retained.close()
}

func (reservation *Reservation) Planned() (ca42authority.Ledger, bool, error) {
	if reservation == nil || reservation.state == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation invalid")
	}
	state := reservation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.retained == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation closed")
	}
	return state.retained.plannedLedger()
}

func (reservation *Reservation) Commit() (ca42authority.Ledger, bool, error) {
	if reservation == nil || reservation.state == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation invalid")
	}
	state := reservation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.retained == nil {
		return ca42authority.Ledger{}, false, errors.New("authority ledger reservation closed")
	}
	committed, recovered, err := state.retained.commit()
	if err != nil {
		return ca42authority.Ledger{}, false, err
	}
	values, err := snapshotCommittedBinding(state.retained, committed, state.clockFloorEpoch)
	if err != nil {
		return ca42authority.Ledger{}, false, errors.Join(errLedgerCommitAmbiguous, err)
	}
	if state.binding != nil && state.binding.values != values {
		return ca42authority.Ledger{}, false, errors.New("authority ledger committed binding changed")
	}
	state.committed = true
	if state.binding == nil {
		state.binding = &bindingState{reservation: state, values: values, parsed: true}
	}
	return committed, recovered, nil
}

// CommittedBinding returns an opaque binding only after an exact committed
// record has been re-read and rebound to the retained reservation.
func (reservation *Reservation) CommittedBinding() (Binding, error) {
	if reservation == nil || reservation.state == nil {
		return Binding{}, errors.New("authority ledger reservation invalid")
	}
	state := reservation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || !state.committed || state.retained == nil || state.binding == nil || !state.binding.parsed {
		return Binding{}, errors.New("authority ledger reservation not committed")
	}
	return Binding{state: state.binding}, nil
}

func (reservation *Reservation) Close() error {
	if reservation == nil || reservation.state == nil {
		return nil
	}
	state := reservation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	retained := state.retained
	state.retained = nil
	if retained == nil {
		return nil
	}
	return retained.close()
}

// RevalidateCommitted re-reads the canonical committed record through the
// retained root and reservation. It accepts no descriptor, ledger or hash.
func (binding Binding) RevalidateCommitted(ctx context.Context) error {
	if ctx == nil || binding.state == nil || !binding.state.parsed || binding.state.reservation == nil {
		return errors.New("authority ledger admission binding invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reservation := binding.state.reservation
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.closed || !reservation.committed || reservation.retained == nil || reservation.binding != binding.state {
		return errors.New("authority ledger admission binding closed")
	}
	committed, _, err := reservation.retained.commit()
	if err != nil {
		return err
	}
	values, err := snapshotCommittedBinding(reservation.retained, committed, reservation.clockFloorEpoch)
	if err != nil {
		return err
	}
	if values != binding.state.values {
		return errors.New("authority ledger admission binding changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (binding Binding) ValuesAt(ctx context.Context) (BindingValues, error) {
	if err := binding.RevalidateCommitted(ctx); err != nil {
		return BindingValues{}, err
	}
	return binding.state.values, nil
}

func snapshotCommittedBinding(retained *boundLedgerReservation, committed ca42authority.Ledger, clockFloorEpoch int64) (BindingValues, error) {
	if retained == nil || retained.state == nil || committed.Validate() != nil || clockFloorEpoch <= 0 {
		return BindingValues{}, errors.New("authority ledger committed binding invalid")
	}
	retained.state.mu.Lock()
	defer retained.state.mu.Unlock()
	if retained.state.closed || retained.state.capability == nil || retained.state.lease == nil || retained.state.lease.state == nil {
		return BindingValues{}, errors.New("authority ledger committed reservation closed")
	}
	leaseState := retained.state.lease.state
	leaseState.mu.Lock()
	defer leaseState.mu.Unlock()
	if err := leaseState.validateLocked(); err != nil {
		return BindingValues{}, err
	}
	if !leaseState.plannedReady || !leaseState.committed || leaseState.planned.RecordSHA256 != committed.RecordSHA256 {
		return BindingValues{}, errors.New("authority ledger committed record mismatch")
	}
	var before [sha256.Size]byte
	if leaseState.before != nil {
		if err := leaseState.before.Validate(); err != nil {
			return BindingValues{}, err
		}
		before = leaseState.before.RecordSHA256
	}
	values := BindingValues{
		LedgerBeforeSHA256: before, LedgerPlannedSHA256: leaseState.planned.RecordSHA256,
		LedgerCommittedSHA256: committed.RecordSHA256, DescriptorSHA256: committed.DescriptorSHA256,
		PreviousDescriptorSHA256: committed.PreviousDescriptorSHA, ManifestSHA256: committed.LastManifestSHA256,
		AuthorityEpoch: committed.AuthorityEpoch, AuthoritySequence: committed.AuthoritySequence,
		TrustedEpoch: committed.LastTrustedEpoch, ClockFloorEpoch: clockFloorEpoch,
		ExactPlan: leaseState.exactPlan,
	}
	if values.LedgerPlannedSHA256 == ([sha256.Size]byte{}) || values.LedgerPlannedSHA256 != values.LedgerCommittedSHA256 ||
		values.DescriptorSHA256 == ([sha256.Size]byte{}) || values.ManifestSHA256 == ([sha256.Size]byte{}) ||
		values.AuthorityEpoch == 0 || values.AuthoritySequence == 0 || values.TrustedEpoch < values.ClockFloorEpoch {
		return BindingValues{}, errors.New("authority ledger committed binding semantics invalid")
	}
	return values, nil
}
