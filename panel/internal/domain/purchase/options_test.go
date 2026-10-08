package purchase

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

const gb = int64(1) << 30

func day(m time.Month, d int) *time.Time {
	t := time.Date(2026, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

// 原型场景里的几份订阅（数据照 proto/index.html 的 SCENARIOS）
var (
	// 小王「我的」：标准版，10月30日到期，100G 用了 93G
	mine = Candidate{SubscriptionID: "mine", PlanID: "std", PlanName: "标准版", Label: "我的",
		State: StateLive, PeriodEnd: day(10, 30), TrafficUsed: 93 * gb, TrafficCap: 100 * gb}
	// 小王「妈妈的 iPad」：基础版，10月12日到期，50G 用了 12G
	mom = Candidate{SubscriptionID: "mom", PlanID: "basic", PlanName: "基础版", Label: "妈妈的 iPad",
		State: StateLive, PeriodEnd: day(10, 12), TrafficUsed: 12 * gb, TrafficCap: 50 * gb}
	// S1：只有一份标准版
	single = Candidate{SubscriptionID: "one", PlanID: "std", PlanName: "标准版",
		State: StateLive, PeriodEnd: day(10, 19), TrafficUsed: 40 * gb, TrafficCap: 100 * gb}
	// S8：标准版已过期 3 天（过期 30 天内）
	expiredStd = Candidate{SubscriptionID: "exp", PlanID: "std", PlanName: "标准版",
		State: StateRevivable, PeriodEnd: day(10, 4), TrafficUsed: 47 * gb, TrafficCap: 100 * gb}
	// 已彻底停用的一份
	deadStd = Candidate{SubscriptionID: "dead", PlanID: "std", State: StateDead, PeriodEnd: day(8, 1)}
)

func with(c Candidate, f func(*Candidate)) Candidate {
	f(&c)
	return c
}

func keys(opts []Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Key)
	}
	return out
}

func badges(opts []Option) map[string]string {
	out := map[string]string{}
	for _, o := range opts {
		if o.Badge != "" {
			out[o.Key] = o.Badge
		}
	}
	return out
}

func TestOptions(t *testing.T) {
	// 同款可续的次序：生效中按到期从早到晚，再过期 30 天内按到期从晚到早
	liveA := with(single, func(c *Candidate) { c.SubscriptionID = "liveA"; c.PeriodEnd = day(11, 1) })
	liveB := with(single, func(c *Candidate) { c.SubscriptionID = "liveB"; c.PeriodEnd = day(10, 20) })
	revC := with(expiredStd, func(c *Candidate) { c.SubscriptionID = "revC"; c.PeriodEnd = day(10, 1) })
	revD := with(expiredStd, func(c *Candidate) { c.SubscriptionID = "revD"; c.PeriodEnd = day(9, 20) })
	expiredBasic := with(mom, func(c *Candidate) {
		c.SubscriptionID = "expBasic"
		c.State = StateRevivable
		c.PeriodEnd = day(10, 2)
	})
	noEnd := with(mom, func(c *Candidate) { c.SubscriptionID = "forever"; c.PeriodEnd = nil })
	unlimited := with(mom, func(c *Candidate) { c.SubscriptionID = "unl"; c.TrafficCap = 0; c.TrafficUsed = 900 * gb })
	minePack := with(mine, func(c *Candidate) { c.PackRemaining = 50 * gb })

	cases := []struct {
		name    string
		offer   Offer
		cands   []Candidate
		entry   string
		want    []string
		def     string
		badges  map[string]string
		expired []string
	}{
		// ---- 套餐卡 / 后台开单 ----
		{name: "S5a 同款 只有一份：不问 直接续", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{single}, want: []string{"renew:one"}, def: "renew:one", badges: map[string]string{}},
		{name: "S8 同款 只有一份已过期：只给续", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{expiredStd}, want: []string{"renew:exp"}, def: "renew:exp", badges: map[string]string{}},
		{name: "S5a 不同款 只有一份：另开 / 升级 都不预选", offer: Offer{Kind: OfferPlan, PlanID: "adv"},
			cands: []Candidate{single}, want: []string{"new", "change:one"}, def: "", badges: map[string]string{}},
		{name: "不同款 只有一份已过期：恢复并改成 排在另开前 不预选", offer: Offer{Kind: OfferPlan, PlanID: "adv"},
			cands: []Candidate{expiredStd}, want: []string{"change:exp", "new"}, def: "",
			badges: map[string]string{}, expired: []string{"change:exp"}},
		{name: "没有任何订阅：只剩新开 默认新开", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: nil, want: []string{"new"}, def: "new", badges: map[string]string{}},
		{name: "只有停用的：等于没有", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{deadStd}, want: []string{"new"}, def: "new", badges: map[string]string{}},
		{name: "停用的不算一份：同款仍只返回续", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{deadStd, single}, want: []string{"renew:one"}, def: "renew:one", badges: map[string]string{}},
		{name: "S5b 同款 两份：续同款（预选 同款） / 再开 / 换掉别的", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{mom, mine}, want: []string{"renew:mine", "new", "change:mom"}, def: "renew:mine",
			badges: map[string]string{"renew:mine": BadgeSamePlan}},
		{name: "S5b 不同款 两份：都不预选 换掉的按到期从早到晚", offer: Offer{Kind: OfferPlan, PlanID: "adv"},
			cands: []Candidate{mine, mom}, want: []string{"new", "change:mom", "change:mine"}, def: "",
			badges: map[string]string{}},
		{name: "同款续的次序：生效中早到晚 再过期的晚到早", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{revD, liveA, revC, liveB},
			want:  []string{"renew:liveB", "renew:liveA", "renew:revC", "renew:revD", "new"}, def: "renew:liveB",
			badges: map[string]string{"renew:liveB": BadgeSamePlan}},
		{name: "完整次序：同款 → 不同款过期 → 新开 → 不同款生效中", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{mom, expiredBasic, revC, mine},
			want:  []string{"renew:mine", "renew:revC", "change:expBasic", "new", "change:mom"}, def: "renew:mine",
			badges: map[string]string{"renew:mine": BadgeSamePlan}, expired: []string{"change:expBasic"}},
		{name: "后台从订阅行进来：预选入口那份（不同款生效中）", offer: Offer{Kind: OfferPlan, PlanID: "adv"},
			cands: []Candidate{mine, mom}, entry: "mine", want: []string{"new", "change:mom", "change:mine"},
			def: "change:mine", badges: map[string]string{}},
		{name: "后台从订阅行进来：同款多份时预选入口那份", offer: Offer{Kind: OfferPlan, PlanID: "std"},
			cands: []Candidate{liveA, liveB}, entry: "liveA", want: []string{"renew:liveB", "renew:liveA", "new"},
			def: "renew:liveA", badges: map[string]string{"renew:liveB": BadgeSamePlan}},
		{name: "入口是停用的那份：不在选项里 按规则取默认", offer: Offer{Kind: OfferPlan, PlanID: "adv"},
			cands: []Candidate{deadStd, mine}, entry: "dead", want: []string{"new", "change:mine"}, def: "",
			badges: map[string]string{}},

		// ---- 加时长卡 ----
		{name: "S5a 加时长 只有一份：不问", offer: Offer{Kind: OfferDays}, cands: []Candidate{single},
			want: []string{"extend_days:one"}, def: "extend_days:one",
			badges: map[string]string{"extend_days:one": BadgeSoonestExpiry}},
		{name: "S5b 加时长 两份：预选最快到期（妈妈那份）", offer: Offer{Kind: OfferDays}, cands: []Candidate{mine, mom},
			want: []string{"extend_days:mom", "extend_days:mine"}, def: "extend_days:mom",
			badges: map[string]string{"extend_days:mom": BadgeSoonestExpiry}},
		{name: "加时长：生效中在前 过期 30 天内接在后面 停用的不列", offer: Offer{Kind: OfferDays},
			cands: []Candidate{deadStd, expiredStd, mine, revD},
			want:  []string{"extend_days:mine", "extend_days:revD", "extend_days:exp"}, def: "extend_days:mine",
			badges: map[string]string{"extend_days:mine": BadgeSoonestExpiry}},
		{name: "加时长：永不过期的排最后", offer: Offer{Kind: OfferDays}, cands: []Candidate{noEnd, mine},
			want: []string{"extend_days:mine", "extend_days:forever"}, def: "extend_days:mine",
			badges: map[string]string{"extend_days:mine": BadgeSoonestExpiry}},
		{name: "加时长：没有可加的", offer: Offer{Kind: OfferDays}, cands: []Candidate{deadStd},
			want: []string{}, def: "", badges: map[string]string{}},

		// ---- 流量重置卡 ----
		{name: "S5a 重置 只有一份：不问 不显示徽标", offer: Offer{Kind: OfferReset}, cands: []Candidate{single},
			want: []string{"reset_traffic:one"}, def: "reset_traffic:one", badges: map[string]string{}},
		{name: "S5b 重置 两份：预选用得最多（我的 93%）", offer: Offer{Kind: OfferReset}, cands: []Candidate{mom, mine},
			want: []string{"reset_traffic:mom", "reset_traffic:mine"}, def: "reset_traffic:mine",
			badges: map[string]string{"reset_traffic:mine": BadgeMostUsed}},
		{name: "重置：只列生效中的", offer: Offer{Kind: OfferReset}, cands: []Candidate{expiredStd, mom},
			want: []string{"reset_traffic:mom"}, def: "reset_traffic:mom", badges: map[string]string{}},
		{name: "重置：不限量的按 0 算", offer: Offer{Kind: OfferReset}, cands: []Candidate{unlimited, single},
			want: []string{"reset_traffic:unl", "reset_traffic:one"}, def: "reset_traffic:one",
			badges: map[string]string{"reset_traffic:one": BadgeMostUsed}},
		{name: "重置：没有生效中的", offer: Offer{Kind: OfferReset}, cands: []Candidate{expiredStd},
			want: []string{}, def: "", badges: map[string]string{}},

		// ---- 加流量（送流量卡、流量包标签） ----
		{name: "S2 加流量 两份：预选剩得最少（我的 7G）", offer: Offer{Kind: OfferTraffic}, cands: []Candidate{mine, mom},
			want: []string{"add_traffic:mom", "add_traffic:mine"}, def: "add_traffic:mine",
			badges: map[string]string{"add_traffic:mine": BadgeLeastRemaining}},
		{name: "加流量：流量包余量算进剩余", offer: Offer{Kind: OfferTraffic}, cands: []Candidate{minePack, mom},
			want: []string{"add_traffic:mom", "add_traffic:mine"}, def: "add_traffic:mom",
			badges: map[string]string{"add_traffic:mom": BadgeLeastRemaining}},
		{name: "加流量：不限量的不会被预选", offer: Offer{Kind: OfferTraffic}, cands: []Candidate{unlimited, mine},
			want: []string{"add_traffic:unl", "add_traffic:mine"}, def: "add_traffic:mine",
			badges: map[string]string{"add_traffic:mine": BadgeLeastRemaining}},
		{name: "加流量 只有一份：不问", offer: Offer{Kind: OfferTraffic}, cands: []Candidate{single},
			want: []string{"add_traffic:one"}, def: "add_traffic:one", badges: map[string]string{}},
		{name: "加流量：一份生效中的都没有（送流量记未分配 / 流量包拒绝）", offer: Offer{Kind: OfferTraffic},
			cands: []Candidate{expiredStd, deadStd}, want: []string{}, def: "", badges: map[string]string{}},

		// ---- 一张卡多项奖励（普通卡、盲盒） ----
		{name: "多项：含加时长 按最快到期", offer: Offer{Kind: OfferMixed, HasDays: true},
			cands: []Candidate{mine, expiredStd, mom},
			want:  []string{"extend_days:mom", "extend_days:mine", "extend_days:exp"}, def: "extend_days:mom",
			badges: map[string]string{"extend_days:mom": BadgeSoonestExpiry}},
		{name: "多项：加时长 + 流量 只列生效中的 按最快到期", offer: Offer{Kind: OfferMixed, HasDays: true, HasTraffic: true},
			cands: []Candidate{mine, expiredStd, mom},
			want:  []string{"extend_days:mom", "extend_days:mine"}, def: "extend_days:mom",
			badges: map[string]string{"extend_days:mom": BadgeSoonestExpiry}},
		{name: "多项：重置 + 流量 按用得最多", offer: Offer{Kind: OfferMixed, HasReset: true, HasTraffic: true},
			cands: []Candidate{mom, mine}, want: []string{"reset_traffic:mom", "reset_traffic:mine"},
			def: "reset_traffic:mine", badges: map[string]string{"reset_traffic:mine": BadgeMostUsed}},
		{name: "多项：只有流量 按剩得最少", offer: Offer{Kind: OfferMixed, HasTraffic: true},
			cands: []Candidate{minePack, mom}, want: []string{"add_traffic:mom", "add_traffic:mine"},
			def: "add_traffic:mom", badges: map[string]string{"add_traffic:mom": BadgeLeastRemaining}},
		{name: "多项：全是余额 没有落点", offer: Offer{Kind: OfferMixed}, cands: []Candidate{mine},
			want: []string{}, def: "", badges: map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, def := Options(tc.offer, tc.cands, tc.entry)
			if got := keys(opts); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("keys=%v want %v", got, tc.want)
			}
			if def != tc.def {
				t.Fatalf("default=%q want %q", def, tc.def)
			}
			if got := badges(opts); !reflect.DeepEqual(got, tc.badges) {
				t.Fatalf("badges=%v want %v", got, tc.badges)
			}
			var expired []string
			for _, o := range opts {
				if o.Expired {
					expired = append(expired, o.Key)
				}
				if o.Kind == KindNew && (o.Key != "new" || o.SubscriptionID != "") {
					t.Fatalf("new option malformed: %+v", o)
				}
				if o.Kind != KindNew && o.Key != OptionKey(o.Kind, o.SubscriptionID) {
					t.Fatalf("key %q does not match kind/sub", o.Key)
				}
			}
			if !reflect.DeepEqual(expired, tc.expired) {
				t.Fatalf("expired=%v want %v", expired, tc.expired)
			}
		})
	}
}

// 不同款一律不预选；换掉一份（生效中或过期）在没有入口上下文时永远不是默认。
// 拿多份订阅的所有子集、所有套餐跑一遍，比逐格列举更能防回归。
func TestOptionsNeverDefaultsToReplacing(t *testing.T) {
	pool := []Candidate{mine, mom, single, expiredStd, deadStd,
		with(mom, func(c *Candidate) { c.SubscriptionID = "expBasic"; c.State = StateRevivable })}
	for mask := 0; mask < 1<<len(pool); mask++ {
		var cands []Candidate
		hasSame := map[string]bool{}
		for i, c := range pool {
			if mask&(1<<i) != 0 {
				cands = append(cands, c)
				if c.State != StateDead {
					hasSame[c.PlanID] = true
				}
			}
		}
		for _, plan := range []string{"std", "basic", "adv"} {
			opts, def := Options(Offer{Kind: OfferPlan, PlanID: plan}, cands, "")
			var chosen *Option
			for i := range opts {
				if opts[i].Key == def {
					chosen = &opts[i]
				}
			}
			if def != "" && chosen == nil {
				t.Fatalf("mask=%b plan=%s default %q not among options", mask, plan, def)
			}
			if chosen != nil && chosen.Kind == KindChange {
				t.Fatalf("mask=%b plan=%s defaulted to replacing %s", mask, plan, chosen.SubscriptionID)
			}
			if !hasSame[plan] && def != "" && len(opts) > 1 {
				t.Fatalf("mask=%b plan=%s different plan must not preselect, got %q", mask, plan, def)
			}
			if hasSame[plan] && (chosen == nil || chosen.Kind != KindRenew) {
				t.Fatalf("mask=%b plan=%s same plan must preselect renew, got %q", mask, plan, def)
			}
		}
	}
}

func TestChoiceMatchAndResolve(t *testing.T) {
	opts, _ := Options(Offer{Kind: OfferPlan, PlanID: "adv"}, []Candidate{mine, mom}, "")

	got, err := Choice{Kind: KindChange, SubscriptionID: "mom"}.Match(opts)
	if err != nil || got.Key != "change:mom" {
		t.Fatalf("match change:mom = %+v, %v", got, err)
	}
	if _, err := (Choice{Kind: KindRenew, SubscriptionID: "mom"}).Match(opts); !errors.Is(err, ErrChoiceStale) {
		t.Fatalf("renew on different plan must be stale, got %v", err)
	}
	if _, err := (Choice{Kind: KindChange, SubscriptionID: "someone-else"}).Match(opts); !errors.Is(err, ErrChoiceStale) {
		t.Fatalf("foreign subscription must be stale, got %v", err)
	}
	if got, err := (Choice{Kind: KindNew, SubscriptionID: "ignored"}).Match(opts); err != nil || got.Key != "new" {
		t.Fatalf("new ignores subscription id: %+v, %v", got, err)
	}

	// 多个选项而没给选择：要求先选
	if _, _, err := Resolve(nil, opts); !errors.Is(err, ErrChoiceRequired) {
		t.Fatalf("resolve without choice: %v", err)
	}
	// 只有一个选项：可以不传
	one, _ := Options(Offer{Kind: OfferPlan, PlanID: "std"}, []Candidate{single}, "")
	if o, ok, err := Resolve(nil, one); err != nil || !ok || o.Key != "renew:one" {
		t.Fatalf("single option resolve: %+v %v %v", o, ok, err)
	}
	// 一个选项都没有：ok=false，由调用方决定（送流量记未分配）
	if _, ok, err := Resolve(nil, nil); ok || err != nil {
		t.Fatalf("empty resolve: ok=%v err=%v", ok, err)
	}
	// 给了选择却失效
	if _, _, err := Resolve(&Choice{Kind: KindRenew, SubscriptionID: "gone"}, one); !errors.Is(err, ErrChoiceStale) {
		t.Fatalf("stale choice: %v", err)
	}
	// 空 kind 的选择等于没给
	if o, ok, err := Resolve(&Choice{}, one); err != nil || !ok || o.Key != "renew:one" {
		t.Fatalf("zero choice: %+v %v %v", o, ok, err)
	}
}
