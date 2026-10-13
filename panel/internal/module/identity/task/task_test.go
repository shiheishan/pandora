package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type purgeCall struct {
	before time.Time
	batch  int
}

type seqPurger struct {
	counts []int
	calls  []purgeCall
	idx    int
}

func (s *seqPurger) purge(ctx context.Context, before time.Time, batch int) (int, error) {
	s.calls = append(s.calls, purgeCall{before: before, batch: batch})
	if s.idx >= len(s.counts) {
		return 0, nil
	}
	n := s.counts[s.idx]
	s.idx++
	if n < 0 {
		return 0, errors.New("purge failed")
	}
	return n, nil
}

func TestPurgeAllSequence(t *testing.T) {
	before := time.Now().Add(-1)
	p := &seqPurger{counts: []int{5000, 5000, 10}}
	total, err := purgeAll(context.Background(), before, p.purge)
	if err != nil {
		t.Fatal(err)
	}
	if total != 10010 || len(p.calls) != 3 {
		t.Fatalf("total=%d calls=%d", total, len(p.calls))
	}
}

func TestPurgeAllStopsEarly(t *testing.T) {
	p := &seqPurger{counts: []int{100}}
	total, err := purgeAll(context.Background(), time.Now(), p.purge)
	if err != nil || total != 100 || len(p.calls) != 1 {
		t.Fatalf("total=%d calls=%d err=%v", total, len(p.calls), err)
	}
}

func TestPurgeAllErrorReturnsPartial(t *testing.T) {
	p := &seqPurger{counts: []int{5000, -1}}
	total, err := purgeAll(context.Background(), time.Now(), p.purge)
	if err == nil || total != 5000 || len(p.calls) != 2 {
		t.Fatalf("total=%d calls=%d err=%v", total, len(p.calls), err)
	}
}

func TestPurgeAllMaxBatches(t *testing.T) {
	counts := make([]int, maxBatches+5)
	for i := range counts {
		counts[i] = purgeBatch
	}
	p := &seqPurger{counts: counts}
	_, err := purgeAll(context.Background(), time.Now(), p.purge)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != maxBatches {
		t.Fatalf("calls=%d want %d", len(p.calls), maxBatches)
	}
}

func TestPurgeAllCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &seqPurger{counts: []int{5000}}
	total, err := purgeAll(ctx, time.Now(), p.purge)
	if !errors.Is(err, context.Canceled) || total != 0 || len(p.calls) != 0 {
		t.Fatalf("total=%d calls=%d err=%v", total, len(p.calls), err)
	}
}

type retentionFake struct {
	reg       seqPurger
	codes     seqPurger
	jobs      seqPurger
	regErr    error
	codesSeen bool

	runIDs    []uuid.UUID
	runErr    error
	reconErr  error
	reconRuns int
}

func (f *retentionFake) PurgeGenerationJobs(ctx context.Context, before time.Time, batch int) (int, error) {
	return f.jobs.purge(ctx, before, batch)
}

func (f *retentionFake) RunGenerationJob(_ context.Context, id uuid.UUID) error {
	f.runIDs = append(f.runIDs, id)
	return f.runErr
}

func (f *retentionFake) ReconcileGeneration(context.Context) (int, int64, error) {
	f.reconRuns++
	return 1, 2, f.reconErr
}

func (f *retentionFake) PurgeRegistrations(ctx context.Context, before time.Time, batch int) (int, error) {
	if f.regErr != nil {
		return 0, f.regErr
	}
	return f.reg.purge(ctx, before, batch)
}

func (f *retentionFake) PurgeCodes(ctx context.Context, before time.Time, batch int) (int, error) {
	f.codesSeen = true
	return f.codes.purge(ctx, before, batch)
}

func TestRetentionBeforeAndRegError(t *testing.T) {
	fixed := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	wantBefore := fixed.AddDate(0, 0, -RetentionDays)

	rf := &retentionFake{reg: seqPurger{counts: []int{3}}, codes: seqPurger{counts: []int{2}}}
	m := New(rf)
	m.now = func() time.Time { return fixed }
	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatal(err)
	}
	for _, c := range rf.reg.calls {
		if !c.before.Equal(wantBefore) || c.batch != purgeBatch {
			t.Fatalf("reg before/batch: %v %d", c.before, c.batch)
		}
	}
	for _, c := range rf.codes.calls {
		if !c.before.Equal(wantBefore) {
			t.Fatalf("codes before: %v want %v", c.before, wantBefore)
		}
	}
	wantJobsBefore := fixed.AddDate(0, 0, -service.GenerationRetentionDays)
	if len(rf.jobs.calls) != 1 || !rf.jobs.calls[0].before.Equal(wantJobsBefore) {
		t.Fatalf("generation jobs calls: %+v want before %v", rf.jobs.calls, wantJobsBefore)
	}

	rf2 := &retentionFake{regErr: errors.New("reg fail")}
	m2 := New(rf2)
	m2.now = func() time.Time { return fixed }
	if err := m2.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err == nil {
		t.Fatal("应返回错误")
	}
	if rf2.codesSeen {
		t.Fatal("注册清理失败时不应调 PurgeCodes")
	}
}

func TestPeriodicRetention(t *testing.T) {
	m := New(&retentionFake{})
	ps := m.Periodic()
	if len(ps) != 2 {
		t.Fatalf("periodic len=%d", len(ps))
	}
	types := map[string]string{}
	for _, p := range ps {
		types[p.Type] = p.Cronspec
	}
	if types[TypeRetention] == "" || types[service.TaskReconcileGeneration] != "@every 1m" {
		t.Fatalf("periodic: %+v", types)
	}
}

func TestGenerateHandler(t *testing.T) {
	f := &retentionFake{runErr: errors.New("boom")}
	m := New(f)
	id := uuid.New()
	task, err := jobs.NewTask(context.Background(), service.TaskGenerateUsers, service.GenerationPayload{JobID: id})
	if err != nil {
		t.Fatal(err)
	}
	// 业务出错只记日志、回 nil，免得 asynq 归档占着 TaskID
	if err := m.generate(context.Background(), task); err != nil {
		t.Fatalf("generate err=%v", err)
	}
	if len(f.runIDs) != 1 || f.runIDs[0] != id {
		t.Fatalf("run ids=%v", f.runIDs)
	}
	// 载荷解不开才回错
	if err := m.generate(context.Background(), asynq.NewTask(service.TaskGenerateUsers, []byte("{"))); err == nil {
		t.Fatal("坏载荷应返回错误")
	}
}

func TestReconcileHandler(t *testing.T) {
	f := &retentionFake{}
	m := New(f)
	if err := m.reconcile(context.Background(), asynq.NewTask(service.TaskReconcileGeneration, nil)); err != nil {
		t.Fatal(err)
	}
	f.reconErr = errors.New("db down")
	if err := m.reconcile(context.Background(), asynq.NewTask(service.TaskReconcileGeneration, nil)); err == nil {
		t.Fatal("应返回错误")
	}
	if f.reconRuns != 2 {
		t.Fatalf("runs=%d", f.reconRuns)
	}
}
