package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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
	bad := []TemplateInput{
		{Name: "", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.NewFromInt(1)}},
		{Name: "x", Type: "bogus"},
		{Name: "x", Type: TypeGeneral},
		{Name: "x", Type: TypeGeneral, Rewards: Rewards{ExpireDays: maxExpireDays + 1}},
		{Name: "x", Type: TypeGeneral, Rewards: Rewards{Balance: decimal.RequireFromString("1.001")}},
		{Name: "x", Type: TypePlan},
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
	planID := uuid.Must(uuid.NewV7())
	got, err := normalizeTemplate(TemplateInput{Name: " 套餐卡 ", Type: TypePlan,
		Rewards: Rewards{PlanID: &planID, Balance: decimal.NewFromInt(9), ExpireDays: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "套餐卡" || got.Status != TemplateActive || !got.Rewards.Balance.IsZero() || got.Rewards.ExpireDays != 0 {
		t.Fatalf("规范化结果 %+v：应去空白、默认 active、丢掉套餐卡用不到的奖励", got)
	}
}

// 套餐卡的落点：同款续、不同款只有装了折算才给「换掉」，新开永远在。
func TestPlanPlacementOptions(t *testing.T) {
	now := time.Now()
	planA, planB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	same := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planA, State: StateLive, PeriodEnd: now.Add(48 * time.Hour)}
	other := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planB, State: StateLive, PeriodEnd: now.Add(24 * time.Hour)}
	dead := candidate{SubscriptionID: uuid.Must(uuid.NewV7()), PlanID: planA, State: StateDead, PeriodEnd: now.Add(-90 * 24 * time.Hour)}

	opts, def := placementOptions(offer{Kind: offerPlan, PlanID: planA}, []candidate{same}, false)
	if len(opts) != 1 || opts[0].Kind != KindRenew || def != opts[0].Key {
		t.Fatalf("只有一份同款：%+v def=%q", opts, def)
	}

	opts, def = placementOptions(offer{Kind: offerPlan, PlanID: planA}, []candidate{other, dead}, false)
	if len(opts) != 1 || opts[0].Kind != KindNew || def != KindNew {
		t.Fatalf("没装折算、只有不同款：%+v def=%q", opts, def)
	}

	opts, def = placementOptions(offer{Kind: offerPlan, PlanID: planA}, []candidate{other}, true)
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

// 只送流量的卡没有落点也能兑；加时长、重置不行。
func TestTrafficOnlyOffer(t *testing.T) {
	cases := []struct {
		t    Template
		want bool
	}{
		{Template{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 1}}, true},
		{Template{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 1, Balance: decimal.NewFromInt(1)}}, true},
		{Template{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 1, ExpireDays: 1}}, false},
		{Template{Type: TypeGeneral, Rewards: Rewards{ResetQuota: true}}, false},
		{Template{Type: TypeMystery, Rewards: Rewards{Pool: []Prize{{TrafficBytes: 1}, {Balance: decimal.NewFromInt(1)}}}}, true},
		{Template{Type: TypeMystery, Rewards: Rewards{Pool: []Prize{{TrafficBytes: 1}, {ExpireDays: 1}}}}, false},
	}
	for i, c := range cases {
		if got := offerOf(c.t).trafficOnly(); got != c.want {
			t.Errorf("第 %d 个：trafficOnly = %v，期望 %v", i, got, c.want)
		}
	}
}
