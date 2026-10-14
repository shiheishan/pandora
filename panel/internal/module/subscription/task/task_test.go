package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	"github.com/hibiken/asynq"
)

type fakeSvc struct {
	expireCalls int
	expireStats service.ExpireStats
	expireErr   error
	expireDL    bool

	rollCalls int
	rollStats service.RollStats
	rollErr   error
	rollDL    bool

	purgeReturns []int64
	purgeErr     error
	purgeCalls   int
}

func (f *fakeSvc) ExpireDue(ctx context.Context) (service.ExpireStats, error) {
	f.expireCalls++
	_, f.expireDL = ctx.Deadline()
	return f.expireStats, f.expireErr
}

func (f *fakeSvc) RollQuotas(ctx context.Context) (service.RollStats, error) {
	f.rollCalls++
	_, f.rollDL = ctx.Deadline()
	return f.rollStats, f.rollErr
}

func (f *fakeSvc) PurgeFetchLogs(_ context.Context, _ int) (int64, error) {
	f.purgeCalls++
	if f.purgeErr != nil {
		return 0, f.purgeErr
	}
	i := f.purgeCalls - 1
	if i < len(f.purgeReturns) {
		return f.purgeReturns[i], nil
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
	if len(ps) != 3 {
		t.Fatalf("periodic: got %d", len(ps))
	}

	e := ps[0]
	if e.Type != TypeExpire || e.Cronspec != "@every 1m" {
		t.Fatalf("expire periodic = %+v", e)
	}
	queue, retry, unique, timeout := optsOf(e.Opts)
	if queue != "critical" || retry != 0 || unique != 55*time.Second || timeout != expireTimeout {
		t.Fatalf("expire opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}

	r := ps[1]
	if r.Type != TypeRollQuota || r.Cronspec != "@every 10m" {
		t.Fatalf("roll periodic = %+v", r)
	}
	queue, retry, unique, timeout = optsOf(r.Opts)
	if queue != "critical" || retry != 0 || unique != 9*time.Minute || timeout != rollTimeout {
		t.Fatalf("roll opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}

	ret := ps[2]
	if ret.Type != TypeRetentionFetchLog || ret.Cronspec != "50 3 * * *" {
		t.Fatalf("retention periodic = %+v", ret)
	}
	queue, retry, unique, timeout = optsOf(ret.Opts)
	if queue != "low" || retry != 0 || unique != 23*time.Hour || timeout != retentionTimeout {
		t.Fatalf("retention opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}
}

func TestRegisterAllTypes(t *testing.T) {
	f := &fakeSvc{purgeReturns: []int64{0}}
	mux := asynq.NewServeMux()
	New(f).Register(mux)
	for _, typ := range []string{TypeExpire, TypeRollQuota, TypeRetentionFetchLog} {
		beforeExpire, beforeRoll, beforePurge := f.expireCalls, f.rollCalls, f.purgeCalls
		task, err := jobs.NewTask(context.Background(), typ, nil)
		if err != nil {
			t.Fatalf("NewTask %s: %v", typ, err)
		}
		if err := mux.ProcessTask(context.Background(), task); err != nil {
			t.Fatalf("ProcessTask %s: %v", typ, err)
		}
		switch typ {
		case TypeExpire:
			if f.expireCalls != beforeExpire+1 {
				t.Fatalf("expire calls=%d", f.expireCalls)
			}
		case TypeRollQuota:
			if f.rollCalls != beforeRoll+1 {
				t.Fatalf("roll calls=%d", f.rollCalls)
			}
		case TypeRetentionFetchLog:
			if f.purgeCalls != beforePurge+1 {
				t.Fatalf("purge calls=%d", f.purgeCalls)
			}
		}
	}
}

func TestPayloadDecode(t *testing.T) {
	task, err := jobs.NewTask(context.Background(), TypeExpire, nil)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if err := jobs.Decode(task, &struct{}{}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}

func TestExpireHasDeadlineAndWrapsError(t *testing.T) {
	fp := &fakeSvc{expireStats: service.ExpireStats{Expired: 1}}
	if err := New(fp).expire(context.Background(), asynq.NewTask(TypeExpire, nil)); err != nil {
		t.Fatalf("expire ok: %v", err)
	}
	if fp.expireCalls != 1 || !fp.expireDL {
		t.Fatalf("calls=%d deadline=%v", fp.expireCalls, fp.expireDL)
	}

	sentinel := errors.New("db down")
	fp2 := &fakeSvc{expireErr: sentinel}
	err := New(fp2).expire(context.Background(), asynq.NewTask(TypeExpire, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
}

func TestRollQuotaWrapsError(t *testing.T) {
	fp := &fakeSvc{rollStats: service.RollStats{Rolled: 1}}
	if err := New(fp).rollQuota(context.Background(), asynq.NewTask(TypeRollQuota, nil)); err != nil {
		t.Fatalf("rollQuota ok: %v", err)
	}
	if fp.rollCalls != 1 || !fp.rollDL {
		t.Fatalf("calls=%d deadline=%v", fp.rollCalls, fp.rollDL)
	}

	sentinel := errors.New("roll failed")
	fp2 := &fakeSvc{rollErr: sentinel}
	err := New(fp2).rollQuota(context.Background(), asynq.NewTask(TypeRollQuota, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
}

// 不满一批就停：5000、5000、3 共三次，不再多查一次空批。
func TestRetentionStopsOnShortBatch(t *testing.T) {
	fp := &fakeSvc{purgeReturns: []int64{purgeBatch, purgeBatch, 3, 0}}
	if err := New(fp).retention(context.Background(), asynq.NewTask(TypeRetentionFetchLog, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if fp.purgeCalls != 3 {
		t.Fatalf("calls=%d, want 3", fp.purgeCalls)
	}
}

func TestRetentionStopsAtMaxBatches(t *testing.T) {
	full := make([]int64, maxPurgeBatches+5)
	for i := range full {
		full[i] = purgeBatch
	}
	fp := &fakeSvc{purgeReturns: full}
	if err := New(fp).retention(context.Background(), asynq.NewTask(TypeRetentionFetchLog, nil)); err != nil {
		t.Fatalf("retention: %v", err)
	}
	if fp.purgeCalls != maxPurgeBatches {
		t.Fatalf("calls=%d, want %d", fp.purgeCalls, maxPurgeBatches)
	}
}

func TestRetentionError(t *testing.T) {
	fp := &fakeSvc{purgeErr: errors.New("db down")}
	if err := New(fp).retention(context.Background(), asynq.NewTask(TypeRetentionFetchLog, nil)); err == nil {
		t.Fatal("want error")
	}
	if fp.purgeCalls != 1 {
		t.Fatalf("calls=%d, want 1", fp.purgeCalls)
	}
}
