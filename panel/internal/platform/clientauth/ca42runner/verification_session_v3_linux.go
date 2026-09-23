//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/releasejournal"
)

// V3VerificationSession is a read-only aggregate of one opaque ArtifactSet,
// its retained production authority, bound control, production inventory, and
// exact Journal v3 LAYOUT_SWITCHED boundary. It exposes no values, descriptors,
// reservation, or admission mutation authority.
type V3VerificationSession struct {
	state *v3VerificationSessionState
}

type v3VerificationSessionState struct {
	mu        sync.Mutex
	cond      *sync.Cond
	set       ca42artifactsv2.Set
	binding   ca42artifactsv2.JournalBinding
	values    ca42artifactsv2.JournalBindingValues
	authority v3AuthorityLease
	inventory v3InventoryLease
	control   v3ControlLease
	journal   v3JournalLease
	clock     func() time.Time
	ops       v3VerificationOps
	lastTime  time.Time
	active    bool
	closing   bool
	cancel    context.CancelFunc
	poisoned  bool
	closed    bool
	closeErr  error
}

type v3InventoryLease interface {
	Revalidate(context.Context) error
	Close() error
}

type v3JournalLease interface {
	InspectLayoutSwitchedFor(ca42artifactsv2.JournalBinding, time.Time) (releasejournal.V3Snapshot, error)
	Close() error
}

type v3VerificationOps struct {
	now            func() time.Time
	verifySet      func(ca42artifactsv2.Set, time.Time) (ca42artifactsv2.Set, error)
	journalBinding func(ca42artifactsv2.Set, time.Time) (ca42artifactsv2.JournalBinding, error)
	bindingValues  func(ca42artifactsv2.JournalBinding, time.Time) (ca42artifactsv2.JournalBindingValues, error)
	openJournal    func(string) (v3JournalLease, error)
}

func productionV3VerificationOps() v3VerificationOps {
	return v3VerificationOps{
		now: time.Now,
		verifySet: func(candidate ca42artifactsv2.Set, now time.Time) (ca42artifactsv2.Set, error) {
			return candidate.VerifiedCopyAt(now)
		},
		journalBinding: func(candidate ca42artifactsv2.Set, now time.Time) (ca42artifactsv2.JournalBinding, error) {
			return candidate.JournalBindingAt(now)
		},
		bindingValues: func(binding ca42artifactsv2.JournalBinding, now time.Time) (ca42artifactsv2.JournalBindingValues, error) {
			return binding.ValuesAt(now)
		},
		openJournal: func(attemptID string) (v3JournalLease, error) {
			return releasejournal.OpenProductionV3Session(attemptID)
		},
	}
}

func openV3VerificationSessionFromHandoff(ctx context.Context, handoff *v3ArtifactHandoff) (*V3VerificationSession, error) {
	return openV3VerificationSessionFromHandoffWithOps(ctx, handoff, productionV3VerificationOps())
}

func openV3VerificationSessionFromHandoffWithOps(ctx context.Context, handoff *v3ArtifactHandoff, ops v3VerificationOps) (*V3VerificationSession, error) {
	if ctx == nil || handoff == nil {
		return nil, errors.New("CA42 v3 verification handoff input invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owned, err := handoff.take()
	if err != nil {
		return nil, err
	}
	return openV3VerificationSessionOwned(ctx, owned, ops, time.Time{})
}

func openV3VerificationSessionOwned(ctx context.Context, owned v3SessionOwnership, ops v3VerificationOps, lastTime time.Time) (_ *V3VerificationSession, resultErr error) {
	state := &v3VerificationSessionState{set: owned.set, authority: owned.authority, inventory: owned.inventory, control: owned.control,
		clock: ops.now, ops: ops, lastTime: lastTime}
	state.cond = sync.NewCond(&state.mu)
	session := &V3VerificationSession{state: state}
	success := false
	defer func() {
		if !success {
			resultErr = errors.Join(resultErr, session.Close())
		}
	}()
	if ctx == nil || nilV3Lease(owned.authority) || nilV3Lease(owned.inventory) || nilV3Lease(owned.control) ||
		ops.now == nil || ops.verifySet == nil || ops.journalBinding == nil ||
		ops.bindingValues == nil || ops.openJournal == nil {
		return nil, errors.New("CA42 v3 owned verification session input invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now, err := sampleV3VerificationClock(ops.now, &state.lastTime)
	if err != nil {
		return nil, err
	}
	verified, err := ops.verifySet(owned.set, now)
	if err != nil {
		return nil, err
	}
	binding, err := ops.journalBinding(verified, now)
	if err != nil {
		return nil, err
	}
	values, err := ops.bindingValues(binding, now)
	if err != nil || values.AttemptID == "" {
		return nil, errors.Join(errors.New("CA42 v3 journal routing binding invalid"), err)
	}
	if !nilV3Lease(owned.authority) {
		if err := owned.authority.Revalidate(ctx); err != nil {
			return nil, err
		}
	}
	if !nilV3Lease(owned.control) {
		if err := owned.control.Revalidate(ctx); err != nil {
			return nil, err
		}
	}
	if err := owned.inventory.Revalidate(ctx); err != nil {
		return nil, err
	}
	journal, err := ops.openJournal(values.AttemptID)
	if err != nil || nilV3Lease(journal) {
		if !nilV3Lease(journal) {
			err = errors.Join(err, journal.Close())
		}
		return nil, errors.Join(errors.New("CA42 v3 production journal acquisition failed"), err)
	}
	state.journal = journal
	verified, binding, err = verifyV3SessionComponents(ctx, verified, binding, values, owned.authority, owned.inventory, owned.control, journal, ops, &state.lastTime)
	if err != nil {
		return nil, err
	}
	state.set, state.binding, state.values = verified, binding, values
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return session, nil
}

func verifyV3SessionComponents(ctx context.Context, set ca42artifactsv2.Set, binding ca42artifactsv2.JournalBinding,
	expected ca42artifactsv2.JournalBindingValues, authority v3AuthorityLease, inventory v3InventoryLease, control v3ControlLease, journal v3JournalLease,
	ops v3VerificationOps, lastTime *time.Time) (ca42artifactsv2.Set, ca42artifactsv2.JournalBinding, error) {
	if err := ctx.Err(); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	now, err := sampleV3VerificationClock(ops.now, lastTime)
	if err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	verified, err := ops.verifySet(set, now)
	if err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	currentBinding, err := ops.journalBinding(verified, now)
	if err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	values, err := ops.bindingValues(currentBinding, now)
	if err != nil || values != expected {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, errors.Join(errors.New("CA42 v3 artifact binding changed"), err)
	}
	if !nilV3Lease(authority) {
		if err := authority.Revalidate(ctx); err != nil {
			return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
		}
	}
	if !nilV3Lease(control) {
		if err := control.Revalidate(ctx); err != nil {
			return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
		}
	}
	if err := inventory.Revalidate(ctx); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	if _, err := journal.InspectLayoutSwitchedFor(currentBinding, now); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	if err := ctx.Err(); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	finalNow, err := sampleV3VerificationClock(ops.now, lastTime)
	if err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	finalSet, err := ops.verifySet(verified, finalNow)
	if err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	finalBinding, err := ops.journalBinding(finalSet, finalNow)
	if err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	finalValues, err := ops.bindingValues(finalBinding, finalNow)
	if err != nil || finalValues != expected {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, errors.Join(errors.New("CA42 v3 final artifact binding changed"), err)
	}
	if !nilV3Lease(authority) {
		if err := authority.Revalidate(ctx); err != nil {
			return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
		}
	}
	if !nilV3Lease(control) {
		if err := control.Revalidate(ctx); err != nil {
			return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
		}
	}
	if err := inventory.Revalidate(ctx); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	if _, err := journal.InspectLayoutSwitchedFor(finalBinding, finalNow); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	if err := ctx.Err(); err != nil {
		return ca42artifactsv2.Set{}, ca42artifactsv2.JournalBinding{}, err
	}
	return finalSet, finalBinding, nil
}

// Verify rechecks the complete opaque Set, exact Journal v3 boundary, and
// retained production inventory. Any failure poisons and closes the session.
func (session *V3VerificationSession) Verify(ctx context.Context) (result error) {
	if session == nil || session.state == nil || ctx == nil {
		return errors.New("CA42 v3 verification session invalid")
	}
	state := session.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing || state.active || state.cond == nil || state.clock == nil ||
		nilV3Lease(state.authority) || nilV3Lease(state.control) || nilV3Lease(state.inventory) || nilV3Lease(state.journal) {
		state.mu.Unlock()
		return errors.New("CA42 v3 verification session closed, poisoned, or busy")
	}
	if err := ctx.Err(); err != nil {
		state.poisoned, state.closed = true, true
		func() {
			defer state.mu.Unlock()
			closeErr := state.closeOwnedLocked()
			state.closeErr = errors.Join(state.closeErr, closeErr)
			result = errors.Join(err, closeErr)
		}()
		return result
	}
	operationContext, cancel := context.WithCancel(ctx)
	state.active, state.cancel = true, cancel
	set, binding, authority, inventory, control, journal := state.set, state.binding, state.authority, state.inventory, state.control, state.journal
	lastTime := state.lastTime
	values := state.values
	ops := state.ops
	state.mu.Unlock()

	committed := false
	var verified ca42artifactsv2.Set
	var currentBinding ca42artifactsv2.JournalBinding
	defer func() {
		cancel()
		state.mu.Lock()
		defer state.mu.Unlock()
		state.cancel, state.active = nil, false
		state.cond.Broadcast()
		if state.closing && result == nil {
			result = context.Canceled
		}
		if !committed || result != nil {
			state.poisoned, state.closed = true, true
			closeErr := state.closeOwnedLocked()
			state.closeErr = errors.Join(state.closeErr, closeErr)
			result = errors.Join(result, closeErr)
		} else {
			state.set, state.binding, state.lastTime = verified, currentBinding, lastTime
		}
	}()
	func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		verified, currentBinding, result = verifyV3SessionComponents(operationContext, set, binding, values, authority, inventory, control, journal, ops, &lastTime)
	}()
	committed = result == nil
	return result
}

func sampleV3VerificationClock(clock func() time.Time, last *time.Time) (time.Time, error) {
	if clock == nil || last == nil {
		return time.Time{}, errors.New("CA42 v3 verification clock invalid")
	}
	now := clock().UTC()
	if now.IsZero() || (!last.IsZero() && now.Before(*last)) {
		return time.Time{}, errors.New("CA42 v3 verification clock invalid or regressed")
	}
	*last = now
	return now, nil
}

// Close is nil-safe and idempotent. It cancels an active verification and
// closes Journal, Inventory, Control, then Authority in reverse ownership order.
func (session *V3VerificationSession) Close() error {
	if session == nil || session.state == nil {
		return nil
	}
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return state.closeErr
	}
	state.closing = true
	if state.cancel != nil {
		state.cancel()
	}
	for state.active {
		state.cond.Wait()
	}
	state.closed, state.closing = true, false
	state.closeErr = errors.Join(state.closeErr, state.closeOwnedLocked())
	return state.closeErr
}

func (state *v3VerificationSessionState) closeOwnedLocked() error {
	journal, inventory, control, authority := state.journal, state.inventory, state.control, state.authority
	state.journal, state.inventory, state.control, state.authority = nil, nil, nil, nil
	state.set = ca42artifactsv2.Set{}
	state.binding = ca42artifactsv2.JournalBinding{}
	state.values = ca42artifactsv2.JournalBindingValues{}
	state.clock = nil
	state.ops = v3VerificationOps{}
	state.lastTime = time.Time{}
	return closeV3SessionOwned(journal, inventory, control, authority)
}

// closeV3SessionOwned preserves the complete reverse-release chain even when a
// child Close panics or calls runtime.Goexit.
func closeV3SessionOwned(journal v3JournalLease, inventory v3InventoryLease, control v3ControlLease, authority v3AuthorityLease) (result error) {
	defer func() {
		if !nilV3Lease(authority) {
			result = errors.Join(result, authority.Close())
		}
	}()
	defer func() {
		if !nilV3Lease(control) {
			result = errors.Join(result, control.Close())
		}
	}()
	defer func() {
		if !nilV3Lease(inventory) {
			result = errors.Join(result, inventory.Close())
		}
	}()
	if !nilV3Lease(journal) {
		result = errors.Join(result, journal.Close())
	}
	return result
}
