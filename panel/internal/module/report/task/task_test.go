package task

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

type fakeService struct {
	refreshCalls int
	refreshErr   error
	purgeCounts  []int
	purgeCalls   []int
	purgeIdx     int
	integrityRun int
	integrityErr error
}

func (f *fakeService) RunIntegrity(context.Context) error {
	f.integrityRun++
	return f.integrityErr
}

func (f *fakeService) RefreshActivity(context.Context) error {
	f.refreshCalls++
	return f.refreshErr
}

func (f *fakeService) PurgeActivity(_ context.Context, batch int) (int, error) {
	f.purgeCalls = append(f.purgeCalls, batch)
	if f.purgeIdx >= len(f.purgeCounts) {
		return 0, nil
	}
	n := f.purgeCounts[f.purgeIdx]
	f.purgeIdx++
	if n < 0 {
		return 0, errors.New("purge failed")
	}
	return n, nil
}

func TestPeriodicJobs(t *testing.T) {
	m := New(&fakeService{})
	ps := m.Periodic()
	if len(ps) != 3 {
		t.Fatalf("periodic: got %d want 3", len(ps))
	}
	want := []struct {
		typ      string
		cronspec string
	}{
		{TypeActivity, "*/10 * * * *"},
		{TypeRetention, "57 3 * * *"},
		{TypeIntegrity, "19,49 * * * *"},
	}
	for i, w := range want {
		if ps[i].Type != w.typ {
			t.Errorf("periodic[%d] type: got %q want %q", i, ps[i].Type, w.typ)
		}
		if ps[i].Cronspec != w.cronspec {
			t.Errorf("periodic[%d] cronspec: got %q want %q", i, ps[i].Cronspec, w.cronspec)
		}
	}
}

func TestActivityCallsRefreshOnce(t *testing.T) {
	fs := &fakeService{}
	m := New(fs)
	if err := m.activity(context.Background(), asynq.NewTask(TypeActivity, nil)); err != nil {
		t.Fatalf("activity: %v", err)
	}
	if fs.refreshCalls != 1 {
		t.Fatalf("refresh calls: got %d want 1", fs.refreshCalls)
	}
}

func TestActivityRefreshError(t *testing.T) {
	sentinel := errors.New("refresh failed")
	fs := &fakeService{refreshErr: sentinel}
	m := New(fs)
	err := m.activity(context.Background(), asynq.NewTask(TypeActivity, nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: got %v want %v", err, sentinel)
	}
}

func TestRetentionPurgeSequence(t *testing.T) {
	fs := &fakeService{purgeCounts: []int{5000, 5000, 10}}
	m := New(fs)
	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fs.purgeCalls) != 3 {
		t.Fatalf("purge calls: got %d want 3", len(fs.purgeCalls))
	}
	for i, batch := range fs.purgeCalls {
		if batch != purgeBatch {
			t.Errorf("call %d batch: got %d want %d", i, batch, purgeBatch)
		}
	}
}

func TestRetentionStopsAtMaxBatches(t *testing.T) {
	counts := make([]int, maxBatches+5)
	for i := range counts {
		counts[i] = purgeBatch
	}
	fs := &fakeService{purgeCounts: counts}
	m := New(fs)
	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fs.purgeCalls) != maxBatches {
		t.Fatalf("purge calls: got %d want %d", len(fs.purgeCalls), maxBatches)
	}
}

func TestRetentionPurgeError(t *testing.T) {
	fs := &fakeService{purgeCounts: []int{5000, -1}}
	m := New(fs)
	err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if len(fs.purgeCalls) != 2 {
		t.Fatalf("purge calls: got %d want 2", len(fs.purgeCalls))
	}
}

func TestRetentionCanceledContext(t *testing.T) {
	fs := &fakeService{purgeCounts: []int{purgeBatch}}
	m := New(fs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.retention(ctx, asynq.NewTask(TypeRetention, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: got %v want context.Canceled", err)
	}
	if len(fs.purgeCalls) != 0 {
		t.Fatalf("purge calls: got %d want 0", len(fs.purgeCalls))
	}
}

func TestIntegrityRunsAndWrapsError(t *testing.T) {
	fs := &fakeService{}
	m := New(fs)
	if err := m.integrity(context.Background(), asynq.NewTask(TypeIntegrity, nil)); err != nil || fs.integrityRun != 1 {
		t.Fatalf("integrity: err=%v runs=%d", err, fs.integrityRun)
	}
	sentinel := errors.New("check failed")
	fs.integrityErr = sentinel
	if err := m.integrity(context.Background(), asynq.NewTask(TypeIntegrity, nil)); !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: got %v want %v", err, sentinel)
	}
}
