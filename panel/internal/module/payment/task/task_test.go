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

	purgeReturns []int
	purgeErr     error
	purgeBefore  []time.Time
	purgeBatch   []int
}

func (f *fakePatroller) PurgeUnverifiedEvents(_ context.Context, before time.Time, batch int) (int, error) {
	f.purgeBefore = append(f.purgeBefore, before)
	f.purgeBatch = append(f.purgeBatch, batch)
	if f.purgeErr != nil {
		return 0, f.purgeErr
	}
	i := len(f.purgeBefore) - 1
	if i < len(f.purgeReturns) {
		return f.purgeReturns[i], nil
	}
	return 0, nil
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

func optsOf(p []asynq.Option) (queue string, retry int, unique, timeout time.Duration) {
	retry = -1
	for _, o := range p {
		switch o.Type() {
		case asynq.QueueOpt:
			queue = o.Value().(string)
		case asynq.MaxRetryOpt:
			retry = o.Value().(int)
		case asynq.UniqueOpt:
			unique = o.Value().(time.Duration)
		case asynq.TimeoutOpt:
			timeout = o.Value().(time.Duration)
		}
	}
	return
}

func TestPeriodicSchedule(t *testing.T) {
	ps := New(&fakePatroller{}).Periodic()
	if len(ps) != 2 {
		t.Fatalf("periodic: got %d", len(ps))
	}
	p := ps[0]
	if p.Type != TypeQueryPatrol || p.Cronspec != "@every 1m" {
		t.Fatalf("periodic = %+v", p)
	}
	if patrolTimeout >= time.Minute {
		t.Fatalf("patrolTimeout %v 必须短于周期", patrolTimeout)
	}
	if queue, retry, _, _ := optsOf(p.Opts); queue != "critical" || retry != 0 {
		t.Fatalf("queue=%q retry=%d", queue, retry)
	}

	r := ps[1]
	if r.Type != TypeRetention || r.Type != "retention:payment_unverified_events" || r.Cronspec != "53 3 * * *" {
		t.Fatalf("retention periodic = %+v", r)
	}
	queue, retry, unique, timeout := optsOf(r.Opts)
	if queue != "low" || retry != 0 || unique != 23*time.Hour || timeout != 5*time.Minute {
		t.Fatalf("retention opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}
}

func TestRetentionLoopsUntilShortBatch(t *testing.T) {
	now := time.Date(2026, 10, 12, 3, 53, 0, 0, time.UTC)
	fp := &fakePatroller{purgeReturns: []int{purgeBatch, purgeBatch, 7}}
	m := New(fp)
	m.now = func() time.Time { return now }
	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.purgeBefore) != 3 {
		t.Fatalf("calls=%d, want 3", len(fp.purgeBefore))
	}
	want := now.AddDate(0, 0, -RetentionDays)
	for i, b := range fp.purgeBefore {
		if !b.Equal(want) || fp.purgeBatch[i] != purgeBatch {
			t.Fatalf("call %d: before=%v batch=%d", i, b, fp.purgeBatch[i])
		}
	}
}

func TestRetentionStopsAtMaxBatches(t *testing.T) {
	full := make([]int, maxBatches+5)
	for i := range full {
		full[i] = purgeBatch
	}
	fp := &fakePatroller{purgeReturns: full}
	if err := New(fp).retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.purgeBefore) != maxBatches {
		t.Fatalf("calls=%d, want %d", len(fp.purgeBefore), maxBatches)
	}
}

func TestRetentionReturnsError(t *testing.T) {
	fp := &fakePatroller{purgeErr: errors.New("db down")}
	if err := New(fp).retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err == nil {
		t.Fatal("want error")
	}
}
