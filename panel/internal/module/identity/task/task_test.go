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
	regErr    error
	codesSeen bool
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
	if len(ps) != 1 {
		t.Fatalf("periodic len=%d", len(ps))
	}
	if ps[0].Type != TypeRetention || ps[0].Cronspec == "" {
		t.Fatalf("type=%q cronspec=%q", ps[0].Type, ps[0].Cronspec)
	}
}
