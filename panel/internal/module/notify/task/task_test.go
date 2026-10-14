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

	scanN, publishN        int
	scanErr, publishErr    error
	inboxCalls, eventCalls []purgeCall
	codeCalls              []purgeCall
	ticketMsgCalls         []purgeCall
}

func (f *fakeService) Scan(context.Context) (int, error) { return f.scanN, f.scanErr }

func (f *fakeService) PublishDueAnnouncements(context.Context) (int, error) {
	return f.publishN, f.publishErr
}

func (f *fakeService) PurgeInbox(_ context.Context, before time.Time, batch int) (int, error) {
	f.inboxCalls = append(f.inboxCalls, purgeCall{before: before, batch: batch})
	return 0, nil
}

func (f *fakeService) PurgeEvents(_ context.Context, before time.Time, batch int) (int, error) {
	f.eventCalls = append(f.eventCalls, purgeCall{before: before, batch: batch})
	return 0, nil
}

func (f *fakeService) PurgeBindCodes(_ context.Context, before time.Time, batch int) (int, error) {
	f.codeCalls = append(f.codeCalls, purgeCall{before: before, batch: batch})
	return 0, nil
}

func (f *fakeService) PurgeTicketMessages(_ context.Context, before time.Time, batch int) (int, error) {
	f.ticketMsgCalls = append(f.ticketMsgCalls, purgeCall{before: before, batch: batch})
	return 0, nil
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
	want := []struct{ typ, cron string }{
		{service.TaskDispatch, "@every 30s"},
		{service.TaskScan, "@every 5m"},
		{TypePublishAnnouncements, "@every 1m"},
		{TypeRetention, "53 3 * * *"},
	}
	if len(ps) != len(want) {
		t.Fatalf("len %d", len(ps))
	}
	for i, w := range want {
		if ps[i].Type != w.typ || ps[i].Cronspec != w.cron {
			t.Fatalf("periodic %d: %+v", i, ps[i])
		}
	}
}

func TestRetentionAllKinds(t *testing.T) {
	fixed := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	fs := &fakeService{counts: []int{-1}} // 投递清理出错，其余照常
	m := New(fs)
	m.now = func() time.Time { return fixed }
	if err := m.retention(context.Background(), asynq.NewTask(TypeRetention, nil)); err == nil {
		t.Fatal("want error")
	}
	if len(fs.inboxCalls) != 1 || !fs.inboxCalls[0].before.Equal(fixed.AddDate(0, 0, -service.InboxRetentionDays)) {
		t.Fatalf("inbox %+v", fs.inboxCalls)
	}
	if len(fs.eventCalls) != 1 || !fs.eventCalls[0].before.Equal(fixed.AddDate(0, 0, -service.EventRetentionDays)) {
		t.Fatalf("events %+v", fs.eventCalls)
	}
	if len(fs.codeCalls) != 1 || !fs.codeCalls[0].before.Equal(fixed.Add(-bindCodeGrace)) {
		t.Fatalf("codes %+v", fs.codeCalls)
	}
}

func TestScanAndPublish(t *testing.T) {
	boom := errors.New("boom")
	fs := &fakeService{scanN: 3, publishN: 1}
	m := New(fs)
	if err := m.scan(context.Background(), asynq.NewTask(service.TaskScan, nil)); err != nil {
		t.Fatal(err)
	}
	if err := m.publish(context.Background(), asynq.NewTask(TypePublishAnnouncements, nil)); err != nil {
		t.Fatal(err)
	}
	fs.scanErr, fs.publishErr = boom, boom
	if err := m.scan(context.Background(), asynq.NewTask(service.TaskScan, nil)); !errors.Is(err, boom) {
		t.Fatalf("scan err %v", err)
	}
	if err := m.publish(context.Background(), asynq.NewTask(TypePublishAnnouncements, nil)); !errors.Is(err, boom) {
		t.Fatalf("publish err %v", err)
	}
}
