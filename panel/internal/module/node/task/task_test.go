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

	rechecks   int
	recheckErr error

	resets   int
	resetRet int
	resetErr error

	echPurges   int
	echPurgeRet int64
	echPurgeErr error

	watches  int
	watchRet int
	watchErr error
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

func (f *fakeSvc) RecheckStrictDevices(_ context.Context) error {
	f.rechecks++
	return f.recheckErr
}

func (f *fakeSvc) ResetDueNodeTraffic(_ context.Context) (int, error) {
	f.resets++
	if f.resetErr != nil {
		return 0, f.resetErr
	}
	return f.resetRet, nil
}

func (f *fakeSvc) PurgeRetiredECHKeys(_ context.Context) (int64, error) {
	f.echPurges++
	if f.echPurgeErr != nil {
		return 0, f.echPurgeErr
	}
	return f.echPurgeRet, nil
}

func (f *fakeSvc) WatchUpgrades(_ context.Context) (int, error) {
	f.watches++
	if f.watchErr != nil {
		return 0, f.watchErr
	}
	return f.watchRet, nil
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
	if len(ps) != 5 {
		t.Fatalf("periodic: got %d want 5", len(ps))
	}
	e := ps[0]
	if e.Type != TypeRetention || e.Cronspec != "44 3 * * *" {
		t.Fatalf("retention periodic = %+v", e)
	}
	queue, retry, unique, timeout := optsOf(e.Opts)
	if queue != "low" || retry != 0 || unique != 23*time.Hour || timeout != 10*time.Minute {
		t.Fatalf("retention opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}

	e = ps[1]
	if e.Type != TypeDevicesStrict || e.Cronspec != "@every 1m" {
		t.Fatalf("devices strict periodic = %+v", e)
	}
	queue, retry, unique, timeout = optsOf(e.Opts)
	if queue != "low" || retry != 0 || unique != 50*time.Second || timeout != 30*time.Second {
		t.Fatalf("devices strict opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}
	if unique >= time.Minute {
		t.Fatalf("unique %v should be < 1m", unique)
	}

	e = ps[2]
	if e.Type != TypeTrafficReset || e.Cronspec != "@every 5m" {
		t.Fatalf("traffic reset periodic = %+v", e)
	}
	queue, retry, unique, timeout = optsOf(e.Opts)
	if queue != "low" || retry != 0 || unique != 4*time.Minute || timeout != 1*time.Minute {
		t.Fatalf("traffic reset opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}
	if unique >= 5*time.Minute {
		t.Fatalf("unique %v should be < 5m", unique)
	}

	e = ps[3]
	if e.Type != TypeECHPurge || e.Cronspec != "@every 1h" {
		t.Fatalf("ech purge periodic = %+v", e)
	}
	queue, retry, unique, timeout = optsOf(e.Opts)
	if queue != "low" || retry != 0 || unique != 50*time.Minute || timeout != 1*time.Minute {
		t.Fatalf("ech purge opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
	}

	e = ps[4]
	if e.Type != TypeUpgradeWatch || e.Cronspec != "@every 1m" {
		t.Fatalf("upgrade watch periodic = %+v", e)
	}
	queue, retry, unique, timeout = optsOf(e.Opts)
	if queue != "low" || retry != 0 || unique != 50*time.Second || timeout != 30*time.Second {
		t.Fatalf("upgrade watch opts: queue=%q retry=%d unique=%v timeout=%v", queue, retry, unique, timeout)
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

func TestDevicesStrictCallsService(t *testing.T) {
	fp := &fakeSvc{}
	mod := New(fp)
	if err := mod.devicesStrict(context.Background(), asynq.NewTask(TypeDevicesStrict, nil)); err != nil {
		t.Fatalf("devicesStrict: %v", err)
	}
	if fp.rechecks != 1 {
		t.Fatalf("rechecks=%d want 1", fp.rechecks)
	}
}

func TestDevicesStrictErrorWrapped(t *testing.T) {
	sentinel := errors.New("recheck failed")
	fp := &fakeSvc{recheckErr: sentinel}
	mod := New(fp)
	err := mod.devicesStrict(context.Background(), asynq.NewTask(TypeDevicesStrict, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
}

func TestTrafficResetCallsService(t *testing.T) {
	fp := &fakeSvc{resetRet: 5}
	mod := New(fp)
	if err := mod.trafficReset(context.Background(), asynq.NewTask(TypeTrafficReset, nil)); err != nil {
		t.Fatalf("trafficReset: %v", err)
	}
	if fp.resets != 1 {
		t.Fatalf("resets=%d want 1", fp.resets)
	}
}

func TestTrafficResetErrorWrapped(t *testing.T) {
	sentinel := errors.New("reset failed")
	fp := &fakeSvc{resetErr: sentinel}
	mod := New(fp)
	err := mod.trafficReset(context.Background(), asynq.NewTask(TypeTrafficReset, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
}

func TestECHPurgeCallsService(t *testing.T) {
	fp := &fakeSvc{echPurgeRet: 2}
	mod := New(fp)
	if err := mod.echPurge(context.Background(), asynq.NewTask(TypeECHPurge, nil)); err != nil {
		t.Fatalf("echPurge: %v", err)
	}
	if fp.echPurges != 1 {
		t.Fatalf("echPurges=%d want 1", fp.echPurges)
	}
}

func TestECHPurgeErrorWrapped(t *testing.T) {
	sentinel := errors.New("purge failed")
	fp := &fakeSvc{echPurgeErr: sentinel}
	mod := New(fp)
	err := mod.echPurge(context.Background(), asynq.NewTask(TypeECHPurge, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
}

func TestUpgradeWatchCallsService(t *testing.T) {
	fp := &fakeSvc{watchRet: 3}
	mod := New(fp)
	if err := mod.upgradeWatch(context.Background(), asynq.NewTask(TypeUpgradeWatch, nil)); err != nil {
		t.Fatalf("upgradeWatch: %v", err)
	}
	if fp.watches != 1 {
		t.Fatalf("watches=%d want 1", fp.watches)
	}
}

func TestUpgradeWatchErrorWrapped(t *testing.T) {
	sentinel := errors.New("watch failed")
	fp := &fakeSvc{watchErr: sentinel}
	mod := New(fp)
	err := mod.upgradeWatch(context.Background(), asynq.NewTask(TypeUpgradeWatch, nil))
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is: %v", err)
	}
}
