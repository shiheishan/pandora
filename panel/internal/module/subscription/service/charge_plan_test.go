package service

import (
	"math"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

// cid 生成固定的 uuid，n 越大字节序越靠后。
func cid(n int) uuid.UUID {
	id := uuid.MustParse("00000000-0000-7000-8000-000000000000")
	id[15] = byte(n)
	id[14] = byte(n >> 8)
	return id
}

func TestSplitTrafficCharge(t *testing.T) {
	for _, c := range []struct {
		name               string
		billed             int64
		room               *int64
		packs              int64
		wantPlan, wantPack int64
	}{
		{"零用量", 0, i64(100), 50, 0, 0},
		{"负用量", -5, i64(100), 50, 0, 0},
		{"不限量", 150, nil, 50, 150, 0},
		{"额度够", 80, i64(100), 50, 80, 0},
		{"额度不够、包不够", 150, i64(100), 30, 120, 30},
		{"额度不够、包够", 150, i64(100), 100, 100, 50},
		{"已超额", 50, i64(-20), 10, 40, 10},
		{"额度为零无包", 50, i64(0), 0, 50, 0},
	} {
		plan, pack := splitTrafficCharge(c.billed, c.room, c.packs)
		if plan != c.wantPlan || pack != c.wantPack {
			t.Errorf("%s: 得 (%d,%d)，期望 (%d,%d)", c.name, plan, pack, c.wantPlan, c.wantPack)
		}
	}
}

func TestQuotaRooms(t *testing.T) {
	a, b, c := cid(1), cid(2), cid(3)
	rooms := quotaRooms([]repo.TrafficQuotaRow{
		{ID: cid(10), SubscriptionID: a, Room: nil},
		{ID: cid(11), SubscriptionID: a, Room: i64(5)},
		{ID: cid(12), SubscriptionID: a, Room: i64(3)},
		{ID: cid(13), SubscriptionID: b, Room: nil},
	})
	if r := rooms[a]; r == nil || *r != 3 {
		t.Fatalf("a 的剩余额度应取最紧的 3，得 %v", r)
	}
	if r, ok := rooms[b]; !ok || r != nil {
		t.Fatalf("b 只有不限量行，应在 map 里且为 nil，得 %v %v", r, ok)
	}
	if _, ok := rooms[c]; ok {
		t.Fatal("没有配额行的订阅不该在 map 里")
	}
}

func TestNeedsPacks(t *testing.T) {
	for _, c := range []struct {
		room  *int64
		bytes int64
		want  bool
	}{
		{nil, 100, false},
		{i64(10), 10, true},
		{i64(10), 9, false},
		{i64(-5), 1, true},
	} {
		if got := needsPacks(c.room, c.bytes); got != c.want {
			t.Errorf("needsPacks(%v, %d) = %v", c.room, c.bytes, got)
		}
	}
}

func TestPlanQuotaAddsPerRow(t *testing.T) {
	a, b := cid(1), cid(2)
	rows := []repo.TrafficQuotaRow{
		{ID: cid(10), SubscriptionID: a, Room: i64(1000)},
		{ID: cid(11), SubscriptionID: b, Room: nil},
		{ID: cid(12), SubscriptionID: a, Room: nil},
	}
	p := planTrafficCharges([]chargeItem{{a, 30}, {b, 7}}, rows, nil)
	want := []repo.QuotaAdd{{ID: cid(10), Amount: 30}, {ID: cid(11), Amount: 7}, {ID: cid(12), Amount: 30}}
	if len(p.QuotaAdds) != len(want) {
		t.Fatalf("QuotaAdds %v", p.QuotaAdds)
	}
	for i := range want {
		if p.QuotaAdds[i] != want[i] {
			t.Fatalf("QuotaAdds[%d] = %v，期望 %v", i, p.QuotaAdds[i], want[i])
		}
	}
	if len(p.PackTakes) != 0 || len(p.Exhausted) != 0 {
		t.Fatalf("不该扣包或判耗尽：%v %v", p.PackTakes, p.Exhausted)
	}
}

func TestPlanPacksFirstComeFirstServed(t *testing.T) {
	a := cid(1)
	rows := []repo.TrafficQuotaRow{{ID: cid(10), SubscriptionID: a, Room: i64(0)}}
	packs := []repo.OpenTrafficPack{
		{ID: cid(20), SubscriptionID: a, Remaining: 10},
		{ID: cid(21), SubscriptionID: a, Remaining: 20},
	}
	p := planTrafficCharges([]chargeItem{{a, 25}}, rows, packs)
	if len(p.PackTakes) != 2 || p.PackTakes[0] != (repo.PackTake{ID: cid(20), Amount: 10}) ||
		p.PackTakes[1] != (repo.PackTake{ID: cid(21), Amount: 15}) {
		t.Fatalf("PackTakes %v", p.PackTakes)
	}
	if len(p.QuotaAdds) != 0 {
		t.Fatalf("全部由包承担，配额不该加：%v", p.QuotaAdds)
	}
	if packs[0].Remaining != 10 {
		t.Fatal("不该改调用方的切片")
	}
}

func TestPlanExhausted(t *testing.T) {
	a := cid(1)
	for _, c := range []struct {
		name  string
		room  *int64
		packs []int64
		bytes int64
		want  bool
	}{
		{"恰好用完", i64(100), nil, 100, true},
		{"还剩一点", i64(100), nil, 99, false},
		{"之前就没余量", i64(0), nil, 10, false},
		{"额度加包一起用完", i64(10), []int64{5}, 15, true},
		{"包还有剩", i64(10), []int64{5}, 12, false},
		{"不限量", nil, nil, 1 << 40, false},
	} {
		rows := []repo.TrafficQuotaRow{{ID: cid(10), SubscriptionID: a, Room: c.room}}
		var packs []repo.OpenTrafficPack
		for i, left := range c.packs {
			packs = append(packs, repo.OpenTrafficPack{ID: cid(20 + i), SubscriptionID: a, Remaining: left})
		}
		p := planTrafficCharges([]chargeItem{{a, c.bytes}}, rows, packs)
		if got := len(p.Exhausted) == 1 && p.Exhausted[0] == a; got != c.want {
			t.Errorf("%s: Exhausted=%v，期望 %v", c.name, p.Exhausted, c.want)
		}
	}
}

func TestPlanUsageSorted(t *testing.T) {
	a, b := cid(1), cid(2)
	p := planTrafficCharges([]chargeItem{{b, 5}, {a, 9}}, nil, nil)
	if len(p.Usage) != 2 || p.Usage[0] != (repo.UsageAdd{SubscriptionID: a, Bytes: 9}) ||
		p.Usage[1] != (repo.UsageAdd{SubscriptionID: b, Bytes: 5}) {
		t.Fatalf("Usage %v", p.Usage)
	}
	if len(p.QuotaAdds) != 0 {
		t.Fatalf("没有配额行就不写配额：%v", p.QuotaAdds)
	}
}

func TestSatAdd(t *testing.T) {
	if got := satAdd(math.MaxInt64-1, 5); got != math.MaxInt64 {
		t.Fatalf("satAdd 应封顶，得 %d", got)
	}
	if got := satAdd(2, 3); got != 5 {
		t.Fatalf("satAdd(2,3) = %d", got)
	}
}
