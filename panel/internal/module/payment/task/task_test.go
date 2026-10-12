package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/hibiken/asynq"
)

type fakePatroller struct {
	calls    int
	deadline bool
	stats    service.PatrolStats
	err      error
}

func (f *fakePatroller) ReconcileDue(ctx context.Context) (service.PatrolStats, error) {
	f.calls++
	_, f.deadline = ctx.Deadline()
	return f.stats, f.err
}

func TestQueryPatrolRunsOneRoundWithDeadline(t *testing.T) {
	fp := &fakePatroller{stats: service.PatrolStats{Queried: 2, Reconciled: 1}}
	if err := New(fp).queryPatrol(context.Background(), asynq.NewTask(TypeQueryPatrol, nil)); err != nil {
		t.Fatalf("queryPatrol: %v", err)
	}
	if fp.calls != 1 || !fp.deadline {
		t.Fatalf("calls=%d deadline=%v", fp.calls, fp.deadline)
	}
}

func TestQueryPatrolReturnsError(t *testing.T) {
	fp := &fakePatroller{err: errors.New("db down")}
	if err := New(fp).queryPatrol(context.Background(), asynq.NewTask(TypeQueryPatrol, nil)); err == nil {
		t.Fatal("want error")
	}
}

func TestPeriodicSchedule(t *testing.T) {
	ps := New(&fakePatroller{}).Periodic()
	if len(ps) != 1 {
		t.Fatalf("periodic: got %d", len(ps))
	}
	p := ps[0]
	if p.Type != TypeQueryPatrol || p.Cronspec != "@every 1m" {
		t.Fatalf("periodic = %+v", p)
	}
	if patrolTimeout >= time.Minute {
		t.Fatalf("patrolTimeout %v 必须短于周期", patrolTimeout)
	}
	var queue string
	var retry = -1
	for _, o := range p.Opts {
		switch o.Type() {
		case asynq.QueueOpt:
			queue = o.Value().(string)
		case asynq.MaxRetryOpt:
			retry = o.Value().(int)
		}
	}
	if queue != "critical" || retry != 0 {
		t.Fatalf("queue=%q retry=%d", queue, retry)
	}
}
