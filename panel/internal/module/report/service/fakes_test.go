package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	commissionsvc "github.com/aegispanel/aegis/internal/module/commission/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	"github.com/aegispanel/aegis/internal/module/report/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	supportsvc "github.com/aegispanel/aegis/internal/module/support/service"
)

// 本文件是 service 单测共用的假实现：fakeStore（本模块 repo）、fakeTx、fakeAudit、fakePorts（全部属主模块端口）。
// 各用例只改自己关心的字段；errFor 里登记的方法名会返回 errBoom。

var errBoom = errors.New("boom")

// testNow 是单测的固定时钟：上海 2026-03-10 10:30（UTC 02:30）。
var testNow = time.Date(2026, 3, 10, 2, 30, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// fakeStore
// ---------------------------------------------------------------------------

type fakeStore struct {
	adjustments []model.Adjustment // 插入顺序
	activity    map[string]model.ActivityDaily
	locked      []uuid.UUID
	upserts     [][]model.ActivityDaily
	purgeCalls  []string
	purgeLeft   int // PurgeActivity 还能删的行数（每次至多 batch）
	dbStats     repo.DatabaseStatsRow
	errFor      map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{activity: map[string]model.ActivityDaily{}, errFor: map[string]error{}}
}

func (f *fakeStore) err(name string) error { return f.errFor[name] }

func (f *fakeStore) InsertAdjustment(_ context.Context, a *model.Adjustment) error {
	if err := f.err("InsertAdjustment"); err != nil {
		return err
	}
	if a.ReversalOf != nil {
		for _, x := range f.adjustments {
			if x.ReversalOf != nil && *x.ReversalOf == *a.ReversalOf {
				return repo.ErrDuplicate
			}
		}
	}
	a.ID = uuid.New()
	a.CreatedAt = testNow
	f.adjustments = append(f.adjustments, *a)
	return nil
}

func (f *fakeStore) GetAdjustment(_ context.Context, id uuid.UUID) (model.Adjustment, bool, error) {
	if err := f.err("GetAdjustment"); err != nil {
		return model.Adjustment{}, false, err
	}
	for _, x := range f.adjustments {
		if x.ID == id {
			return x, true, nil
		}
	}
	return model.Adjustment{}, false, nil
}

func (f *fakeStore) LockAdjustment(_ context.Context, id uuid.UUID) error {
	f.locked = append(f.locked, id)
	return f.err("LockAdjustment")
}

func (f *fakeStore) HasReversal(_ context.Context, id uuid.UUID) (bool, error) {
	if err := f.err("HasReversal"); err != nil {
		return false, err
	}
	for _, x := range f.adjustments {
		if x.ReversalOf != nil && *x.ReversalOf == id {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ListAdjustments(_ context.Context, currency string, limit int) ([]repo.AdjustmentRow, error) {
	if err := f.err("ListAdjustments"); err != nil {
		return nil, err
	}
	out := []repo.AdjustmentRow{}
	for i := len(f.adjustments) - 1; i >= 0 && len(out) < limit; i-- {
		x := f.adjustments[i]
		if currency != "" && x.Currency != currency {
			continue
		}
		reversed, _ := f.HasReversal(context.Background(), x.ID)
		out = append(out, repo.AdjustmentRow{
			ID: x.ID, Currency: x.Currency, Amount: x.Amount, Reason: x.Reason, EffectiveOn: x.EffectiveOn,
			ReversalOf: x.ReversalOf, CreatedBy: x.CreatedBy, CreatedAt: x.CreatedAt, Reversed: reversed,
		})
	}
	return out, nil
}

func (f *fakeStore) AdjustmentSumsByDay(_ context.Context, fromDay, toDay string) ([]repo.AdjustmentDayRow, error) {
	if err := f.err("AdjustmentSumsByDay"); err != nil {
		return nil, err
	}
	sums := map[[2]string]decimal.Decimal{}
	for _, x := range f.adjustments {
		if x.EffectiveOn < fromDay || x.EffectiveOn >= toDay {
			continue
		}
		k := [2]string{x.Currency, x.EffectiveOn}
		sums[k] = sums[k].Add(x.Amount)
	}
	out := []repo.AdjustmentDayRow{}
	for k, v := range sums {
		out = append(out, repo.AdjustmentDayRow{Currency: k[0], Day: k[1], Amount: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Currency != out[j].Currency {
			return out[i].Currency < out[j].Currency
		}
		return out[i].Day < out[j].Day
	})
	return out, nil
}

func (f *fakeStore) AdjustmentTotals(_ context.Context) ([]repo.AdjustmentTotalRow, error) {
	if err := f.err("AdjustmentTotals"); err != nil {
		return nil, err
	}
	sums := map[string]decimal.Decimal{}
	for _, x := range f.adjustments {
		sums[x.Currency] = sums[x.Currency].Add(x.Amount)
	}
	out := []repo.AdjustmentTotalRow{}
	for c, v := range sums {
		out = append(out, repo.AdjustmentTotalRow{Currency: c, Amount: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out, nil
}

func (f *fakeStore) UpsertActivity(_ context.Context, rows []model.ActivityDaily) error {
	if err := f.err("UpsertActivity"); err != nil {
		return err
	}
	f.upserts = append(f.upserts, rows)
	for _, r := range rows {
		f.activity[r.TZ+"|"+r.Day] = r
	}
	return nil
}

func (f *fakeStore) ActivityRows(_ context.Context, tz, fromDay, toDay string) ([]model.ActivityDaily, error) {
	if err := f.err("ActivityRows"); err != nil {
		return nil, err
	}
	out := []model.ActivityDaily{}
	for _, r := range f.activity {
		if r.TZ == tz && r.Day >= fromDay && r.Day < toDay {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

func (f *fakeStore) PurgeActivity(_ context.Context, beforeDay string, batch int) (int, error) {
	f.purgeCalls = append(f.purgeCalls, beforeDay)
	if err := f.err("PurgeActivity"); err != nil {
		return 0, err
	}
	n := min(batch, f.purgeLeft)
	f.purgeLeft -= n
	return n, nil
}

func (f *fakeStore) Ping(_ context.Context) error { return f.err("Ping") }

func (f *fakeStore) DatabaseStats(_ context.Context) (repo.DatabaseStatsRow, error) {
	if err := f.err("DatabaseStats"); err != nil {
		return repo.DatabaseStatsRow{}, err
	}
	return f.dbStats, nil
}

// ---------------------------------------------------------------------------
// fakeTx / fakeAudit
// ---------------------------------------------------------------------------

type fakeTx struct{ calls int }

func (f *fakeTx) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	f.calls++
	return fn(ctx)
}

type fakeAudit struct {
	events []auditsvc.Event
	err    error
}

func (f *fakeAudit) Write(_ context.Context, e auditsvc.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

// ---------------------------------------------------------------------------
// fakePorts：一个结构体实现全部属主模块端口
// ---------------------------------------------------------------------------

type fakePorts struct {
	tz string

	userCounts identitysvc.ReportUserCounts
	emails     map[uuid.UUID]string

	subCounts    subsvc.ReportSubscriptionCounts
	owners       map[uuid.UUID]uuid.UUID
	ownerBatches []int // 每次 OwnersBySubscription 收到的 ids 个数
	activeUsers  []subsvc.ActiveUsersDay

	nodeCounts     nodesvc.ReportNodeCounts
	offline        nodesvc.OfflineNodes
	configLagging  int64
	nodeTraffic    []nodesvc.NodeTraffic
	subTraffic     []nodesvc.SubscriptionTraffic
	trafficQuality nodesvc.TrafficQuality
	trafficCalls   [][2]time.Time // TrafficByNode / TrafficBySubscription 收到的 [from, to)

	revenueDays   []ledgersvc.RevenueDay
	revenueTotals []ledgersvc.RevenueTotal
	revenueCalls  [][2]time.Time
	drift         int64

	orderCounts ordersvc.ReportOrderCounts
	stuck       int64
	withdrawals []commissionsvc.PendingWithdrawalSum
	tickets     supportsvc.OpenTicketStats
	backlog     notifysvc.DeliveryBacklog
	channels    map[string]notifysvc.ChannelDeliveryCounts

	activityDays  []auditsvc.ActivityDay
	activityCalls [][2]time.Time
	actions       []auditsvc.ActivityActions

	errFor map[string]error
}

func newFakePorts() *fakePorts {
	return &fakePorts{
		tz:       "Asia/Shanghai",
		emails:   map[uuid.UUID]string{},
		owners:   map[uuid.UUID]uuid.UUID{},
		channels: map[string]notifysvc.ChannelDeliveryCounts{},
		errFor:   map[string]error{},
	}
}

func (f *fakePorts) err(name string) error { return f.errFor[name] }

func (f *fakePorts) Site(context.Context) (settingssvc.Site, error) {
	return settingssvc.Site{Timezone: f.tz, Currency: "CNY"}, f.err("Site")
}

func (f *fakePorts) ReportUserCounts(context.Context, time.Time, time.Time) (identitysvc.ReportUserCounts, error) {
	return f.userCounts, f.err("ReportUserCounts")
}

func (f *fakePorts) Emails(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if err := f.err("Emails"); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		if e, ok := f.emails[id]; ok {
			out[id] = e
		}
	}
	return out, nil
}

func (f *fakePorts) ReportSubscriptionCounts(context.Context, time.Time, time.Time, time.Time) (subsvc.ReportSubscriptionCounts, error) {
	return f.subCounts, f.err("ReportSubscriptionCounts")
}

func (f *fakePorts) OwnersBySubscription(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	if err := f.err("OwnersBySubscription"); err != nil {
		return nil, err
	}
	f.ownerBatches = append(f.ownerBatches, len(ids))
	out := map[uuid.UUID]uuid.UUID{}
	for _, id := range ids {
		if u, ok := f.owners[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

func (f *fakePorts) ActiveUsersByDay(context.Context, time.Time, time.Time) ([]subsvc.ActiveUsersDay, error) {
	return f.activeUsers, f.err("ActiveUsersByDay")
}

func (f *fakePorts) ReportNodeCounts(context.Context, time.Time) (nodesvc.ReportNodeCounts, error) {
	return f.nodeCounts, f.err("ReportNodeCounts")
}

func (f *fakePorts) OfflineNodes(context.Context, time.Time, int) (nodesvc.OfflineNodes, error) {
	return f.offline, f.err("OfflineNodes")
}

func (f *fakePorts) ConfigLaggingCount(context.Context) (int64, error) {
	return f.configLagging, f.err("ConfigLaggingCount")
}

func (f *fakePorts) TrafficByNode(_ context.Context, from, to time.Time) ([]nodesvc.NodeTraffic, error) {
	f.trafficCalls = append(f.trafficCalls, [2]time.Time{from, to})
	return f.nodeTraffic, f.err("TrafficByNode")
}

func (f *fakePorts) TrafficBySubscription(_ context.Context, from, to time.Time) ([]nodesvc.SubscriptionTraffic, error) {
	f.trafficCalls = append(f.trafficCalls, [2]time.Time{from, to})
	return f.subTraffic, f.err("TrafficBySubscription")
}

func (f *fakePorts) TrafficQuality(context.Context, time.Time, time.Time) (nodesvc.TrafficQuality, error) {
	return f.trafficQuality, f.err("TrafficQuality")
}

func (f *fakePorts) RevenueByDay(_ context.Context, from, to time.Time) ([]ledgersvc.RevenueDay, error) {
	f.revenueCalls = append(f.revenueCalls, [2]time.Time{from, to})
	return f.revenueDays, f.err("RevenueByDay")
}

func (f *fakePorts) RevenueTotals(context.Context) ([]ledgersvc.RevenueTotal, error) {
	return f.revenueTotals, f.err("RevenueTotals")
}

func (f *fakePorts) DriftAccounts(context.Context) (int64, error) {
	return f.drift, f.err("DriftAccounts")
}

func (f *fakePorts) ReportOrderCounts(context.Context, time.Time, time.Time) (ordersvc.ReportOrderCounts, error) {
	return f.orderCounts, f.err("ReportOrderCounts")
}

func (f *fakePorts) StuckCallbackCount(context.Context, time.Time) (int64, error) {
	return f.stuck, f.err("StuckCallbackCount")
}

func (f *fakePorts) PendingWithdrawalSums(context.Context) ([]commissionsvc.PendingWithdrawalSum, error) {
	return f.withdrawals, f.err("PendingWithdrawalSums")
}

func (f *fakePorts) OpenTicketStats(context.Context, time.Time) (supportsvc.OpenTicketStats, error) {
	return f.tickets, f.err("OpenTicketStats")
}

func (f *fakePorts) DeliveryBacklog(context.Context, time.Time) (notifysvc.DeliveryBacklog, error) {
	return f.backlog, f.err("DeliveryBacklog")
}

func (f *fakePorts) DeliveryCountsByChannel(context.Context) (map[string]notifysvc.ChannelDeliveryCounts, error) {
	return f.channels, f.err("DeliveryCountsByChannel")
}

func (f *fakePorts) ActivityByDay(_ context.Context, from, to time.Time, a auditsvc.ActivityActions) ([]auditsvc.ActivityDay, error) {
	f.activityCalls = append(f.activityCalls, [2]time.Time{from, to})
	f.actions = append(f.actions, a)
	return f.activityDays, f.err("ActivityByDay")
}

// fakeRedis 是 Redis 端口。
type fakeRedis struct{ err error }

func (f fakeRedis) Ping(context.Context) error { return f.err }

// testEnv 是一套装好假实现的 service。
type testEnv struct {
	svc   *Service
	store *fakeStore
	ports *fakePorts
	audit *fakeAudit
	tx    *fakeTx
	redis *fakeRedis
}

// newTestEnv 构造 service；时钟钉在 testNow，备份目录默认空（用例自己用 t.TempDir() 改 svc.opt.BackupDir）。
func newTestEnv() *testEnv {
	st := newFakeStore()
	p := newFakePorts()
	au := &fakeAudit{}
	tx := &fakeTx{}
	rd := &fakeRedis{}
	deps := Deps{
		Settings: p, Users: p, Subscriptions: p, Nodes: p, Ledger: p, Orders: p, Payments: p,
		Commissions: p, Tickets: p, Notices: p, Activity: p, Redis: rd,
	}
	svc := newService(st, tx, au, deps, Options{})
	svc.now = func() time.Time { return testNow }
	return &testEnv{svc: svc, store: st, ports: p, audit: au, tx: tx, redis: rd}
}

// dec 把字符串解析成 decimal，测试里写金额用。
func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
