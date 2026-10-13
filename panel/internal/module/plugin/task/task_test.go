package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/plugin/service"
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

func TestPeriodic(t *testing.T) {
	ps := New(&fakeService{}).Periodic()
	want := []struct{ typ, cron string }{
		{service.TaskDispatch, "@every 1m"},
		{TypeRetention, "56 3 * * *"},
	}
	if len(ps) != len(want) {
		t.Fatalf("len %d want %d", len(ps), len(want))
	}
	for i, w := range want {
		if ps[i].Type != w.typ || ps[i].Cronspec != w.cron {
			t.Fatalf("periodic %d: %+v", i, ps[i])
		}
		if len(ps[i].Opts) != 4 {
			t.Fatalf("periodic %d opts len %d want 4", i, len(ps[i].Opts))
		}
	}
}

func TestRetentionPurgeSequence(t *testing.T) {
	fixed := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	want := fixed.AddDate(0, 0, -service.RetentionDays)
	fs := &fakeService{counts: []int{purgeBatch, purgeBatch, 12}}
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

// 派发出错时 handler 只记日志、返回 nil（不让任务进归档占住入队用的固定 TaskID）。
func TestDispatchErrorSwallowed(t *testing.T) {
	fs := &fakeService{dispatchN: 2}
	m := New(fs)
	if err := m.dispatch(context.Background(), asynq.NewTask(service.TaskDispatch, nil)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	fs.dispatchErr = boom
	if err := m.dispatch(context.Background(), asynq.NewTask(service.TaskDispatch, nil)); err != nil {
		t.Fatalf("err %v", err)
	}
	if fs.dispatched != 2 {
		t.Fatalf("dispatched %d", fs.dispatched)
	}
}
