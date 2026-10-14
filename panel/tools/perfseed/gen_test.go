package main

import (
	"crypto/sha256"
	"regexp"
	"testing"
	"time"
)

func testIssuer(subscriptionID string) (Credential, error) {
	tok := "tok-" + subscriptionID
	h := sha256.Sum256([]byte(tok))
	return Credential{
		Token:  tok,
		Hash:   h[:],
		Prefix: tok[:8],
		Sealed: []byte("sealed"),
	}, nil
}

func basePlans() []PlanSpec {
	return []PlanSpec{
		{
			PlanID: "plan-a", VersionID: "ver-a", PriceID: "price-a",
			Name: "Plan A", VersionNo: 1, Currency: "CNY", Amount: "15.00",
			Interval: "month", IntervalCount: 1,
			Quotas: []QuotaDef{{
				Metric: "traffic.bytes", Limit: int64Ptr(200e9), Unit: "bytes", Period: "cycle",
			}},
			EntitlementsJSON: "[]", QuotasJSON: "[]",
		},
		{
			PlanID: "plan-b", VersionID: "ver-b", PriceID: "price-b",
			Name: "Plan B", VersionNo: 1, Currency: "CNY", Amount: "30.00",
			Interval: "month", IntervalCount: 1, Quotas: nil,
			EntitlementsJSON: "[]", QuotasJSON: "[]",
		},
	}
}

func baseGenInput(now time.Time, loc *time.Location) GenInput {
	expireAt := now.Add(2 * time.Hour)
	return GenInput{
		Now: now, Loc: loc, Users: 50, ExpiredCount: 5, ExpireCount: 3,
		ExpireAt: &expireAt, HistoryDays: 30, Seed: 1,
		GroupID: "grp-1", OfflineProviderID: "prov-offline", Plans: basePlans(),
	}
}

func TestGenerateMain(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	in := baseGenInput(now, time.UTC)
	seed, err := Generate(in, testIssuer)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(seed.Users) != 50 || len(seed.Subscriptions) != 50 || len(seed.Credentials) != 50 || len(seed.CSV) != 50 {
		t.Fatalf("行数应为 50")
	}
	for i := 0; i < 50; i++ {
		want := userEmail(i + 1)
		if seed.Users[i].Email != want || seed.CSV[i].Email != want || seed.CSV[i].UserID != seed.Users[i].ID {
			t.Fatalf("用户 %d 邮箱或 CSV 顺序不对", i)
		}
	}

	orderNoRe := regexp.MustCompile(`^[A-Z0-9-]{1,64}$`)
	seenOrderNo := map[string]bool{}
	seenProvPay := map[string]bool{}
	seenProvEvt := map[string]bool{}

	newPerSub := map[string]int{}
	renewPerSub := map[string]int{}
	renewalOrdersPlanA := 0

	for _, o := range seed.Orders {
		if !orderNoRe.MatchString(o.OrderNo) || seenOrderNo[o.OrderNo] {
			t.Fatalf("order_no 非法或重复: %s", o.OrderNo)
		}
		seenOrderNo[o.OrderNo] = true
		if o.Kind == "new" {
			newPerSub[o.SubscriptionID]++
		} else if o.Kind == "renewal" {
			renewPerSub[o.SubscriptionID]++
		}
		if !o.PaidAt.Before(now.Add(-3 * time.Hour)) {
			t.Fatalf("PaidAt 须早于 now-3h: %s", o.OrderNo)
		}
		if !o.CreatedAt.Before(o.PaidAt) {
			t.Fatalf("CreatedAt < PaidAt: %s", o.OrderNo)
		}
		if o.ExpiresAt != o.CreatedAt.Add(30*time.Minute) {
			t.Fatalf("ExpiresAt: %s", o.OrderNo)
		}
	}
	if len(seed.Orders) != len(seed.OrderItems) || len(seed.Orders) != len(seed.Reservations) ||
		len(seed.Orders) != len(seed.Payments) || len(seed.Orders) != len(seed.PaymentEvents) ||
		len(seed.Orders) != len(seed.LedgerTxns) {
		t.Fatalf("订单相关六组行数不等")
	}
	for _, p := range seed.Payments {
		if seenProvPay[p.ProviderPaymentID] {
			t.Fatalf("ProviderPaymentID 重复")
		}
		seenProvPay[p.ProviderPaymentID] = true
	}
	for _, e := range seed.PaymentEvents {
		if seenProvEvt[e.ProviderEventID] {
			t.Fatalf("ProviderEventID 重复")
		}
		seenProvEvt[e.ProviderEventID] = true
	}

	minDay := now.AddDate(0, 0, -31)
	usageKey := map[string]bool{}
	usageSumBySub := map[string]int64{}

	for _, u := range seed.Usage {
		key := u.SubscriptionID + "|" + u.Day.Format(time.RFC3339)
		if usageKey[key] {
			t.Fatalf("用量 (订阅, Day) 重复")
		}
		usageKey[key] = true
		if u.Bytes < 1 {
			t.Fatalf("Bytes < 1")
		}
		if u.Day.Before(dayOf(minDay, time.UTC)) {
			t.Fatalf("Day 过早")
		}
		usageSumBySub[u.SubscriptionID] += u.Bytes
	}

	activated := map[string]int{}
	renewed := map[string]int{}
	expiredEv := map[string]int{}

	for i, sub := range seed.Subscriptions {
		if newPerSub[sub.ID] != 1 {
			t.Fatalf("订阅 %s 应恰好一张 new 订单", sub.ID)
		}
		isExpired := i >= 45
		if isExpired {
			if sub.Status != "expired" || !sub.PeriodEnd.Before(now.Add(-time.Hour)) {
				t.Fatalf("末 5 份应 expired 且 PeriodEnd <= now-1h")
			}
		} else {
			if sub.Status != "active" || !sub.PeriodEnd.After(now) || !sub.PeriodStart.Before(sub.PeriodEnd) {
				t.Fatalf("生效中订阅状态或周期不对 idx=%d", i)
			}
		}
		if i < 3 {
			if sub.PeriodEnd != *in.ExpireAt {
				t.Fatalf("前 3 份 PeriodEnd 应等于 ExpireAt")
			}
		}
		if in.Plans[i%2].PlanID == "plan-a" {
			renewalOrdersPlanA += renewPerSub[sub.ID]
		}
	}

	for _, q := range seed.Quotas {
		if q.Metric != "traffic.bytes" {
			continue
		}
		// plan A 才有配额
		if q.Limit == nil || *q.Limit != int64(200e9) {
			continue
		}
		sub := findSub(seed.Subscriptions, q.SubscriptionID)
		if sub.Status == "active" {
			if q.PeriodStart.After(now) {
				t.Fatalf("active 配额 PeriodStart 应 <= now")
			}
			if q.PeriodEnd == nil || *q.PeriodEnd != sub.PeriodEnd {
				t.Fatalf("cycle PeriodEnd 应等于订阅 PeriodEnd")
			}
			if q.Limit != nil && q.Consumed >= *q.Limit {
				t.Fatalf("Consumed 应 < Limit")
			}
			if q.Consumed > usageSumBySub[q.SubscriptionID] {
				t.Fatalf("Consumed 不应超过用量之和")
			}
		}
	}
	quotaPlanA := 0
	for _, q := range seed.Quotas {
		sub := findSub(seed.Subscriptions, q.SubscriptionID)
		if sub.PlanID == "plan-a" {
			quotaPlanA++
		}
		if sub.PlanID == "plan-b" {
			t.Fatalf("plan B 订阅不应有配额行")
		}
	}
	if quotaPlanA != 25 {
		t.Fatalf("plan A 订阅每份恰好一行配额，得 %d", quotaPlanA)
	}

	for _, ev := range seed.SubEvents {
		switch ev.EventType {
		case "activated":
			activated[ev.SubscriptionID]++
		case "renewed":
			renewed[ev.SubscriptionID]++
		case "expired":
			expiredEv[ev.SubscriptionID]++
		}
	}
	for _, sub := range seed.Subscriptions {
		if activated[sub.ID] != 1 {
			t.Fatalf("activated 应为 1")
		}
		if renewed[sub.ID] != renewPerSub[sub.ID] {
			t.Fatalf("renewed 条数应等于 renewal 订单数")
		}
		if sub.Status == "expired" {
			if expiredEv[sub.ID] != 1 {
				t.Fatalf("expired 订阅应有 1 条 expired 事件")
			}
		} else if expiredEv[sub.ID] != 0 {
			t.Fatalf("active 订阅不应有 expired 事件")
		}
	}

	if len(seed.ResetLogs) != renewalOrdersPlanA {
		t.Fatalf("重置日志条数 %d != plan A renewal 总数 %d", len(seed.ResetLogs), renewalOrdersPlanA)
	}

	seed2, err := Generate(in, testIssuer)
	if err != nil {
		t.Fatalf("第二次 Generate: %v", err)
	}
	for i := range seed.Subscriptions {
		if seed.Subscriptions[i].PeriodStart != seed2.Subscriptions[i].PeriodStart ||
			seed.Subscriptions[i].PeriodEnd != seed2.Subscriptions[i].PeriodEnd {
			t.Fatalf("同 Seed 订阅周期应相同 user=%d", i)
		}
	}
	if len(seed.Usage) != len(seed2.Usage) {
		t.Fatalf("用量行数应相同")
	}
	for i := range seed.Usage {
		if seed.Usage[i].Bytes != seed2.Usage[i].Bytes {
			t.Fatalf("同 Seed 用量 Bytes 应相同 idx=%d", i)
		}
	}
}

func findSub(subs []SubscriptionRow, id string) SubscriptionRow {
	for _, s := range subs {
		if s.ID == id {
			return s
		}
	}
	return SubscriptionRow{}
}

func TestGenerateValidation(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	base := baseGenInput(now, time.UTC)

	bad := base
	bad.ExpireCount = 0
	bad.ExpireAt = ptrTime(now.Add(2 * time.Hour))
	if _, err := Generate(bad, testIssuer); err == nil {
		t.Fatal("ExpireAt 有而 ExpireCount 0 应报错")
	}

	bad2 := base
	bad2.ExpiredCount = bad2.Users
	if _, err := Generate(bad2, testIssuer); err == nil {
		t.Fatal("ExpiredCount == Users 应报错")
	}

	bad3 := base
	exp := now.Add(5 * time.Minute)
	bad3.ExpireAt = &exp
	bad3.ExpireCount = 1
	if _, err := Generate(bad3, testIssuer); err == nil {
		t.Fatal("ExpireAt 太近应报错")
	}

	bad4 := base
	bad4.Plans = []PlanSpec{{
		PlanID: "p", VersionID: "v", PriceID: "pr", Name: "n", VersionNo: 1,
		Currency: "CNY", Amount: "1.00", Interval: "year", IntervalCount: 1,
		EntitlementsJSON: "[]", QuotasJSON: "[]",
	}}
	if _, err := Generate(bad4, testIssuer); err == nil {
		t.Fatal("Interval year 应报错")
	}

	bad5 := base
	bad5.Plans = []PlanSpec{{
		PlanID: "p", VersionID: "v", PriceID: "pr", Name: "n", VersionNo: 1,
		Currency: "CNY", Amount: "1.00", Interval: "month", IntervalCount: 1,
		Quotas:           []QuotaDef{{Metric: "m", Period: "day"}},
		EntitlementsJSON: "[]", QuotasJSON: "[]",
	}}
	if _, err := Generate(bad5, testIssuer); err == nil {
		t.Fatal("配额 Period day 应报错")
	}
}

func TestGenerateFixedZone(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	loc := time.FixedZone("CST", 8*3600)
	in := baseGenInput(now, loc)
	if _, err := Generate(in, testIssuer); err != nil {
		t.Fatalf("CST: %v", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
