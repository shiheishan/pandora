package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func rowByID(rows []UserRow, id uuid.UUID) *UserRow {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

func idsContainAll(got []uuid.UUID, want ...uuid.UUID) bool {
	set := make(map[uuid.UUID]bool, len(got))
	for _, id := range got {
		set[id] = true
	}
	for _, id := range want {
		if !set[id] {
			return false
		}
	}
	return true
}

func TestPGListUsersEnrich(t *testing.T) {
	e, ctx, fakes := adminEnv(t)
	actx := adminCtx(t, e, ctx)
	tag := bulkTag(t)

	ua := e.mkUser(t, ctx, uniqEmail(t, tag+"-a"), "Passw0rdLong1")
	ub := e.mkUser(t, ctx, uniqEmail(t, tag+"-b"), "Passw0rdLong1")
	uc := e.mkUser(t, ctx, uniqEmail(t, tag+"-c"), "Passw0rdLong1")

	liveID := uuid.New()
	expiredID := uuid.New()
	liveSub := AdminSub{ID: liveID, PlanName: "套餐Live", Live: true, Status: "active"}
	expiredSub := AdminSub{ID: expiredID, PlanName: "套餐旧", Live: false, Status: "expired"}
	fakes.subs.admin[ua.ID] = AdminUserSubs{
		Subscriptions: []AdminSub{liveSub, expiredSub},
		Current:       &liveSub,
	}

	onlyExpired := AdminSub{ID: uuid.New(), PlanName: "过期套餐", Live: false, Status: "expired"}
	fakes.subs.admin[ub.ID] = AdminUserSubs{
		Subscriptions: []AdminSub{onlyExpired},
		Current:       &onlyExpired,
	}

	fakes.wallets.bal[ua.ID] = decimal.RequireFromString("12.34")

	rows, total, err := e.svc.ListUsers(actx, UserFilter{Query: tag}, 50, 0)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("list total=%d rows=%d err=%v", total, len(rows), err)
	}
	if fakes.subs.adminCalls != 1 || fakes.wallets.calls != 1 {
		t.Fatalf("calls subs=%d wallets=%d", fakes.subs.adminCalls, fakes.wallets.calls)
	}
	if len(fakes.subs.adminIDs) != 1 || !idsContainAll(fakes.subs.adminIDs[0], ua.ID, ub.ID, uc.ID) {
		t.Fatalf("adminIDs %+v", fakes.subs.adminIDs)
	}

	ra := rowByID(rows, ua.ID)
	if ra == nil || ra.SubscriptionCount != 2 || ra.ActivePlan == nil || *ra.ActivePlan != "套餐Live" {
		t.Fatalf("user A subs/plan %+v", ra)
	}
	if ra.Current == nil || ra.Current.ID != liveID {
		t.Fatalf("user A current %+v", ra.Current)
	}
	if !ra.Balance.Equal(decimal.RequireFromString("12.34")) || ra.Currency != "CNY" {
		t.Fatalf("user A balance %s %s", ra.Balance, ra.Currency)
	}

	rb := rowByID(rows, ub.ID)
	if rb == nil || rb.SubscriptionCount != 1 || rb.ActivePlan != nil || rb.Current == nil {
		t.Fatalf("user B %+v", rb)
	}

	rc := rowByID(rows, uc.ID)
	if rc == nil || rc.SubscriptionCount != 0 || rc.ActivePlan != nil || rc.Current != nil {
		t.Fatalf("user C subs/current %+v", rc)
	}
	if !rc.Balance.Equal(decimal.Zero) {
		t.Fatalf("user C balance %s", rc.Balance)
	}

	_, _, err = e.svc.ListUsers(actx, UserFilter{Query: tag}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fakes.subs.adminCalls != 2 {
		t.Fatalf("第二页后 adminCalls=%d want 2", fakes.subs.adminCalls)
	}
}

func TestPGUserDetailEnrich(t *testing.T) {
	e, ctx, fakes := adminEnv(t)
	actx := adminCtx(t, e, ctx)

	u := e.mkUser(t, ctx, uniqEmail(t, "detail-enrich"), "Passw0rdLong1")
	s1 := AdminSub{ID: uuid.New(), PlanName: "详单套餐1", Live: true, Status: "active"}
	s2 := AdminSub{ID: uuid.New(), PlanName: "详单套餐2", Live: false, Status: "expired"}
	fakes.subs.admin[u.ID] = AdminUserSubs{
		Subscriptions:       []AdminSub{s1, s2},
		Current:             &s1,
		UnattachedPackBytes: 5,
	}
	fakes.orders.stats = map[uuid.UUID]OrderStat{
		u.ID: {
			Orders: 3,
			Paid: map[string]decimal.Decimal{
				"USD": decimal.RequireFromString("10"),
				"CNY": decimal.RequireFromString("20"),
				"EUR": decimal.Zero,
			},
		},
	}
	fakes.orders.recent = map[uuid.UUID][]AdminOrderRow{
		u.ID: {
			{ID: uuid.New(), OrderNo: "o1"},
			{ID: uuid.New(), OrderNo: "o2"},
		},
	}
	boundAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fakes.telegram.refs[u.ID] = &TelegramRef{Username: "tg_user", BoundAt: boundAt}

	d, err := e.svc.UserDetail(actx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Subscriptions) != 2 || d.UnattachedPackBytes != 5 || d.OrderCount != 3 {
		t.Fatalf("subs/pack/orders %+v", d)
	}
	if len(d.PaidTotals) != 2 || d.PaidTotals[0].Currency != "CNY" || !d.PaidTotals[0].Amount.Equal(decimal.RequireFromString("20")) {
		t.Fatalf("paid %+v", d.PaidTotals)
	}
	if d.PaidTotals[1].Currency != "USD" || !d.PaidTotals[1].Amount.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("paid USD %+v", d.PaidTotals)
	}
	if len(d.RecentOrders) != 2 {
		t.Fatalf("recent %d", len(d.RecentOrders))
	}
	if fakes.orders.lastLimit != 20 {
		t.Fatalf("lastLimit %d", fakes.orders.lastLimit)
	}
	if d.Telegram == nil || d.Telegram.Username != "tg_user" {
		t.Fatalf("telegram %+v", d.Telegram)
	}
	if fakes.subs.adminCalls != 1 || fakes.wallets.calls != 1 || fakes.orders.recentCalls != 1 || fakes.telegram.calls != 1 {
		t.Fatalf("calls subs=%d wallets=%d orders=%d tg=%d",
			fakes.subs.adminCalls, fakes.wallets.calls, fakes.orders.recentCalls, fakes.telegram.calls)
	}

	empty := e.mkUser(t, ctx, uniqEmail(t, "detail-empty"), "Passw0rdLong1")
	d2, err := e.svc.UserDetail(actx, empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Subscriptions == nil || len(d2.Subscriptions) != 0 {
		t.Fatalf("empty subs %v", d2.Subscriptions)
	}
	if d2.RecentOrders == nil || len(d2.RecentOrders) != 0 {
		t.Fatalf("empty recent %v", d2.RecentOrders)
	}
	if d2.PaidTotals == nil || len(d2.PaidTotals) != 0 {
		t.Fatalf("empty paid %v", d2.PaidTotals)
	}
	if d2.Telegram != nil {
		t.Fatalf("empty telegram %+v", d2.Telegram)
	}
}

func TestPGUserDetailPortsMissing(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)
	u := e.mkUser(t, ctx, uniqEmail(t, "ports-miss"), "Passw0rdLong1")
	_, err := e.svc.UserDetail(actx, u.ID)
	if err == nil {
		t.Fatal("期望缺端口时报错")
	}
}

func TestPGAdminListGroups(t *testing.T) {
	e, ctx, fakes := adminEnv(t)
	actx := adminCtx(t, e, ctx)

	g1, err := e.svc.CreateGroup(actx, "", "组一", "")
	if err != nil {
		t.Fatal(err)
	}
	g2, err := e.svc.CreateGroup(actx, "", "组二", "")
	if err != nil {
		t.Fatal(err)
	}
	poolID := uuid.New()
	fakes.groups.usage[g1] = GroupUsage{
		Plans:          2,
		Prices:         1,
		Coupons:        3,
		ExclusivePools: []PoolRef{{ID: poolID, Name: "池A"}},
	}

	groups, err := e.svc.AdminListGroups(actx)
	if err != nil {
		t.Fatal(err)
	}
	if fakes.groups.calls != 1 {
		t.Fatalf("groups.calls %d", fakes.groups.calls)
	}

	var row1, row2 *Group
	for i := range groups {
		switch groups[i].ID {
		case g1:
			row1 = &groups[i]
		case g2:
			row2 = &groups[i]
		}
	}
	if row1 == nil || row1.Plans != 2 || row1.Prices != 1 || row1.Coupons != 3 {
		t.Fatalf("g1 usage %+v", row1)
	}
	if len(row1.ExclusivePools) != 1 || row1.ExclusivePools[0].Name != "池A" {
		t.Fatalf("g1 pools %+v", row1.ExclusivePools)
	}
	if row2 == nil || row2.Plans != 0 || row2.Prices != 0 || row2.Coupons != 0 {
		t.Fatalf("g2 counts %+v", row2)
	}
	if row2.ExclusivePools == nil || len(row2.ExclusivePools) != 0 {
		t.Fatalf("g2 pools %v", row2.ExclusivePools)
	}
}
