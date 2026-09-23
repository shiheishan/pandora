//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42artifactsv2"
)

type v3HandoffProbe struct {
	mu              sync.Mutex
	order           []string
	invClose        int
	ctlClose        int
	authClose       int
	invErr          error
	ctlErr          error
	authErr         error
	invCloseStarted chan struct{}
	invCloseRelease chan struct{}
	invOnClose      func()
	ctlOnClose      func()
	authOnClose     func()
}

type v3HandoffInventory struct{ probe *v3HandoffProbe }

func (lease *v3HandoffInventory) Revalidate(ctx context.Context) error { return ctx.Err() }
func (lease *v3HandoffInventory) Close() error {
	lease.probe.mu.Lock()
	lease.probe.invClose++
	lease.probe.order = append(lease.probe.order, "inventory")
	err, started, release, onClose := lease.probe.invErr, lease.probe.invCloseStarted, lease.probe.invCloseRelease, lease.probe.invOnClose
	lease.probe.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	if onClose != nil {
		onClose()
	}
	return err
}

type v3HandoffControl struct{ probe *v3HandoffProbe }

func (lease *v3HandoffControl) Revalidate(ctx context.Context) error { return ctx.Err() }
func (lease *v3HandoffControl) Close() error {
	lease.probe.mu.Lock()
	defer lease.probe.mu.Unlock()
	lease.probe.ctlClose++
	lease.probe.order = append(lease.probe.order, "control")
	err, onClose := lease.probe.ctlErr, lease.probe.ctlOnClose
	if onClose != nil {
		onClose()
	}
	return err
}

type v3HandoffAuthority struct{ probe *v3HandoffProbe }

func (lease *v3HandoffAuthority) Revalidate(ctx context.Context) error { return ctx.Err() }
func (lease *v3HandoffAuthority) Close() error {
	lease.probe.mu.Lock()
	defer lease.probe.mu.Unlock()
	lease.probe.authClose++
	lease.probe.order = append(lease.probe.order, "authority")
	err, onClose := lease.probe.authErr, lease.probe.authOnClose
	if onClose != nil {
		onClose()
	}
	return err
}

func newV3HandoffFixture(t *testing.T, probe *v3HandoffProbe) *v3ArtifactHandoff {
	t.Helper()
	return &v3ArtifactHandoff{state: &v3ArtifactHandoffState{
		set: ca42artifactsv2.Set{}, authority: &v3HandoffAuthority{probe: probe}, inventory: &v3HandoffInventory{probe: probe}, control: &v3HandoffControl{probe: probe},
	}}
}

func TestV3ArtifactHandoffTakeExactlyOnceAcrossShallowCopies(t *testing.T) {
	probe := &v3HandoffProbe{}
	handoff := newV3HandoffFixture(t, probe)
	copyHandoff := *handoff
	owned, err := copyHandoff.take()
	if err != nil || owned.authority == nil || owned.inventory == nil || owned.control == nil {
		t.Fatalf("first take failed: owned=%+v err=%v", owned, err)
	}
	if _, err := handoff.take(); !errors.Is(err, errV3ArtifactHandoffUnavailable) {
		t.Fatalf("second take succeeded: %v", err)
	}
	if err := handoff.Close(); err != nil {
		t.Fatal(err)
	}
	if probe.invClose != 0 || probe.ctlClose != 0 || probe.authClose != 0 {
		t.Fatal("consumed handoff closed transferred resources")
	}
	if err := owned.inventory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owned.control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owned.authority.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV3ArtifactHandoffCloseBeforeTakeReversesAndJoinsErrors(t *testing.T) {
	invErr, ctlErr, authErr := errors.New("inventory close"), errors.New("control close"), errors.New("authority close")
	probe := &v3HandoffProbe{invErr: invErr, ctlErr: ctlErr, authErr: authErr}
	handoff := newV3HandoffFixture(t, probe)
	err := handoff.Close()
	if !errors.Is(err, invErr) || !errors.Is(err, ctlErr) || !errors.Is(err, authErr) {
		t.Fatalf("close errors not joined: %v", err)
	}
	if len(probe.order) != 3 || probe.order[0] != "inventory" || probe.order[1] != "control" || probe.order[2] != "authority" ||
		probe.invClose != 1 || probe.ctlClose != 1 || probe.authClose != 1 {
		t.Fatalf("rollback order/count invalid: order=%v inventory=%d control=%d authority=%d", probe.order, probe.invClose, probe.ctlClose, probe.authClose)
	}
	if repeat := handoff.Close(); !errors.Is(repeat, invErr) || !errors.Is(repeat, ctlErr) || !errors.Is(repeat, authErr) {
		t.Fatalf("repeated Close lost shared cleanup result: %v", repeat)
	}
	if _, err := handoff.take(); !errors.Is(err, errV3ArtifactHandoffUnavailable) {
		t.Fatalf("closed handoff was consumed: %v", err)
	}
}

func TestV3ArtifactHandoffTakeCloseRaceHasSingleOwner(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		probe := &v3HandoffProbe{}
		handoff := newV3HandoffFixture(t, probe)
		start := make(chan struct{})
		taken := make(chan struct {
			owned v3SessionOwnership
			err   error
		}, 1)
		closed := make(chan error, 1)
		go func() {
			<-start
			owned, err := handoff.take()
			taken <- struct {
				owned v3SessionOwnership
				err   error
			}{owned, err}
		}()
		go func() { <-start; closed <- handoff.Close() }()
		close(start)
		result, closeErr := <-taken, <-closed
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if result.err == nil {
			if probe.invClose != 0 || probe.ctlClose != 0 || probe.authClose != 0 {
				t.Fatal("handoff close raced after transfer and closed session-owned resources")
			}
			_ = result.owned.inventory.Close()
			_ = result.owned.control.Close()
			_ = result.owned.authority.Close()
		} else if !errors.Is(result.err, errV3ArtifactHandoffUnavailable) || probe.invClose != 1 || probe.ctlClose != 1 || probe.authClose != 1 {
			t.Fatalf("race lost resources: take=%v inventory=%d control=%d authority=%d", result.err, probe.invClose, probe.ctlClose, probe.authClose)
		}
		if probe.invClose != 1 || probe.ctlClose != 1 || probe.authClose != 1 {
			t.Fatalf("resource close count invalid: inventory=%d control=%d authority=%d", probe.invClose, probe.ctlClose, probe.authClose)
		}
	}
}

func TestV3ArtifactHandoffRejectsTypedNilLeases(t *testing.T) {
	if handoff, err := newV3ArtifactHandoff(context.Background(), nil, nil, nil); handoff != nil || !errors.Is(err, errV3ArtifactHandoffUnavailable) {
		t.Fatalf("nil production provenance accepted: handoff=%v err=%v", handoff, err)
	}
}

func TestV3ArtifactHandoffRejectsWithOpsControlOrigin(t *testing.T) {
	fixture := newV3ControlFixture(t)
	raw, err := openV3ControlBundleWithOps(context.Background(), fixture.attemptID, fixture.ops())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	production := &productionV3ControlBundle{bundle: raw}
	if bound, bindErr := production.bindParsedGraph(context.Background(), ca42artifactsv2.Set{}); bound != nil || !errors.Is(bindErr, errV3ArtifactHandoffUnavailable) {
		t.Fatalf("WithOps control reached core binder: bound=%v err=%v", bound, bindErr)
	}
	forged := &boundV3ControlBundle{production: production, binding: &v3AttestationCoreBinding{attemptID: fixture.attemptID}}
	if forged.valid() {
		t.Fatal("WithOps control bundle acquired production provenance")
	}
}

func TestV3ArtifactHandoffConcurrentCloseWaitsAndSharesError(t *testing.T) {
	sentinel := errors.New("inventory-close")
	probe := &v3HandoffProbe{invErr: sentinel, invCloseStarted: make(chan struct{}), invCloseRelease: make(chan struct{})}
	handoff := newV3HandoffFixture(t, probe)
	copyHandoff := *handoff
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- handoff.Close() }()
	<-probe.invCloseStarted
	go func() { second <- copyHandoff.Close() }()
	select {
	case err := <-second:
		t.Fatalf("concurrent Close returned before owner cleanup completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(probe.invCloseRelease)
	firstErr, secondErr := <-first, <-second
	if !errors.Is(firstErr, sentinel) || !errors.Is(secondErr, sentinel) {
		t.Fatalf("concurrent Close did not share first result: first=%v second=%v", firstErr, secondErr)
	}
	if probe.invClose != 1 || probe.ctlClose != 1 || probe.authClose != 1 {
		t.Fatalf("concurrent Close duplicated cleanup: inventory=%d control=%d authority=%d", probe.invClose, probe.ctlClose, probe.authClose)
	}
}

func TestV3ArtifactHandoffChildClosePanicAndGoexitReleaseRemainingOwners(t *testing.T) {
	t.Run("inventory-panic", func(t *testing.T) {
		probe := &v3HandoffProbe{invOnClose: func() { panic("inventory-close-panic") }}
		handoff := newV3HandoffFixture(t, probe)
		func() {
			defer func() {
				if recovered := recover(); recovered != "inventory-close-panic" {
					t.Fatalf("unexpected panic: %v", recovered)
				}
			}()
			_ = handoff.Close()
		}()
		if probe.invClose != 1 || probe.ctlClose != 1 || probe.authClose != 1 {
			t.Fatalf("panic interrupted reverse cleanup: inventory=%d control=%d authority=%d", probe.invClose, probe.ctlClose, probe.authClose)
		}
	})
	t.Run("control-goexit", func(t *testing.T) {
		probe := &v3HandoffProbe{ctlOnClose: runtime.Goexit}
		handoff := newV3HandoffFixture(t, probe)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = handoff.Close()
			t.Error("runtime.Goexit returned")
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("child Close Goexit cleanup deadlocked")
		}
		if probe.invClose != 1 || probe.ctlClose != 1 || probe.authClose != 1 {
			t.Fatalf("Goexit interrupted reverse cleanup: inventory=%d control=%d authority=%d", probe.invClose, probe.ctlClose, probe.authClose)
		}
	})
}
