package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNormalizeCode(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"  abcd2345efgh ":                 {"ABCD2345EFGH", true},
		"SHORT":                           {"", false},
		"ABCD-2345-EFGH":                  {"", false},
		"卡密卡密卡密卡密":                        {"", false},
		strings.Repeat("A", maxCodeLen+1): {"", false},
	}
	for in, c := range cases {
		got, ok := normalizeCode(in)
		if got != c.want || ok != c.ok {
			t.Errorf("normalizeCode(%q) = %q,%v，期望 %q,%v", in, got, ok, c.want, c.ok)
		}
	}
}

func TestNewCodeAlphabetAndMask(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newCode("GC")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(c, "GC") || len(c) != 2+codeRandomLen {
			t.Fatalf("卡密 %q 格式不对", c)
		}
		for _, r := range c[2:] {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("卡密 %q 含字母表外的字符 %q", c, r)
			}
		}
		if seen[c] {
			t.Fatalf("200 次里撞码 %q", c)
		}
		seen[c] = true
		if n, ok := normalizeCode(c); !ok || n != c {
			t.Fatalf("生成的码 %q 过不了规范化", c)
		}
		m := MaskCode(c)
		if strings.Contains(m, c[len(c)-maskedTail:]) || !strings.HasPrefix(m, c[:len(c)-maskedTail]) {
			t.Fatalf("掩码 %q 露出了末尾", m)
		}
	}
}

func TestCodeTransitions(t *testing.T) {
	allowed := [][2]string{
		{CodeUnused, CodeUsed}, {CodeUnused, CodeDisabled}, {CodeUnused, CodeExpired}, {CodeDisabled, CodeUnused},
	}
	for _, p := range allowed {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s → %s 应允许", p[0], p[1])
		}
	}
	denied := [][2]string{
		{CodeUsed, CodeUnused}, {CodeUsed, CodeDisabled}, {CodeExpired, CodeUnused}, {CodeDisabled, CodeUsed}, {CodeUnused, CodeUnused},
	}
	for _, p := range denied {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s → %s 应拒绝", p[0], p[1])
		}
	}
}

func TestEncodeDecodeCodes(t *testing.T) {
	in := []string{"AAAA2222BBBB", "CCCC3333DDDD"}
	out := decodeCodes(encodeCodes(in))
	if len(out) != 2 || out[0] != in[0] || out[1] != in[1] {
		t.Fatalf("往返得到 %v", out)
	}
	if decodeCodes(nil) != nil {
		t.Fatal("空载荷应解出 nil")
	}
}

func TestDrawPrizeRespectsWeights(t *testing.T) {
	pool := []Prize{{Label: "never", Weight: 0}, {Label: "always", Weight: 5}}
	for i := 0; i < 50; i++ {
		p, err := drawPrize(pool)
		if err != nil {
			t.Fatal(err)
		}
		if p.Label != "always" {
			t.Fatalf("抽中了权重 0 的 %q", p.Label)
		}
	}
	if _, err := drawPrize([]Prize{{Label: "x", Weight: 0}}); err == nil {
		t.Fatal("权重合计为 0 应报错")
	}
}

func TestNormalizeTemplateRejects(t *testing.T) {
	planIDOnly := uuid.Must(uuid.NewV7())
	bad := []TemplateInput{
		{Name: "", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.NewFromInt(1)}},
		{Name: "x", Type: "bogus"},
		{Name: "x", Type: TypeGeneral},
		{Name: "x", Type: TypeGeneral, Rewards: Rewards{ExpireDays: maxExpireDays + 1}},
		{Name: "x", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.RequireFromString("1.001")}},
		{Name: "x", Type: TypePlan},
		{Name: "x", Type: TypePlan, Rewards: Rewards{PlanID: &planIDOnly}}, // 套餐卡有 PlanID 没 PriceID
		{Name: "x", Type: TypeMystery, Rewards: Rewards{Pool: []Prize{{Label: "only", Weight: 1, Balance: decimal.NewFromInt(1)}}}},
		{Name: "x\x00", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.NewFromInt(1)}},
		{Name: "x", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.NewFromInt(1)}, Conditions: Conditions{NewUserOnly: true, PaidUserOnly: true}},
		{Name: "x", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.NewFromInt(1)}, Limits: Limits{MaxUsePerUser: -1}},
	}
	for i, in := range bad {
		if _, err := normalizeTemplate(in); err == nil {
			t.Errorf("第 %d 个输入应被拒: %+v", i, in)
		}
	}
	// 套餐卡有 PlanID 没 PriceID 必须返回 422 且 fields 有 rewards
	if _, err := normalizeTemplate(TemplateInput{Name: "x", Type: TypePlan, Rewards: Rewards{PlanID: &planIDOnly}}); err == nil {
		t.Fatal("套餐卡缺少 PriceID 应报错")
	} else if e := errs.As(err); e.Code != errs.ValidationFailed || e.Fields["rewards"] == "" {
		t.Fatalf("缺少 PriceID 期望 rewards 字段校验错误，得到 %v", err)
	}

	planID := uuid.Must(uuid.NewV7())
	priceID := uuid.Must(uuid.NewV7())
	got, err := normalizeTemplate(TemplateInput{Name: " 套餐卡 ", Type: TypePlan,
		Rewards: Rewards{PlanID: &planID, PriceID: &priceID, Balance: decimal.NewFromInt(9), ExpireDays: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "套餐卡" || got.Status != TemplateActive || !got.Rewards.Balance.IsZero() || got.Rewards.ExpireDays != 0 || *got.Rewards.PriceID != priceID {
		t.Fatalf("规范化结果 %+v：应去空白、默认 active、丢掉套餐卡用不到的奖励并保留 PriceID", got)
	}
}

// 套餐卡的落点：同款续、不同款只有装了折算才给「换掉」，新开永远在。
func TestPlanPlacementOptions(t *testing.T) {
	now := time.Now()
	planA, planB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	same := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planA, State: StateLive, PeriodEnd: now.Add(48 * time.Hour)}
	other := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planB, State: StateLive, PeriodEnd: now.Add(24 * time.Hour)}
	dead := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planA, State: StateDead, PeriodEnd: now.Add(-90 * 24 * time.Hour)}

	opts, def := placementOptions(offer{Kind: offerPlan, PlanID: planA, PlanKind: catalogsvc.KindSubscription}, []candidate{same}, false)
	if len(opts) != 1 || opts[0].Kind != KindRenew || def != opts[0].Key {
		t.Fatalf("只有一份同款：%+v def=%q", opts, def)
	}

	opts, def = placementOptions(offer{Kind: offerPlan, PlanID: planA, PlanKind: catalogsvc.KindSubscription}, []candidate{other, dead}, false)
	if len(opts) != 1 || opts[0].Kind != KindNew || def != KindNew {
		t.Fatalf("没装折算、只有不同款：%+v def=%q", opts, def)
	}

	opts, def = placementOptions(offer{Kind: offerPlan, PlanID: planA, PlanKind: catalogsvc.KindSubscription}, []candidate{other}, true)
	if len(opts) != 2 || opts[0].Kind != KindNew || opts[1].Kind != KindChange || def != "" {
		t.Fatalf("装了折算、只有不同款：%+v def=%q", opts, def)
	}

	if _, _, err := resolveChoice(nil, opts); err != ErrChoiceRequired {
		t.Fatalf("多个选项不选应回 ErrChoiceRequired，得到 %v", err)
	}
	id := other.SubscriptionID
	if o, ok, err := resolveChoice(&Choice{Kind: KindChange, SubscriptionID: &id}, opts); err != nil || !ok || o.Kind != KindChange {
		t.Fatalf("选「换掉」: %+v %v %v", o, ok, err)
	}
	if _, _, err := resolveChoice(&Choice{Kind: KindRenew, SubscriptionID: &id}, opts); err != ErrChoiceStale {
		t.Fatalf("不在选项里的选择应回 ErrChoiceStale，得到 %v", err)
	}
}

// 一次性套餐卡的落点：同款叠加（默认）、新开，换掉。
func TestOneTimePlanPlacementOptions(t *testing.T) {
	now := time.Now()
	planA, planB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	sameLive := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planA, State: StateLive, PeriodEnd: now.Add(48 * time.Hour)}
	sameRevivable := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planA, State: StateRevivable, PeriodEnd: now.Add(-24 * time.Hour)}
	otherLive := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planB, State: StateLive, PeriodEnd: now.Add(24 * time.Hour)}

	// 1. 候选一份同款生效中 → 选项依次是 stack（BadgeSamePlan、默认）、new
	opts, def := placementOptions(offer{Kind: offerPlan, PlanID: planA, PlanKind: catalogsvc.KindOneTime}, []candidate{sameLive}, false)
	if len(opts) != 2 || opts[0].Kind != KindStack || opts[0].Badge != BadgeSamePlan || def != opts[0].Key || opts[1].Kind != KindNew {
		t.Fatalf("同款生效中：%+v def=%q", opts, def)
	}

	// 2. 同款只有已过期（StateRevivable）→ 只有 new
	opts, def = placementOptions(offer{Kind: offerPlan, PlanID: planA, PlanKind: catalogsvc.KindOneTime}, []candidate{sameRevivable}, false)
	if len(opts) != 1 || opts[0].Kind != KindNew || def != KindNew {
		t.Fatalf("同款已过期：%+v def=%q", opts, def)
	}

	// 3. 同款生效中 + 不同款生效中、allowChange=true → stack、new、change (stack 是默认且有 BadgeSamePlan)
	opts, def = placementOptions(offer{Kind: offerPlan, PlanID: planA, PlanKind: catalogsvc.KindOneTime}, []candidate{sameLive, otherLive}, true)
	if len(opts) != 3 || opts[0].Kind != KindStack || opts[0].Badge != BadgeSamePlan || def != opts[0].Key ||
		opts[1].Kind != KindNew || opts[2].Kind != KindChange {
		t.Fatalf("同款生效中+不同款生效中（允许换）：%+v def=%q", opts, def)
	}
}

// 无落点时的报错：只送流量（含余额 + 流量、盲盒流量 + 余额）回 ErrNoTrafficTarget，含加时长或重置的回 ErrNoPlacement。
func TestNoTargetErr(t *testing.T) {
	cases := []struct {
		t    Template
		want error
	}{
		{Template{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 1}}, ErrNoTrafficTarget},
		{Template{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 1, Balance: decimal.NewFromInt(1)}}, ErrNoTrafficTarget},
		{Template{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 1, ExpireDays: 1}}, ErrNoPlacement},
		{Template{Type: TypeGeneral, Rewards: Rewards{ResetQuota: true}}, ErrNoPlacement},
		{Template{Type: TypeGeneral, Rewards: Rewards{ResetQuota: true, TrafficBytes: 1}}, ErrNoPlacement},
		{Template{Type: TypeMystery, Rewards: Rewards{Pool: []Prize{{TrafficBytes: 1}, {Balance: decimal.NewFromInt(1)}}}}, ErrNoTrafficTarget},
		{Template{Type: TypeMystery, Rewards: Rewards{Pool: []Prize{{TrafficBytes: 1}, {ExpireDays: 1}}}}, ErrNoPlacement},
		{Template{Type: TypePlan, Rewards: Rewards{}}, ErrNoPlacement},
	}
	for i, c := range cases {
		if got := offerOf(c.t, "").noTargetErr(); got != c.want {
			t.Errorf("第 %d 个：noTargetErr = %v，期望 %v", i, got, c.want)
		}
	}
}

// 通用卡落点过滤永久与不限量；盲盒不排除。
func TestPlacementFiltersPermanentAndUnlimited(t *testing.T) {
	now := time.Now()
	permID := uuid.Must(uuid.NewV7())
	unlimID := uuid.Must(uuid.NewV7())
	perm := candidate{SubscriptionID: permID, State: StateLive, Permanent: true, PeriodEnd: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
	unlim := candidate{SubscriptionID: unlimID, State: StateLive, Unlimited: true, PeriodEnd: now.Add(24 * time.Hour)}

	// 通用卡加时长（offerDays）时 Permanent 的候选不在选项里
	opts, _ := placementOptions(offer{Kind: offerDays, Days: 30}, []candidate{perm, unlim}, false)
	if len(opts) != 1 || opts[0].SubscriptionID == nil || *opts[0].SubscriptionID != unlimID {
		t.Fatalf("加时长应排除永久候选，得到 %+v", opts)
	}

	// 通用卡加流量（offerTraffic）时 Unlimited 的候选不在选项里
	opts, _ = placementOptions(offer{Kind: offerTraffic}, []candidate{perm, unlim}, false)
	if len(opts) != 1 || opts[0].SubscriptionID == nil || *opts[0].SubscriptionID != permID {
		t.Fatalf("加流量应排除不限量候选，得到 %+v", opts)
	}

	// Mystery: true 时，offerMixed（HasDays 或 HasTraffic）均不排除
	mysteryDays := offer{Kind: offerMixed, Mystery: true, HasDays: true, Days: 30}
	opts, _ = placementOptions(mysteryDays, []candidate{perm, unlim}, false)
	if len(opts) != 2 {
		t.Fatalf("盲盒加时长不应排除永久候选，得到 %+v", opts)
	}

	mysteryTraffic := offer{Kind: offerMixed, Mystery: true, HasTraffic: true}
	opts, _ = placementOptions(mysteryTraffic, []candidate{perm, unlim}, false)
	if len(opts) != 2 {
		t.Fatalf("盲盒加流量不应排除不限量候选，得到 %+v", opts)
	}
}
