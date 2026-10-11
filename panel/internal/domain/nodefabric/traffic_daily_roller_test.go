package nodefabric

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// fakeDailyStore 是进度机器的假库：missing 里的日子是「小时表有、按天表没有」的缺口。
type fakeDailyStore struct {
	missing map[time.Time]bool
	probes  int
	rolled  []time.Time
	failDay time.Time
	failErr error
}

func (f *fakeDailyStore) trafficDailyPendingDays(_ context.Context, _ string, first, last time.Time) ([]time.Time, error) {
	f.probes++
	var out []time.Time
	for d := last; !d.Before(first); d = d.Add(-24 * time.Hour) {
		if f.missing[d] {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDailyStore) rollupTrafficDay(_ context.Context, _ string, day time.Time) (int64, error) {
	if !f.failDay.IsZero() && day.Equal(f.failDay) {
		return 0, f.failErr
	}
	f.rolled = append(f.rolled, day)
	delete(f.missing, day)
	return 2, nil
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestTrafficDailyWindow(t *testing.T) {
	// 10-09 00:09 UTC：昨天（10-08）还没结束满 10 分钟，最后可汇总的是 10-07
	first, last := trafficDailyWindow(time.Date(2026, 10, 9, 0, 9, 59, 0, time.UTC))
	if !last.Equal(day(2026, 10, 7)) {
		t.Fatalf("last = %v, want 2026-10-07 (10-08 has not been over for ten minutes)", last)
	}
	// 10-09 00:10 UTC：10-08 刚好满 10 分钟
	_, last = trafficDailyWindow(time.Date(2026, 10, 9, 0, 10, 0, 0, time.UTC))
	if !last.Equal(day(2026, 10, 8)) {
		t.Fatalf("last = %v, want 2026-10-08", last)
	}
	// 70 天线 = 07-31 某时刻，整天都在线内的第一天是 08-01，再留一天余量即 08-02
	if !first.Equal(day(2026, 8, 2)) {
		t.Fatalf("first = %v, want 2026-08-02 (one day of margin after the 70-day line)", first)
	}
	// 非 UTC 时区的时钟不改变日界
	cst := time.FixedZone("CST", 8*3600)
	_, last = trafficDailyWindow(time.Date(2026, 10, 9, 8, 10, 0, 0, cst))
	if !last.Equal(day(2026, 10, 8)) {
		t.Fatalf("last (UTC+8 clock) = %v, want 2026-10-08", last)
	}
}

func TestTrafficDailyRollerChecksOnlyAtStartAndDayBoundary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	store := &fakeDailyStore{missing: map[time.Time]bool{
		day(2026, 10, 8): true, day(2026, 10, 5): true, day(2026, 10, 4): true, day(2026, 10, 1): true,
		day(2026, 8, 1): true, // 掉出窗口的，永远不汇总
	}}
	r := &trafficDailyRoller{now: func() time.Time { return now }}

	// 启动后第一轮：核对一次，补洞最新的三天（新 → 旧）
	n, err := r.refresh(ctx, store, "t")
	if err != nil || n != 6 || store.probes != 1 {
		t.Fatalf("first round: rows=%d err=%v probes=%d, want 6 rows after one probe", n, err, store.probes)
	}
	if want := []time.Time{day(2026, 10, 8), day(2026, 10, 5), day(2026, 10, 4)}; !slices.Equal(store.rolled, want) {
		t.Fatalf("first round rolled %v, want %v", store.rolled, want)
	}
	// 第二轮：补完剩下的（10-01），不再核对库
	n, err = r.refresh(ctx, store, "t")
	if err != nil || n != 2 || store.probes != 1 {
		t.Fatalf("second round: rows=%d err=%v probes=%d, want 2 rows and still one probe", n, err, store.probes)
	}
	// 之后同一天里的轮次：什么都不做，不碰库
	for i := 0; i < 30; i++ {
		now = now.Add(10 * time.Minute)
		if n, err := r.refresh(ctx, store, "t"); err != nil || n != 0 {
			t.Fatalf("idle round %d: rows=%d err=%v", i, n, err)
		}
	}
	if store.probes != 1 || len(store.rolled) != 4 {
		t.Fatalf("idle rounds touched the store: probes=%d rolled=%v", store.probes, store.rolled)
	}
	if slices.Contains(store.rolled, day(2026, 8, 1)) {
		t.Fatal("a day outside the retention window was rolled up")
	}

	// 日界：10-09 在 10-10 00:10 之前不算结束
	now = time.Date(2026, 10, 10, 0, 9, 0, 0, time.UTC)
	if n, _ := r.refresh(ctx, store, "t"); n != 0 || store.probes != 1 {
		t.Fatalf("before the boundary: rows=%d probes=%d", n, store.probes)
	}
	store.missing[day(2026, 10, 9)] = true
	now = time.Date(2026, 10, 10, 0, 10, 0, 0, time.UTC)
	n, err = r.refresh(ctx, store, "t")
	if err != nil || n != 2 || store.probes != 2 {
		t.Fatalf("after the boundary: rows=%d err=%v probes=%d, want the new day rolled after a second probe", n, err, store.probes)
	}
	if store.rolled[len(store.rolled)-1] != day(2026, 10, 9) {
		t.Fatalf("rolled %v, want 2026-10-09 last", store.rolled)
	}
	// 换日核对整窗口：人工补进来的旧缺口最多一天内被发现
	store.missing[day(2026, 9, 20)] = true
	now = time.Date(2026, 10, 11, 0, 10, 0, 0, time.UTC)
	if _, err := r.refresh(ctx, store, "t"); err != nil || store.probes != 3 || store.rolled[len(store.rolled)-1] != day(2026, 9, 20) {
		t.Fatalf("day-boundary recheck: err=%v probes=%d rolled=%v, want the late gap found", err, store.probes, store.rolled)
	}
}

func TestTrafficDailyRollerKeepsFailedDays(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	boom := errors.New("boom")
	store := &fakeDailyStore{
		missing: map[time.Time]bool{day(2026, 10, 8): true, day(2026, 10, 7): true},
		failDay: day(2026, 10, 7), failErr: boom,
	}
	r := &trafficDailyRoller{now: func() time.Time { return now }}
	if n, err := r.refresh(ctx, store, "t"); !errors.Is(err, boom) || n != 2 {
		t.Fatalf("round 1: rows=%d err=%v, want the first day rolled and the failure reported", n, err)
	}
	// 下一轮接着做失败的那天，不重新核对库
	store.failDay = time.Time{}
	if n, err := r.refresh(ctx, store, "t"); err != nil || n != 2 || store.probes != 1 {
		t.Fatalf("round 2: rows=%d err=%v probes=%d, want the failed day retried without another probe", n, err, store.probes)
	}
}

func TestTrafficDailyRollerRetriesFailedProbe(t *testing.T) {
	ctx := context.Background()
	r := &trafficDailyRoller{now: func() time.Time { return time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC) }}
	bad := &erroringProbeStore{}
	if _, err := r.refresh(ctx, bad, "t"); err == nil {
		t.Fatal("probe failure was swallowed")
	}
	good := &fakeDailyStore{missing: map[time.Time]bool{day(2026, 10, 8): true}}
	if n, err := r.refresh(ctx, good, "t"); err != nil || n != 2 || good.probes != 1 {
		t.Fatalf("after a failed probe: rows=%d err=%v probes=%d, want a fresh probe next round", n, err, good.probes)
	}
}

type erroringProbeStore struct{ fakeDailyStore }

func (*erroringProbeStore) trafficDailyPendingDays(context.Context, string, time.Time, time.Time) ([]time.Time, error) {
	return nil, errors.New("db down")
}
