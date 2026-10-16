package service

import (
	"math"
	"reflect"
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

func TestPlanQuotaAddsPerRow(t *testing.T) {
	a, b := cid(1), cid(2)
	rows := []repo.TrafficQuotaRow{
		{ID: cid(10), SubscriptionID: a, Room: i64(1000)},
		{ID: cid(11), SubscriptionID: b, Room: nil},
		{ID: cid(12), SubscriptionID: a, Room: nil},
	}
	p := planTrafficCharges([]chargeItem{{a, 30}, {b, 7}}, rows)
	want := []repo.QuotaAdd{{ID: cid(10), Amount: 30}, {ID: cid(11), Amount: 7}, {ID: cid(12), Amount: 30}}
	if len(p.QuotaAdds) != len(want) {
		t.Fatalf("QuotaAdds %v", p.QuotaAdds)
	}
	for i := range want {
		if p.QuotaAdds[i] != want[i] {
			t.Fatalf("QuotaAdds[%d] = %v，期望 %v", i, p.QuotaAdds[i], want[i])
		}
	}
	if len(p.Exhausted) != 0 {
		t.Fatalf("不该判耗尽：%v", p.Exhausted)
	}
}

func TestPlanTrafficChargesTable(t *testing.T) {
	a, b := cid(1), cid(2)
	tests := []struct {
		name          string
		items         []chargeItem
		rows          []repo.TrafficQuotaRow
		wantQuotaAdds []repo.QuotaAdd
		wantExhausted []uuid.UUID
	}{
		{
			name:  "不限量（room nil，不进 Exhausted）",
			items: []chargeItem{{SubID: a, Bytes: 150}},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: nil},
			},
			wantQuotaAdds: []repo.QuotaAdd{{ID: cid(10), Amount: 150}},
			wantExhausted: nil,
		},
		{
			name:  "还剩一点（不进 Exhausted）",
			items: []chargeItem{{SubID: a, Bytes: 99}},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: i64(100)},
			},
			wantQuotaAdds: []repo.QuotaAdd{{ID: cid(10), Amount: 99}},
			wantExhausted: nil,
		},
		{
			name:  "刚好用完（room 100 扣 100 → Exhausted）",
			items: []chargeItem{{SubID: a, Bytes: 100}},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: i64(100)},
			},
			wantQuotaAdds: []repo.QuotaAdd{{ID: cid(10), Amount: 100}},
			wantExhausted: []uuid.UUID{a},
		},
		{
			name:  "超出（room 100 扣 150 → Exhausted，consumed 加 150）",
			items: []chargeItem{{SubID: a, Bytes: 150}},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: i64(100)},
			},
			wantQuotaAdds: []repo.QuotaAdd{{ID: cid(10), Amount: 150}},
			wantExhausted: []uuid.UUID{a},
		},
		{
			name:  "本来就用尽（room 0 扣 10 → 不重复进 Exhausted）",
			items: []chargeItem{{SubID: a, Bytes: 10}},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: i64(0)},
			},
			wantQuotaAdds: []repo.QuotaAdd{{ID: cid(10), Amount: 10}},
			wantExhausted: nil,
		},
		{
			name:  "本来就超额（room -20 扣 10 → 不重复进 Exhausted）",
			items: []chargeItem{{SubID: a, Bytes: 10}},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: i64(-20)},
			},
			wantQuotaAdds: []repo.QuotaAdd{{ID: cid(10), Amount: 10}},
			wantExhausted: nil,
		},
		{
			name: "两份订阅混在一批",
			items: []chargeItem{
				{SubID: a, Bytes: 150},
				{SubID: b, Bytes: 50},
			},
			rows: []repo.TrafficQuotaRow{
				{ID: cid(10), SubscriptionID: a, Room: i64(100)},
				{ID: cid(20), SubscriptionID: b, Room: i64(500)},
			},
			wantQuotaAdds: []repo.QuotaAdd{
				{ID: cid(10), Amount: 150},
				{ID: cid(20), Amount: 50},
			},
			wantExhausted: []uuid.UUID{a},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := planTrafficCharges(tt.items, tt.rows)
			if !reflect.DeepEqual(p.QuotaAdds, tt.wantQuotaAdds) {
				t.Errorf("%s: QuotaAdds = %v, 期望 %v", tt.name, p.QuotaAdds, tt.wantQuotaAdds)
			}
			if len(p.Exhausted) == 0 && len(tt.wantExhausted) == 0 {
				// Both empty/nil
			} else if !reflect.DeepEqual(p.Exhausted, tt.wantExhausted) {
				t.Errorf("%s: Exhausted = %v, 期望 %v", tt.name, p.Exhausted, tt.wantExhausted)
			}
		})
	}
}

func TestPlanUsageSorted(t *testing.T) {
	a, b := cid(1), cid(2)
	p := planTrafficCharges([]chargeItem{{b, 5}, {a, 9}}, nil)
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
