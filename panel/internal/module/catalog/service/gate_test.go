package service

// gate_test.go 覆盖下单快照与门户目录的门禁纯规则：可见性、购买用途判定、价格有效性。

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func activeSubscriptionPlan() Plan {
	ver := uuid.New()
	return Plan{
		Kind:             KindSubscription,
		Status:           PlanActive,
		Visibility:       VisibilityPublic,
		CurrentVersionID: &ver,
		AllowNewPurchase: true,
		AllowRenewal:     true,
		AllowUpgrade:     true,
	}
}

func activeOneTimePlan() Plan {
	ver := uuid.New()
	return Plan{
		Kind:             KindOneTime,
		Status:           PlanActive,
		Visibility:       VisibilityPublic,
		CurrentVersionID: &ver,
		AllowNewPurchase: true,
		AllowRenewal:     false,
		AllowUpgrade:     true,
	}
}

func activePrice(kind string) Price {
	return Price{
		ID:         uuid.New(),
		PlanKind:   kind,
		Status:     PriceActive,
		UnitAmount: decimal.New(10, 0),
	}
}

func TestVisibleTo(t *testing.T) {
	gIn := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	gOut := uuid.MustParse("018e0000-0000-7000-8000-000000000002")

	cases := []struct {
		name       string
		visibility string
		groups     []uuid.UUID
		viewer     viewer
		status     string
		want       bool
	}{
		// public
		{"public 匿名", VisibilityPublic, nil, viewer{}, PlanActive, true},
		{"public 登录", VisibilityPublic, nil, viewer{LoggedIn: true}, PlanActive, true},
		// authenticated
		{"authenticated 匿名", VisibilityAuthenticated, nil, viewer{}, PlanActive, false},
		{"authenticated 登录", VisibilityAuthenticated, nil, viewer{LoggedIn: true}, PlanActive, true},
		// group
		{"group 匿名", VisibilityGroup, []uuid.UUID{gIn}, viewer{}, PlanActive, false},
		{"group 登录无组", VisibilityGroup, []uuid.UUID{gIn}, viewer{LoggedIn: true}, PlanActive, false},
		{"group 组内", VisibilityGroup, []uuid.UUID{gIn}, viewer{LoggedIn: true, GroupID: &gIn}, PlanActive, true},
		{"group 组外", VisibilityGroup, []uuid.UUID{gIn}, viewer{LoggedIn: true, GroupID: &gOut}, PlanActive, false},
		// hidden 与未知值
		{"hidden 登录也看不见", VisibilityHidden, nil, viewer{LoggedIn: true}, PlanActive, false},
		{"未知可见性", "bogus", nil, viewer{LoggedIn: true}, PlanActive, false},
		// 非 active 状态
		{"draft 不可见", VisibilityPublic, nil, viewer{LoggedIn: true}, PlanDraft, false},
		{"archived 不可见", VisibilityPublic, nil, viewer{LoggedIn: true}, PlanArchived, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := activeSubscriptionPlan()
			p.Visibility = tc.visibility
			p.VisibleGroupIDs = tc.groups
			p.Status = tc.status
			if got := visibleTo(p, tc.viewer); got != tc.want {
				t.Fatalf("visibleTo = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanRefusal(t *testing.T) {
	gIn := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	gOut := uuid.MustParse("018e0000-0000-7000-8000-000000000002")

	t.Run("续费一次性套餐回 ErrChangeNotAllowed", func(t *testing.T) {
		p := activeOneTimePlan()
		if err := planRefusal(p, UseRenewal, viewer{}, false); !errors.Is(err, ErrChangeNotAllowed) {
			t.Fatalf("got %v, want ErrChangeNotAllowed", err)
		}
	})
	t.Run("续费已归档报 ErrRenewalNotAllowed", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.Status = PlanArchived
		if err := planRefusal(p, UseRenewal, viewer{}, false); !errors.Is(err, ErrRenewalNotAllowed) {
			t.Fatalf("got %v, want ErrRenewalNotAllowed", err)
		}
	})
	t.Run("续费关续费报 ErrRenewalNotAllowed", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.AllowRenewal = false
		if err := planRefusal(p, UseRenewal, viewer{}, false); !errors.Is(err, ErrRenewalNotAllowed) {
			t.Fatalf("got %v, want ErrRenewalNotAllowed", err)
		}
	})
	t.Run("续费不看可见性（hidden 匿名也可续）", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.Visibility = VisibilityHidden
		if err := planRefusal(p, UseRenewal, viewer{}, false); err != nil {
			t.Fatalf("续费应成功，got %v", err)
		}
	})
	t.Run("新购不在售回 ErrNotFound", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.Status = PlanDraft
		if err := planRefusal(p, UseNew, viewer{}, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	})
	t.Run("新购不可见回 ErrNotFound", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.Visibility = VisibilityGroup
		p.VisibleGroupIDs = []uuid.UUID{gIn}
		if err := planRefusal(p, UseNew, viewer{LoggedIn: true, GroupID: &gOut}, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	})
	t.Run("Manual 跳过可见性", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.Visibility = VisibilityHidden
		if err := planRefusal(p, UseNew, viewer{}, true); err != nil {
			t.Fatalf("Manual 应跳过可见性，got %v", err)
		}
	})
	t.Run("新购关新购回 ErrNewPurchaseClosed", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.AllowNewPurchase = false
		if err := planRefusal(p, UseNew, viewer{}, false); !errors.Is(err, ErrNewPurchaseClosed) {
			t.Fatalf("got %v, want ErrNewPurchaseClosed", err)
		}
	})
	t.Run("Stack 用订阅套餐回 ErrStackNotAllowed", func(t *testing.T) {
		p := activeSubscriptionPlan()
		if err := planRefusal(p, UseStack, viewer{}, false); !errors.Is(err, ErrStackNotAllowed) {
			t.Fatalf("got %v, want ErrStackNotAllowed", err)
		}
	})
	t.Run("Stack 用一次性套餐通过", func(t *testing.T) {
		p := activeOneTimePlan()
		if err := planRefusal(p, UseStack, viewer{}, false); err != nil {
			t.Fatalf("got %v, want nil", err)
		}
	})
	t.Run("Change 关升级回 ErrChangeNotAllowed", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.AllowUpgrade = false
		if err := planRefusal(p, UseChange, viewer{}, false); !errors.Is(err, ErrChangeNotAllowed) {
			t.Fatalf("got %v, want ErrChangeNotAllowed", err)
		}
	})
	t.Run("没有当前版本回 ErrNoPublishedVersion", func(t *testing.T) {
		p := activeSubscriptionPlan()
		p.CurrentVersionID = nil
		if err := planRefusal(p, UseNew, viewer{}, false); !errors.Is(err, ErrNoPublishedVersion) {
			t.Fatalf("got %v, want ErrNoPublishedVersion", err)
		}
		if err := planRefusal(p, UseRenewal, viewer{}, false); !errors.Is(err, ErrNoPublishedVersion) {
			t.Fatalf("got %v, want ErrNoPublishedVersion", err)
		}
	})
	t.Run("未知用途回 internal 错误", func(t *testing.T) {
		p := activeSubscriptionPlan()
		err := planRefusal(p, Use("bogus"), viewer{}, false)
		if err == nil || errs.As(err).Code != errs.Internal {
			t.Fatalf("got %v, want code=internal", err)
		}
	})
}

func TestPriceRefusal(t *testing.T) {
	t.Run("在售价格对各种用途都可用", func(t *testing.T) {
		p := activePrice(KindSubscription)
		for _, use := range []Use{UseNew, UseRenewal, UseChange, UseStack} {
			if err := priceRefusal(p, use); err != nil {
				t.Fatalf("use=%s got %v", use, err)
			}
		}
	})
	t.Run("归档价格续费报 ErrRenewPriceGone", func(t *testing.T) {
		p := activePrice(KindSubscription)
		p.Status = PriceArchived
		if err := priceRefusal(p, UseRenewal); !errors.Is(err, ErrRenewPriceGone) {
			t.Fatalf("got %v, want ErrRenewPriceGone", err)
		}
	})
	t.Run("归档价格新购报 ErrPriceArchived", func(t *testing.T) {
		p := activePrice(KindSubscription)
		p.Status = PriceArchived
		if err := priceRefusal(p, UseNew); !errors.Is(err, ErrPriceArchived) {
			t.Fatalf("got %v, want ErrPriceArchived", err)
		}
	})
}

func TestUsablePrices(t *testing.T) {
	p1 := activePrice(KindSubscription)
	p2 := activePrice(KindSubscription)
	p2.Status = PriceArchived

	t.Run("只保留在售价格", func(t *testing.T) {
		out := usablePrices([]Price{p1, p2}, UseNew)
		if len(out) != 1 || out[0].ID != p1.ID {
			t.Fatalf("out=%v", out)
		}
	})
	t.Run("空输入返回非 nil 空切片", func(t *testing.T) {
		out := usablePrices(nil, UseNew)
		if out == nil || len(out) != 0 {
			t.Fatalf("out=%v", out)
		}
	})
	t.Run("全归档返回空切片", func(t *testing.T) {
		out := usablePrices([]Price{p2}, UseNew)
		if out == nil || len(out) != 0 {
			t.Fatalf("out=%v", out)
		}
	})
}
