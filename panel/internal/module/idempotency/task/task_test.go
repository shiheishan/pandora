package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/hibiken/asynq"
)

type fakePurger struct {
	returns []int64
	calls   []purgeCall
	err     error
}

type purgeCall struct {
	before time.Time
	batch  int
}

func (f *fakePurger) Purge(ctx context.Context, before time.Time, batch int) (int64, error) {
	f.calls = append(f.calls, purgeCall{before: before, batch: batch})
	if f.err != nil {
		return 0, f.err
	}
	idx := len(f.calls) - 1
	if idx >= len(f.returns) {
		return 0, nil
	}
	return f.returns[idx], nil
}

func TestModulePeriodic(t *testing.T) {
	m := &Module{svc: &fakePurger{}}
	ps := m.Periodic()
	if len(ps) != 1 {
		t.Fatalf("Periodic 应只有一项，得到 %d", len(ps))
	}
	p := ps[0]
	if p.Type != TypeRetention {
		t.Fatalf("Type 应为 %q，得到 %q", TypeRetention, p.Type)
	}
	parts := strings.Fields(p.Cronspec)
	if len(parts) != 5 {
		t.Fatalf("Cronspec 应为 5 段，得到 %q", p.Cronspec)
	}
	for i := 2; i < 5; i++ {
		if parts[i] != "*" {
			t.Fatalf("Cronspec 后三段应为 *，得到 %q", p.Cronspec)
		}
	}
	var hasLowQueue bool
	for _, opt := range p.Opts {
		if opt.Type() == asynq.QueueOpt && opt.Value() == "low" {
			hasLowQueue = true
			break
		}
	}
	if !hasLowQueue {
		t.Fatal("Opts 应包含 asynq.Queue(\"low\")")
	}
}

func TestRetentionPurgeBatchesUntilPartial(t *testing.T) {
	fixed := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	wantBefore := fixed.Add(-service.Retention)
	fp := &fakePurger{returns: []int64{1000, 1000, 3}}
	m := &Module{svc: fp, now: func() time.Time { return fixed }}

	if err := m.retention(context.Background(), nil); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.calls) != 3 {
		t.Fatalf("Purge 应调用 3 次，得到 %d", len(fp.calls))
	}
	for _, c := range fp.calls {
		if !c.before.Equal(wantBefore) {
			t.Fatalf("before 应为 %v，得到 %v", wantBefore, c.before)
		}
		if c.batch != purgeBatch {
			t.Fatalf("batch 应为 %d，得到 %d", purgeBatch, c.batch)
		}
	}
}

func TestRetentionPurgeMaxBatches(t *testing.T) {
	fp := &fakePurger{returns: make([]int64, maxBatches+10)}
	for i := range fp.returns {
		fp.returns[i] = 1000
	}
	m := &Module{svc: fp, now: time.Now}

	if err := m.retention(context.Background(), nil); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.calls) != maxBatches {
		t.Fatalf("Purge 应调用 %d 次，得到 %d", maxBatches, len(fp.calls))
	}
}

func TestRetentionPurgeError(t *testing.T) {
	sentinel := errors.New("purge failed")
	fp := &fakePurger{err: sentinel}
	m := &Module{svc: fp, now: time.Now}

	err := m.retention(context.Background(), nil)
	if err == nil {
		t.Fatal("retention 应返回错误")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is 应匹配原错误，得到 %v", err)
	}
}
