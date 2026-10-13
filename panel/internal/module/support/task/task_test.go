package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"

)

type fakeService struct {
	n       int
	err     error
	calls   int
	gotNilR bool
}

func (f *fakeService) EscalateOverdueScheduled(_ context.Context) (int, error) {
	f.calls++
	f.gotNilR = true
	return f.n, f.err
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
	if len(ps) != 1 {
		t.Fatalf("periodic %d", len(ps))
	}
	p := ps[0]
	if p.Cronspec != "*/5 * * * *" || p.Type != TypeEscalateSLA {
		t.Fatalf("periodic %+v", p)
	}
	if escalateTimeout != 30*time.Second {
		t.Fatalf("timeout %v", escalateTimeout)
	}
}
