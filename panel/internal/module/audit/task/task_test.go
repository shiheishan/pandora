package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

type purgeCall struct {
	before time.Time
	batch  int
}

type fakePurger struct {
	counts []int
	calls  []purgeCall
	idx    int
}

func (f *fakePurger) Purge(_ context.Context, before time.Time, batch int) (int, error) {
	f.calls = append(f.calls, purgeCall{before: before, batch: batch})
	if f.idx >= len(f.counts) {
		return 0, nil
	}
	n := f.counts[f.idx]
	f.idx++
	if n < 0 {
		return 0, errors.New("purge failed")
	}
	return n, nil
}

func TestRetentionPurgeSequence(t *testing.T) {
	fixed := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	wantBefore := fixed.AddDate(0, 0, -RetentionDays)

	fp := &fakePurger{counts: []int{5000, 5000, 10}}
	m := New(fp)
	m.now = func() time.Time { return fixed }

	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.calls) != 3 {
		t.Fatalf("calls: got %d want 3", len(fp.calls))
	}
	for i, c := range fp.calls {
		if !c.before.Equal(wantBefore) {
			t.Errorf("call %d before: got %v want %v", i, c.before, wantBefore)
		}
		if c.batch != purgeBatch {
			t.Errorf("call %d batch: got %d want %d", i, c.batch, purgeBatch)
		}
	}
}

func TestRetentionStopsAtMaxBatches(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	counts := make([]int, maxBatches+5)
	for i := range counts {
		counts[i] = purgeBatch
	}
	fp := &fakePurger{counts: counts}
	m := New(fp)
	m.now = func() time.Time { return fixed }

	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.calls) != maxBatches {
		t.Fatalf("calls: got %d want %d", len(fp.calls), maxBatches)
	}
}

func TestRetentionPurgeError(t *testing.T) {
	fp := &fakePurger{counts: []int{-1}}
	m := New(fp)
	m.now = func() time.Time { return time.Now() }

	err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(fp.calls) != 1 {
		t.Fatalf("calls: got %d want 1", len(fp.calls))
	}
}

func TestPeriodicSingleRetentionJob(t *testing.T) {
	m := New(&fakePurger{})
	ps := m.Periodic()
	if len(ps) != 1 {
		t.Fatalf("periodic: got %d want 1", len(ps))
	}
	if ps[0].Type != TypeRetention {
		t.Fatalf("type: got %q want %q", ps[0].Type, TypeRetention)
	}
}
