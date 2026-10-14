package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

func distOn(l1, l2, l3 int) settingssvc.CommissionDistribution {
	return settingssvc.CommissionDistribution{Enabled: true, Levels: [3]int{l1, l2, l3}}
}

func chainNext(m map[uuid.UUID]uuid.UUID) referrerFunc {
	return func(_ context.Context, userID uuid.UUID) (*uuid.UUID, error) {
		up, ok := m[userID]
		if !ok {
			return nil, nil
		}
		return &up, nil
	}
}

func TestBeneficiaries(t *testing.T) {
	ctx := context.Background()
	buyer := uuid.New()
	direct := uuid.New()

	t.Run("关闭", func(t *testing.T) {
		next := func(context.Context, uuid.UUID) (*uuid.UUID, error) {
			t.Fatal("不应调用 next")
			return nil, nil
		}
		list, err := beneficiaries(ctx, buyer, direct, settingssvc.CommissionDistribution{}, next)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].UserID != direct || list[0].Level != 1 || list[0].SharePercent != 100 {
			t.Fatalf("got %+v", list)
		}
	})

	t.Run("开启50_30_20四级链", func(t *testing.T) {
		a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		list, err := beneficiaries(ctx, buyer, a, distOn(50, 30, 20), chainNext(map[uuid.UUID]uuid.UUID{
			a: b, b: c, c: d,
		}))
		if err != nil {
			t.Fatal(err)
		}
		want := []beneficiary{
			{UserID: a, Level: 1, SharePercent: 50},
			{UserID: b, Level: 2, SharePercent: 30},
			{UserID: c, Level: 3, SharePercent: 20},
		}
		if len(list) != 3 {
			t.Fatalf("got %+v", list)
		}
		for i := range want {
			if list[i] != want[i] {
				t.Fatalf("[%d] got %+v want %+v", i, list[i], want[i])
			}
		}
	})

	t.Run("开启只有两级", func(t *testing.T) {
		a, b := uuid.New(), uuid.New()
		list, err := beneficiaries(ctx, buyer, a, distOn(50, 30, 20), chainNext(map[uuid.UUID]uuid.UUID{a: b}))
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || list[0].Level != 1 || list[1].Level != 2 {
			t.Fatalf("got %+v", list)
		}
	})

	t.Run("第一级0跳过继续", func(t *testing.T) {
		a, b, c := uuid.New(), uuid.New(), uuid.New()
		list, err := beneficiaries(ctx, buyer, a, distOn(0, 60, 40), chainNext(map[uuid.UUID]uuid.UUID{a: b, b: c}))
		if err != nil {
			t.Fatal(err)
		}
		want := []beneficiary{
			{UserID: b, Level: 2, SharePercent: 60},
			{UserID: c, Level: 3, SharePercent: 40},
		}
		if len(list) != 2 {
			t.Fatalf("got %+v", list)
		}
		for i := range want {
			if list[i] != want[i] {
				t.Fatalf("got %+v want %+v", list, want)
			}
		}
	})

	t.Run("环AB", func(t *testing.T) {
		a, b := uuid.New(), uuid.New()
		list, err := beneficiaries(ctx, buyer, a, distOn(50, 30, 20), chainNext(map[uuid.UUID]uuid.UUID{a: b, b: a}))
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 {
			t.Fatalf("got %+v", list)
		}
		if list[0].UserID != a || list[1].UserID != b {
			t.Fatalf("got %+v", list)
		}
	})

	t.Run("环到买家", func(t *testing.T) {
		a := uuid.New()
		list, err := beneficiaries(ctx, buyer, a, distOn(50, 30, 20), chainNext(map[uuid.UUID]uuid.UUID{a: buyer}))
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].UserID != a {
			t.Fatalf("got %+v", list)
		}
	})

	t.Run("next报错", func(t *testing.T) {
		a := uuid.New()
		sentinel := errors.New("boom")
		next := func(context.Context, uuid.UUID) (*uuid.UUID, error) {
			return nil, sentinel
		}
		_, err := beneficiaries(ctx, buyer, a, distOn(50, 30, 20), next)
		if err != sentinel {
			t.Fatalf("err %v", err)
		}
	})
}

func TestLevelAmount(t *testing.T) {
	cases := []struct {
		base  string
		bp    int64
		share int
		want  string
	}{
		{"100.00", 1000, 100, "10.00"},
		{"100.00", 1000, 33, "3.30"},
		{"9.99", 1000, 50, "0.49"},
	}
	for _, c := range cases {
		got := LevelAmount(dec(c.base), c.bp, c.share)
		if !got.Equal(dec(c.want)) {
			t.Errorf("LevelAmount(%s,%d,%d)=%s want %s", c.base, c.bp, c.share, got, c.want)
		}
	}
	base := dec("100.00")
	bp := int64(1000) // 10% 总额
	sum := LevelAmount(base, bp, 50).Add(LevelAmount(base, bp, 30)).Add(LevelAmount(base, bp, 20))
	total := Amount(base, bp)
	if sum.GreaterThan(total) {
		t.Fatalf("三级之和 %s 超过总额 %s", sum, total)
	}
}

func TestDirectRateBP(t *testing.T) {
	if DirectRateBP(10, settingssvc.CommissionDistribution{}) != 1000 {
		t.Fatal("关")
	}
	if DirectRateBP(10, distOn(60, 30, 10)) != 600 {
		t.Fatal("开 l1=60")
	}
}
