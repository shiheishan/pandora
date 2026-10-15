package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

type fakeService struct {
	n              int
	err            error
	calls          int
	gotNilR        bool
	autoCloseN     int
	autoCloseErr   error
	autoCloseCalls int
}

func (f *fakeService) EscalateOverdueScheduled(_ context.Context) (int, error) {
	f.calls++
	f.gotNilR = true
	return f.n, f.err
}

func (f *fakeService) AutoCloseScheduled(_ context.Context) (int, error) {
	f.autoCloseCalls++
	return f.autoCloseN, f.autoCloseErr
}

func TestEscalateCallsServiceWithoutReply(t *testing.T) {
	fs := &fakeService{n: 3}
	if err := New(fs).escalate(context.Background(), asynq.NewTask(TypeEscalateSLA, nil)); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if fs.calls != 1 || !fs.gotNilR {
		t.Fatalf("calls=%d nilReply=%v", fs.calls, fs.gotNilR)
	}
}

func TestEscalateError(t *testing.T) {
	fs := &fakeService{err: errors.New("db down")}
	if err := New(fs).escalate(context.Background(), asynq.NewTask(TypeEscalateSLA, nil)); err == nil {
		t.Fatal("期望返回错误")
	}
}

func TestPeriodic(t *testing.T) {
	ps := New(&fakeService{}).Periodic()
	if len(ps) != 2 {
		t.Fatalf("periodic %d", len(ps))
	}
	p0 := ps[0]
	if p0.Cronspec != "*/5 * * * *" || p0.Type != TypeEscalateSLA {
		t.Fatalf("periodic[0] %+v", p0)
	}
	p1 := ps[1]
	if p1.Cronspec != "2-59/10 * * * *" || p1.Type != TypeAutoClose {
		t.Fatalf("periodic[1] %+v", p1)
	}
	if escalateTimeout != 30*time.Second {
		t.Fatalf("timeout %v", escalateTimeout)
	}
	if autoCloseTimeout != 30*time.Second {
		t.Fatalf("autoCloseTimeout %v", autoCloseTimeout)
	}
}

func TestAutoCloseCallsService(t *testing.T) {
	fs := &fakeService{autoCloseN: 2}
	if err := New(fs).autoClose(context.Background(), asynq.NewTask(TypeAutoClose, nil)); err != nil {
		t.Fatalf("autoClose: %v", err)
	}
	if fs.autoCloseCalls != 1 {
		t.Fatalf("autoCloseCalls=%d", fs.autoCloseCalls)
	}
}

func TestAutoCloseError(t *testing.T) {
	fs := &fakeService{autoCloseErr: errors.New("db down")}
	if err := New(fs).autoClose(context.Background(), asynq.NewTask(TypeAutoClose, nil)); err == nil {
		t.Fatal("期望返回错误")
	}
}
