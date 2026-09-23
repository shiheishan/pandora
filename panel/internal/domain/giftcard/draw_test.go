package giftcard

import (
	"math"
	"testing"
)

// TestDrawPrizeFollowsWeights 验证盲盒真的按权重开奖。
//
// 这件事只能靠统计验：抽一次看不出任何东西，抽少了噪声盖过信号。
// 10 万次下，每档的标准差是 sqrt(n*p*(1-p))/n，对 p=0.05 约 0.07%，
// 所以 1% 的容差相当于十几个标准差 —— 真跑偏了一定会被抓到，
// 而正常的随机波动不会误报。
func TestDrawPrizeFollowsWeights(t *testing.T) {
	pool := []MysteryPrize{
		{Label: "常见", Weight: 70, Balance: 100},
		{Label: "少见", Weight: 25, Balance: 500},
		{Label: "稀有", Weight: 5, Balance: 5000},
	}
	const n = 100000
	count := map[string]int{}
	for i := 0; i < n; i++ {
		p, err := drawPrize(pool)
		if err != nil {
			t.Fatalf("第 %d 次抽奖出错: %v", i, err)
		}
		count[p.Label]++
	}

	want := map[string]float64{"常见": 0.70, "少见": 0.25, "稀有": 0.05}
	for label, expect := range want {
		got := float64(count[label]) / n
		if math.Abs(got-expect) > 0.01 {
			t.Errorf("%s 实际占比 %.4f，期望 %.2f（偏差超过 1%%）", label, got, expect)
		}
		t.Logf("%s: %.4f（期望 %.2f，样本 %d）", label, got, expect, count[label])
	}

	if len(count) != 3 {
		t.Errorf("抽到了 %d 种奖品，期望 3 种 —— 有档位永远抽不到", len(count))
	}
}

// TestDrawPrizeSingleWeightAlwaysWins 权重集中时不该抽到别的。
//
// 这个用例守的是区间累加的边界：如果 n < acc 写成了 n <= acc，
// 权重为 0 的档位会偶尔被抽中。而权重 0 在业务上的意思是「不发」。
func TestDrawPrizeSingleWeightAlwaysWins(t *testing.T) {
	pool := []MysteryPrize{
		{Label: "唯一", Weight: 1, Balance: 100},
		{Label: "陪跑", Weight: 0, Balance: 999999},
	}
	for i := 0; i < 2000; i++ {
		p, err := drawPrize(pool)
		if err != nil {
			t.Fatal(err)
		}
		if p.Label != "唯一" {
			t.Fatalf("第 %d 次抽到了权重为 0 的「%s」", i, p.Label)
		}
	}
}

// TestDrawPrizeRejectsEmptyPool 空奖池必须报错而不是静默返回零值。
//
// 静默返回的话，用户会收到一张「兑换成功」但什么都没给的回执。
func TestDrawPrizeRejectsEmptyPool(t *testing.T) {
	if _, err := drawPrize(nil); err == nil {
		t.Fatal("空奖池应当报错")
	}
	if _, err := drawPrize([]MysteryPrize{{Label: "零权重", Weight: 0}}); err == nil {
		t.Fatal("权重全为 0 的奖池应当报错")
	}
}

// TestValidateRewards 卡住那些「建得出来但兑了等于没兑」的配置。
func TestValidateRewards(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		rewards Rewards
		wantErr bool
	}{
		{"通用卡什么都不送", "general", Rewards{}, true},
		{"通用卡送余额", "general", Rewards{Balance: 100}, false},
		{"通用卡只重置流量", "general", Rewards{ResetQuota: true}, false},
		{"通用卡负数", "general", Rewards{Balance: -1}, true},
		{"延长天数过大", "general", Rewards{ExpireDays: 99999}, true},
		{"套餐卡缺套餐", "plan", Rewards{}, true},
		{"套餐卡非法 uuid", "plan", Rewards{PlanID: "not-a-uuid"}, true},
		{"盲盒只有一档", "mystery", Rewards{Pool: []MysteryPrize{
			{Label: "唯一", Weight: 1, Balance: 100}}}, true},
		{"盲盒有档位没名字", "mystery", Rewards{Pool: []MysteryPrize{
			{Label: "甲", Weight: 1, Balance: 100},
			{Label: "", Weight: 1, Balance: 200}}}, true},
		{"盲盒有档位什么都不送", "mystery", Rewards{Pool: []MysteryPrize{
			{Label: "甲", Weight: 1, Balance: 100},
			{Label: "乙", Weight: 1}}}, true},
		{"盲盒正常", "mystery", Rewards{Pool: []MysteryPrize{
			{Label: "甲", Weight: 7, Balance: 100},
			{Label: "乙", Weight: 3, Balance: 500}}}, false},
		{"未知卡型", "whatever", Rewards{Balance: 100}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRewards(c.typ, c.rewards)
			if (err != nil) != c.wantErr {
				t.Fatalf("期望出错=%v，实际 err=%v", c.wantErr, err)
			}
		})
	}
}

// TestNewCodeAvoidsConfusableChars 卡密常要人工抄写或电话报读。
func TestNewCodeAvoidsConfusableChars(t *testing.T) {
	for i := 0; i < 500; i++ {
		code, err := newCode("")
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 12 {
			t.Fatalf("码长 %d，期望 12", len(code))
		}
		for _, c := range code {
			// I/L/O/0/1 在多数字体里几乎不可分辨，混淆一次
			// 就是一张废卡加一个工单
			if c == 'I' || c == 'L' || c == 'O' || c == '0' || c == '1' {
				t.Fatalf("卡密 %s 含易混淆字符 %c", code, c)
			}
		}
	}
}
