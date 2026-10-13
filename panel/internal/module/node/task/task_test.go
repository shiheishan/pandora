package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/hibiken/asynq"
)

type purgeCall struct {
	table string
	batch int
}

type fakeSvc struct {
	calls []purgeCall

	// returns 按调用顺序脚本化返回值；超出长度则返回 0。
	returns  []int64
	purgeErr error
}

func (f *fakeSvc) PurgeRetention(_ context.Context, table string, now time.Time, batch int) (int64, error) {
	f.calls = append(f.calls, purgeCall{table: table, batch: batch})
	if f.purgeErr != nil {
		return 0, f.purgeErr
	}
	i := len(f.calls) - 1
	if i < len(f.returns) {
		return f.returns[i], nil
	}
	return 0, nil
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
	ps := New(&fakeSvc{}).Periodic()
	if len(ps) != 1 {
		t.Fatalf("periodic: got %d", len(ps))
	}
	e := ps[0]
	if e.Type != TypeRetention || e.Cronspec != "41 3 * * *" {
		t.Fatalf("retention periodic = %+v", e)
	}
	queue, retry, unique, timeout := optsOf(e.Opts)
	if queue != "low" || retry != 0 || unique != 23*time.Hour || timeout != 10*time.Minute {
		t.Fatalf("retention opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}
}

func TestRetentionVisitsAllTablesInOrder(t *testing.T) {
	fp := &fakeSvc{returns: make([]int64, len(service.RetentionTables))}
	mod := New(fp)
	mod.now = func() time.Time { return time.Date(2026, 3, 1, 3, 41, 0, 0, time.UTC) }

	if err := mod.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.calls) != len(service.RetentionTables) {
		t.Fatalf("calls=%d want %d", len(fp.calls), len(service.RetentionTables))
	}
	for i, table := range service.RetentionTables {
		if fp.calls[i].table != table {
			t.Fatalf("call %d table=%q want %q", i, fp.calls[i].table, table)
		}
		if fp.calls[i].batch != purgeBatch {
			t.Fatalf("call %d batch=%d want %d", i, fp.calls[i].batch, purgeBatch)
		}
	}
}

func TestRetentionBatchesUntilPartial(t *testing.T) {
	// 第一张表两批满额，第三批不足；其余表各一批。
	returns := []int64{int64(purgeBatch), int64(purgeBatch), 3}
	for i := 3; i < len(service.RetentionTables); i++ {
		returns = append(returns, 0)
	}
	fp := &fakeSvc{returns: returns}
	mod := New(fp)
	mod.now = func() time.Time { return time.Unix(0, 0) }

	if err := mod.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if len(fp.calls) != len(service.RetentionTables)+2 {
		t.Fatalf("calls=%d want %d", len(fp.calls), len(service.RetentionTables)+2)
	}
	for i := 0; i < 3; i++ {
		if fp.calls[i].table != service.RetentionTables[0] {
			t.Fatalf("batch %d table=%q", i, fp.calls[i].table)
		}
	}
	if fp.calls[3].table != service.RetentionTables[1] {
		t.Fatalf("fourth call table=%q want %q", fp.calls[3].table, service.RetentionTables[1])
	}
}

func TestRetentionErrorWrapped(t *testing.T) {
	sentinel := errors.New("db down")
	fp := &fakeSvc{purgeErr: sentinel}
	mod := New(fp)
	mod.now = func() time.Time { return time.Unix(0, 0) }

	err := mod.retention(context.Background(), asynq.NewTask(TypeRetention, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
	if len(fp.calls) != 1 {
		t.Fatalf("calls=%d want 1", len(fp.calls))
	}
}
