package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/hibiken/asynq"
)

type purgeCall struct {
	before time.Time
	batch  int
}

type fakeService struct {
	dispatchN   int
	dispatchErr error
	dispatched  int

	counts []int // 每次清理返回的行数；负数表示出错
	calls  []purgeCall
}

func (f *fakeService) Dispatch(context.Context) (int, error) {
	f.dispatched++
	return f.dispatchN, f.dispatchErr
}

func (f *fakeService) PurgeDeliveries(_ context.Context, before time.Time, batch int) (int, error) {
	f.calls = append(f.calls, purgeCall{before: before, batch: batch})
	i := len(f.calls) - 1
	if i >= len(f.counts) {
		return 0, nil
	}
	if f.counts[i] < 0 {
		return 0, errors.New("purge failed")
	}
	return f.counts[i], nil
}

func TestRetentionPurgeSequence(t *testing.T) {
	fixed := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	want := fixed.AddDate(0, 0, -service.RetentionDays)
	fs := &fakeService{counts: []int{purgeBatch, purgeBatch, 3}}
	m := New(fs)
	m.now = func() time.Time { return fixed }

	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fs.calls) != 3 {
		t.Fatalf("calls %d want 3", len(fs.calls))
	}
	for i, c := range fs.calls {
		if !c.before.Equal(want) || c.batch != purgeBatch {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
}

func TestRetentionStopsAtMaxBatches(t *testing.T) {
	counts := make([]int, maxBatches+10)
	for i := range counts {
		counts[i] = purgeBatch
	}
	fs := &fakeService{counts: counts}
	if err := New(fs).retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatal(err)
	}
	if len(fs.calls) != maxBatches {
		t.Fatalf("calls %d want %d", len(fs.calls), maxBatches)
	}
}

func TestRetentionError(t *testing.T) {
	fs := &fakeService{counts: []int{purgeBatch, -1}}
	if err := New(fs).retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err == nil {
		t.Fatal("want error")
	}
}

func TestDispatch(t *testing.T) {
	fs := &fakeService{dispatchN: 2}
	m := New(fs)
	if err := m.dispatch(context.Background(), asynq.NewTask(service.TaskDispatch, nil)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	fs.dispatchErr = boom
	if err := m.dispatch(context.Background(), asynq.NewTask(service.TaskDispatch, nil)); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if fs.dispatched != 2 {
		t.Fatalf("dispatched %d", fs.dispatched)
	}
}

func TestPeriodic(t *testing.T) {
	ps := New(&fakeService{}).Periodic()
	if len(ps) != 2 {
		t.Fatalf("len %d", len(ps))
	}
	if ps[0].Type != service.TaskDispatch || ps[0].Cronspec != "@every 30s" {
		t.Fatalf("dispatch %+v", ps[0])
	}
	if ps[1].Type != TypeRetention || ps[1].Cronspec != "47 3 * * *" {
		t.Fatalf("retention %+v", ps[1])
	}
}
