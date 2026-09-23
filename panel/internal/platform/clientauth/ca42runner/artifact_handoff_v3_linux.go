//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

var errV3ArtifactHandoffUnavailable = errors.New("CA42 V3 artifact handoff unavailable")

type v3ControlLease interface {
	Revalidate(context.Context) error
	Close() error
}

type v3AuthorityLease interface {
	Revalidate(context.Context) error
	Close() error
}

// boundV3ControlBundle is intentionally distinct from retainedV3ControlBundle.
// Its binding pointer must be the exact capability installed by bindParsedGraph;
// a boolean or caller-built wrapper carries no authority.
type boundV3ControlBundle struct {
	production *productionV3ControlBundle
	binding    *v3AttestationCoreBinding
}

func (control *boundV3ControlBundle) valid() bool {
	if control == nil || control.binding == nil || control.production == nil || control.production.bundle == nil || control.production.bundle.state == nil {
		return false
	}
	state := control.production.bundle.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.productionOrigin && !state.closed && !state.poisoned && !state.boundTime.IsZero() && state.coreBinding == control.binding
}

func (control *boundV3ControlBundle) Revalidate(ctx context.Context) error {
	if !control.valid() {
		return errV3ArtifactHandoffUnavailable
	}
	return revalidateBoundV3Control(ctx, control)
}

func (control *boundV3ControlBundle) Close() error {
	if control == nil || control.production == nil || control.production.bundle == nil {
		return nil
	}
	return control.production.bundle.Close()
}

type v3ArtifactHandoff struct {
	state *v3ArtifactHandoffState
}

type v3ArtifactHandoffState struct {
	mu        sync.Mutex
	set       ca42artifactsv2.Set
	authority v3AuthorityLease
	inventory v3InventoryLease
	control   v3ControlLease
	consumed  bool
	closing   bool
	closed    bool
	closeDone chan struct{}
	closeErr  error
}

// newV3ArtifactHandoff obtains the exact Set installed by the core binder and
// proves the supplied inventory was retained from that Set. It never accepts a
// detached Set, preventing graph/control/inventory mix-and-match.
func newV3ArtifactHandoff(ctx context.Context, authority *productionAuthorityV3Lease, inventory *ca42storage.InventoryLease,
	control *boundV3ControlBundle) (_ *v3ArtifactHandoff, resultErr error) {
	completed := false
	defer func() {
		if !completed {
			resultErr = errors.Join(resultErr, closeV3HandoffOwned(inventory, control, authority))
		}
	}()
	if ctx == nil || authority == nil || inventory == nil || !control.valid() {
		return nil, errV3ArtifactHandoffUnavailable
	}
	set, err := control.claimSetForHandoff(ctx, authority, inventory)
	if err != nil {
		return nil, err
	}
	completed = true
	return &v3ArtifactHandoff{state: &v3ArtifactHandoffState{set: set, authority: authority, inventory: inventory, control: control}}, nil
}

func (control *boundV3ControlBundle) claimSetForHandoff(ctx context.Context, authority *productionAuthorityV3Lease,
	inventory *ca42storage.InventoryLease) (ca42artifactsv2.Set, error) {
	if ctx == nil || authority == nil || inventory == nil || control == nil || control.production == nil || control.production.bundle == nil || control.binding == nil {
		return ca42artifactsv2.Set{}, errV3ArtifactHandoffUnavailable
	}
	state := control.production.bundle.state
	state.mu.Lock()
	if state.closed || state.poisoned || state.closing || state.active || state.coreBinding != control.binding || state.handoffClaimed {
		state.mu.Unlock()
		return ca42artifactsv2.Set{}, errV3ArtifactHandoffUnavailable
	}
	state.handoffClaimed = true
	set := state.boundSet
	state.mu.Unlock()
	if err := authority.Revalidate(ctx); err != nil {
		return ca42artifactsv2.Set{}, err
	}
	firstTime := time.Now().UTC()
	verified, err := set.VerifiedCopyClaimingInventoryAt(ctx, inventory, firstTime)
	if err != nil {
		return ca42artifactsv2.Set{}, err
	}
	if err := revalidateBoundV3Control(ctx, control); err != nil {
		return ca42artifactsv2.Set{}, err
	}
	secondTime := time.Now().UTC()
	if secondTime.Before(firstTime) {
		return ca42artifactsv2.Set{}, errors.New("CA42 V3 handoff clock moved backwards")
	}
	verified, err = verified.VerifiedCopyBoundToInventoryAt(ctx, inventory, secondTime)
	if err != nil {
		return ca42artifactsv2.Set{}, err
	}
	if err := authority.Revalidate(ctx); err != nil {
		return ca42artifactsv2.Set{}, err
	}
	return verified, nil
}

func nilV3Lease(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type v3SessionOwnership struct {
	set       ca42artifactsv2.Set
	authority v3AuthorityLease
	inventory v3InventoryLease
	control   v3ControlLease
}

func (handoff *v3ArtifactHandoff) take() (v3SessionOwnership, error) {
	if handoff == nil || handoff.state == nil {
		return v3SessionOwnership{}, errV3ArtifactHandoffUnavailable
	}
	state := handoff.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.consumed || nilV3Lease(state.authority) || nilV3Lease(state.inventory) || nilV3Lease(state.control) {
		return v3SessionOwnership{}, errV3ArtifactHandoffUnavailable
	}
	owned := v3SessionOwnership{set: state.set, authority: state.authority, inventory: state.inventory, control: state.control}
	state.set = ca42artifactsv2.Set{}
	state.authority, state.inventory, state.control = nil, nil, nil
	state.consumed = true
	return owned, nil
}

// Close rolls back an unconsumed handoff in reverse composer acquisition
// order. Once take succeeds the source handoff is intentionally inert.
func (handoff *v3ArtifactHandoff) Close() error {
	if handoff == nil || handoff.state == nil {
		return nil
	}
	state := handoff.state
	state.mu.Lock()
	if state.consumed {
		state.mu.Unlock()
		return nil
	}
	if state.closed {
		if !state.closing {
			result := state.closeErr
			state.mu.Unlock()
			return result
		}
		done := state.closeDone
		state.mu.Unlock()
		<-done
		state.mu.Lock()
		result := state.closeErr
		state.mu.Unlock()
		return result
	}
	authority, inventory, control := state.authority, state.inventory, state.control
	state.set = ca42artifactsv2.Set{}
	state.authority, state.inventory, state.control = nil, nil, nil
	state.closed, state.closing = true, true
	state.closeDone = make(chan struct{})
	done := state.closeDone
	state.mu.Unlock()

	var result error
	defer func() {
		state.mu.Lock()
		state.closeErr = errors.Join(state.closeErr, result)
		state.closing = false
		close(done)
		state.mu.Unlock()
	}()
	result = closeV3HandoffOwned(inventory, control, authority)
	return result
}

// closeV3HandoffOwned uses a defer chain so panic or runtime.Goexit from one
// child Close cannot prevent the remaining owners from being released.
func closeV3HandoffOwned(inventory v3InventoryLease, control v3ControlLease, authority v3AuthorityLease) (result error) {
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
	if !nilV3Lease(inventory) {
		result = errors.Join(result, inventory.Close())
	}
	return result
}
