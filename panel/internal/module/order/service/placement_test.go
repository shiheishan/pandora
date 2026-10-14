package service

import (
	"crypto/sha256"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// fixedID 给测试里的名字一个固定的 uuid（同名同值）。
func fixedID(name string) uuid.UUID {
	sum := sha256.Sum256([]byte(name))
	id, _ := uuid.FromBytes(sum[:16])
	return id
}

func onDay(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

func cand(name, plan string, st subsvc.State, end time.Time) candidate {
	return candidate{Sub: subsvc.Subscription{ID: fixedID(name), PlanID: fixedID(plan), PeriodEnd: end}, State: st}
}

// pkey 把「kind:名字」写成真实 Key。
func pkey(kind, name string) string {
	if kind == PlacementNew {
		return PlacementNew
	}
	id := fixedID(name)
	return placementKey(kind, &id)
}

// 后台开单落点（套餐卡 / 后台开单的用例）。
func TestPlanPlacements(t *testing.T) {
	mine := cand("mine", "std", subsvc.StateLive, onDay(10, 30))
	mom := cand("mom", "basic", subsvc.StateLive, onDay(10, 12))
	single := cand("one", "std", subsvc.StateLive, onDay(10, 19))
	expiredStd := cand("exp", "std", subsvc.StateRevivable, onDay(10, 4))
	deadStd := cand("dead", "std", subsvc.StateEnded, onDay(8, 1))
	liveA := cand("liveA", "std", subsvc.StateLive, onDay(11, 1))
	liveB := cand("liveB", "std", subsvc.StateLive, onDay(10, 20))
	revC := cand("revC", "std", subsvc.StateRevivable, onDay(10, 1))
	revD := cand("revD", "std", subsvc.StateRevivable, onDay(9, 20))
	expiredBasic := cand("expBasic", "basic", subsvc.StateRevivable, onDay(10, 2))

	type k = [2]string
	cases := []struct {
		name    string
		plan    string
		cands   []candidate
		entry   string
		want    []k
		def     k
		badge   []k
		expired []k
	}{
		{name: "同款只有一份：不问直接续", plan: "std", cands: []candidate{single},
			want: []k{{"renew", "one"}}, def: k{"renew", "one"}},
		{name: "同款只有一份已过期：只给续", plan: "std", cands: []candidate{expiredStd},
			want: []k{{"renew", "exp"}}, def: k{"renew", "exp"}},
		{name: "不同款只有一份：另开与换掉都不预选", plan: "adv", cands: []candidate{single},
			want: []k{{"new", ""}, {"change", "one"}}},
		{name: "不同款只有一份已过期：恢复并改成排在另开前", plan: "adv", cands: []candidate{expiredStd},
			want: []k{{"change", "exp"}, {"new", ""}}, expired: []k{{"change", "exp"}}},
		{name: "没有任何订阅：只剩新开", plan: "std", cands: nil, want: []k{{"new", ""}}, def: k{"new", ""}},
		{name: "只有停用的：等于没有", plan: "std", cands: []candidate{deadStd}, want: []k{{"new", ""}}, def: k{"new", ""}},
		{name: "停用的不算一份：同款仍只返回续", plan: "std", cands: []candidate{deadStd, single},
			want: []k{{"renew", "one"}}, def: k{"renew", "one"}},
		{name: "同款两份：续同款预选、再开、换掉别的", plan: "std", cands: []candidate{mom, mine},
			want: []k{{"renew", "mine"}, {"new", ""}, {"change", "mom"}}, def: k{"renew", "mine"}, badge: []k{{"renew", "mine"}}},
		{name: "不同款两份：不预选，换掉的按到期从早到晚", plan: "adv", cands: []candidate{mine, mom},
			want: []k{{"new", ""}, {"change", "mom"}, {"change", "mine"}}},
		{name: "同款续的次序：生效中早到晚，再过期的晚到早", plan: "std", cands: []candidate{revD, liveA, revC, liveB},
			want: []k{{"renew", "liveB"}, {"renew", "liveA"}, {"renew", "revC"}, {"renew", "revD"}, {"new", ""}},
			def:  k{"renew", "liveB"}, badge: []k{{"renew", "liveB"}}},
		{name: "完整次序：同款、不同款过期、新开、不同款生效中", plan: "std", cands: []candidate{mom, expiredBasic, revC, mine},
			want: []k{{"renew", "mine"}, {"renew", "revC"}, {"change", "expBasic"}, {"new", ""}, {"change", "mom"}},
			def:  k{"renew", "mine"}, badge: []k{{"renew", "mine"}}, expired: []k{{"change", "expBasic"}}},
		{name: "从订阅行进来：预选入口那份（不同款生效中）", plan: "adv", cands: []candidate{mine, mom}, entry: "mine",
			want: []k{{"new", ""}, {"change", "mom"}, {"change", "mine"}}, def: k{"change", "mine"}},
		{name: "从订阅行进来：同款多份时预选入口那份", plan: "std", cands: []candidate{liveA, liveB}, entry: "liveA",
			want: []k{{"renew", "liveB"}, {"renew", "liveA"}, {"new", ""}}, def: k{"renew", "liveA"}, badge: []k{{"renew", "liveB"}}},
		{name: "入口是停用的那份：不在选项里，按规则取默认", plan: "adv", cands: []candidate{deadStd, mine}, entry: "dead",
			want: []k{{"new", ""}, {"change", "mine"}}},
	}
	keyOf := func(x k) string {
		if x[0] == "" {
			return ""
		}
		return pkey(x[0], x[1])
	}
	for _, tc := range cases {
		var entry *uuid.UUID
		if tc.entry != "" {
			id := fixedID(tc.entry)
			entry = &id
		}
		opts, def := planPlacements(fixedID(tc.plan), tc.cands, entry)
		got := make([]string, 0, len(opts))
		badges := map[string]string{}
		expired := map[string]bool{}
		for _, o := range opts {
			got = append(got, o.Key)
			if o.Badge != "" {
				badges[o.Key] = o.Badge
			}
			if o.Expired {
				expired[o.Key] = true
			}
		}
		want := make([]string, 0, len(tc.want))
		for _, w := range tc.want {
			want = append(want, keyOf(w))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: keys=%v want %v", tc.name, got, want)
		}
		if def != keyOf(tc.def) {
			t.Errorf("%s: default=%q want %q", tc.name, def, keyOf(tc.def))
		}
		wantBadges := map[string]string{}
		for _, b := range tc.badge {
			wantBadges[keyOf(b)] = BadgeSamePlan
		}
		if !reflect.DeepEqual(badges, wantBadges) {
			t.Errorf("%s: badges=%v want %v", tc.name, badges, wantBadges)
		}
		wantExpired := map[string]bool{}
		for _, e := range tc.expired {
			wantExpired[keyOf(e)] = true
		}
		if !reflect.DeepEqual(expired, wantExpired) {
			t.Errorf("%s: expired=%v want %v", tc.name, expired, wantExpired)
		}
	}
}

// 选定落点：给了选择按 Key 找；没给且只有一个就用它；多个没选、选了不存在的都回 422（落在 target 上）。
func TestResolvePlacement(t *testing.T) {
	one := fixedID("one")
	opts := []Placement{{Key: PlacementNew, Kind: PlacementNew}, {Key: placementKey(PlacementChange, &one), Kind: PlacementChange, SubscriptionID: &one}}
	got, err := resolvePlacement(&Choice{Kind: PlacementChange, SubscriptionID: &one}, opts)
	if err != nil || got.Kind != PlacementChange {
		t.Fatalf("got %+v err %v", got, err)
	}
	if got, err := resolvePlacement(&Choice{Kind: PlacementNew}, opts); err != nil || got.Kind != PlacementNew {
		t.Fatalf("new: got %+v err %v", got, err)
	}
	if _, err := resolvePlacement(nil, opts); err != errChoiceRequired {
		t.Fatalf("多个选项没选应 422，得到 %v", err)
	}
	other := fixedID("other")
	if _, err := resolvePlacement(&Choice{Kind: PlacementRenew, SubscriptionID: &other}, opts); err != errChoiceStale {
		t.Fatalf("不存在的选项应 422，得到 %v", err)
	}
	if got, err := resolvePlacement(nil, opts[:1]); err != nil || got.Kind != PlacementNew {
		t.Fatalf("只有一个选项: got %+v err %v", got, err)
	}
}

// 候选：停用的不取，生效中的优先、新建的优先；本期套餐流量只算 cycle / total 且有上限的 traffic.bytes。
func TestCandidatesFrom(t *testing.T) {
	limit := int64(100)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	sum := func(name string, st subsvc.State, created time.Time) subsvc.Summary {
		return subsvc.Summary{Subscription: subsvc.Subscription{ID: fixedID(name), CreatedAt: created}, State: st, Quotas: []subsvc.Quota{}}
	}
	live := sum("live", subsvc.StateLive, t0)
	live.Quotas = []subsvc.Quota{
		{Metric: "traffic.bytes", Period: "cycle", Limit: &limit, Consumed: 30},
		{Metric: "traffic.bytes", Period: "day", Limit: &limit, Consumed: 99},
		{Metric: "devices.active", Period: "total", Limit: &limit, Consumed: 1},
	}
	live.PackRemainingBytes = 7
	us := subsvc.UserSubscriptions{Subscriptions: []subsvc.Summary{
		sum("old-rev", subsvc.StateRevivable, t0.Add(-time.Hour)),
		sum("ended", subsvc.StateEnded, t0.Add(time.Hour)),
		live,
		sum("new-live", subsvc.StateLive, t0.Add(time.Minute)),
	}}
	got := candidatesFrom(us)
	order := []uuid.UUID{fixedID("new-live"), fixedID("live"), fixedID("old-rev")}
	if len(got) != len(order) {
		t.Fatalf("候选 %d 份", len(got))
	}
	for i, id := range order {
		if got[i].Sub.ID != id {
			t.Fatalf("第 %d 份是 %s", i, got[i].Sub.ID)
		}
	}
	if got[1].TrafficUsed != 30 || got[1].TrafficCap != 100 || got[1].PackRemaining != 7 {
		t.Fatalf("流量 %+v", got[1])
	}
}
