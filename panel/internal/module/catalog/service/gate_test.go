package service

import (
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var gateNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func activePlan() Plan {
	ver := uuid.New()
	return Plan{
		Status:           PlanActive,
		Visibility:       VisibilityPublic,
		CurrentVersionID: &ver,
		AllowNewPurchase: true,
		AllowRenewal:     true,
		AllowUpgrade:     true,
	}
}

func activePrice() Price {
	return Price{
		Status:     PriceActive,
		Currency:   money.CNY,
		UnitAmount: decimal.New(1, 0),
	}
}

func TestVisibleTo(t *testing.T) {
	gIn := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	gOut := uuid.MustParse("018e0000-0000-7000-8000-000000000002")
	future := gateNow.Add(time.Hour)

	cases := []struct {
		name       string
		visibility string
		groups     []uuid.UUID
		viewer     viewer
		mut        func(*Plan)
		want       bool
	}{
		// public
		{"public 匿名", VisibilityPublic, nil, viewer{}, nil, true},
		{"public 登录无组", VisibilityPublic, nil, viewer{LoggedIn: true}, nil, true},
		{"public 组内", VisibilityPublic, nil, viewer{LoggedIn: true, GroupID: &gIn}, nil, true},
		{"public 组外", VisibilityPublic, nil, viewer{LoggedIn: true, GroupID: &gOut}, nil, true},
		// authenticated
		{"auth 匿名", VisibilityAuthenticated, nil, viewer{}, nil, false},
		{"auth 登录无组", VisibilityAuthenticated, nil, viewer{LoggedIn: true}, nil, true},
		{"auth 组内", VisibilityAuthenticated, nil, viewer{LoggedIn: true, GroupID: &gIn}, nil, true},
		{"auth 组外", VisibilityAuthenticated, nil, viewer{LoggedIn: true, GroupID: &gOut}, nil, true},
		// group
		{"group 匿名", VisibilityGroup, []uuid.UUID{gIn}, viewer{}, nil, false},
		{"group 登录无组", VisibilityGroup, []uuid.UUID{gIn}, viewer{LoggedIn: true}, nil, false},
		{"group 组内", VisibilityGroup, []uuid.UUID{gIn}, viewer{LoggedIn: true, GroupID: &gIn}, nil, true},
		{"group 组外", VisibilityGroup, []uuid.UUID{gIn}, viewer{LoggedIn: true, GroupID: &gOut}, nil, false},
		// invite_only / hidden（目录里与 hidden 一样不可见）
		{"invite 匿名", VisibilityInviteOnly, nil, viewer{}, nil, false},
		{"invite 登录", VisibilityInviteOnly, nil, viewer{LoggedIn: true}, nil, false},
		{"hidden 登录", VisibilityHidden, nil, viewer{LoggedIn: true}, nil, false},
		// draft
		{"draft", VisibilityPublic, nil, viewer{}, func(p *Plan) { p.Status = PlanDraft }, false},
		// 窗口
		{"VisibleFrom 未来", VisibilityPublic, nil, viewer{}, func(p *Plan) { p.VisibleFrom = &future }, false},
		{"VisibleUntil 等于 now", VisibilityPublic, nil, viewer{}, func(p *Plan) { p.VisibleUntil = &gateNow }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := activePlan()
			p.Visibility = tc.visibility
			p.VisibleGroupIDs = tc.groups
			if tc.mut != nil {
				tc.mut(&p)
			}
			got := visibleTo(p, tc.viewer, gateNow)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestPlanRefusal(t *testing.T) {
	t.Run("UseNew draft", func(t *testing.T) {
		p := activePlan()
		p.Status = PlanDraft
		if !errors.Is(planRefusal(p, UseNew, viewer{}, false, gateNow), ErrNotFound) {
			t.Fatal("want ErrNotFound")
		}
	})
	t.Run("UseNew hidden", func(t *testing.T) {
		p := activePlan()
		p.Visibility = VisibilityHidden
		if !errors.Is(planRefusal(p, UseNew, viewer{}, false, gateNow), ErrNotFound) {
			t.Fatal("want ErrNotFound")
		}
		if err := planRefusal(p, UseNew, viewer{}, true, gateNow); err != nil {
			t.Fatalf("manual: %v", err)
		}
	})
	t.Run("UseNew 关新购", func(t *testing.T) {
		p := activePlan()
		p.AllowNewPurchase = false
		if !errors.Is(planRefusal(p, UseNew, viewer{}, false, gateNow), ErrNewPurchaseClosed) {
			t.Fatal("want ErrNewPurchaseClosed")
		}
	})
	t.Run("UseNew 无发布版本", func(t *testing.T) {
		p := activePlan()
		p.CurrentVersionID = nil
		if !errors.Is(planRefusal(p, UseNew, viewer{}, false, gateNow), ErrNoPublishedVersion) {
			t.Fatal("want ErrNoPublishedVersion")
		}
	})
	t.Run("UseChange 关变更", func(t *testing.T) {
		p := activePlan()
		p.AllowUpgrade = false
		if !errors.Is(planRefusal(p, UseChange, viewer{LoggedIn: true}, false, gateNow), ErrChangeNotAllowed) {
			t.Fatal("want ErrChangeNotAllowed")
		}
	})
	t.Run("UseChange group 组外", func(t *testing.T) {
		gIn := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
		gOut := uuid.MustParse("018e0000-0000-7000-8000-000000000002")
		p := activePlan()
		p.Visibility = VisibilityGroup
		p.VisibleGroupIDs = []uuid.UUID{gIn}
		if !errors.Is(planRefusal(p, UseChange, viewer{LoggedIn: true, GroupID: &gOut}, false, gateNow), ErrNotFound) {
			t.Fatal("want ErrNotFound")
		}
	})
	t.Run("UseRenewal archived hidden 允许续费", func(t *testing.T) {
		p := activePlan()
		p.Status = PlanArchived
		p.Visibility = VisibilityHidden
		p.AllowRenewal = true
		if err := planRefusal(p, UseRenewal, viewer{}, false, gateNow); err != nil {
			t.Fatalf("want nil: %v", err)
		}
	})
	t.Run("UseRenewal 不允许", func(t *testing.T) {
		p := activePlan()
		p.AllowRenewal = false
		if !errors.Is(planRefusal(p, UseRenewal, viewer{}, false, gateNow), ErrRenewalNotAllowed) {
			t.Fatal("want ErrRenewalNotAllowed")
		}
	})
	t.Run("UseRenewal 无发布版本", func(t *testing.T) {
		p := activePlan()
		p.CurrentVersionID = nil
		if !errors.Is(planRefusal(p, UseRenewal, viewer{}, false, gateNow), ErrNoPublishedVersion) {
			t.Fatal("want ErrNoPublishedVersion")
		}
	})
	t.Run("未知用途", func(t *testing.T) {
		p := activePlan()
		err := planRefusal(p, Use("bogus"), viewer{}, false, gateNow)
		if err == nil {
			t.Fatal("want error")
		}
		if errs.As(err).Code != errs.Internal {
			t.Fatalf("code=%s", errs.As(err).Code)
		}
	})
}

func TestPriceRefusal(t *testing.T) {
	gid := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	other := uuid.MustParse("018e0000-0000-7000-8000-000000000002")
	past := gateNow.Add(-time.Hour)

	t.Run("组价组外", func(t *testing.T) {
		p := activePrice()
		p.UserGroupID = &gid
		if !errors.Is(priceRefusal(p, UseNew, viewer{LoggedIn: true, GroupID: &other}, false, gateNow), ErrNotFound) {
			t.Fatal("want ErrNotFound")
		}
	})
	t.Run("组价 manual", func(t *testing.T) {
		p := activePrice()
		p.UserGroupID = &gid
		if err := priceRefusal(p, UseNew, viewer{LoggedIn: true, GroupID: &other}, true, gateNow); err != nil {
			t.Fatalf("manual: %v", err)
		}
	})
	t.Run("归档", func(t *testing.T) {
		p := activePrice()
		p.Status = PriceArchived
		if !errors.Is(priceRefusal(p, UseNew, viewer{}, false, gateNow), ErrPriceArchived) {
			t.Fatal("want ErrPriceArchived")
		}
	})
	t.Run("过期", func(t *testing.T) {
		p := activePrice()
		p.ValidUntil = &past
		if !errors.Is(priceRefusal(p, UseNew, viewer{}, false, gateNow), ErrPriceOutOfWindow) {
			t.Fatal("want ErrPriceOutOfWindow")
		}
	})
	t.Run("续费三种映射 RenewPriceGone", func(t *testing.T) {
		arch := activePrice()
		arch.Status = PriceArchived
		if !errors.Is(priceRefusal(arch, UseRenewal, viewer{}, false, gateNow), ErrRenewPriceGone) {
			t.Fatal("archived renewal")
		}
		exp := activePrice()
		exp.ValidUntil = &past
		if !errors.Is(priceRefusal(exp, UseRenewal, viewer{}, false, gateNow), ErrRenewPriceGone) {
			t.Fatal("window renewal")
		}
		grp := activePrice()
		grp.UserGroupID = &gid
		if !errors.Is(priceRefusal(grp, UseRenewal, viewer{LoggedIn: true, GroupID: &other}, false, gateNow), ErrRenewPriceGone) {
			t.Fatal("group renewal")
		}
	})
	t.Run("组外且归档组优先", func(t *testing.T) {
		p := activePrice()
		p.UserGroupID = &gid
		p.Status = PriceArchived
		if !errors.Is(priceRefusal(p, UseNew, viewer{LoggedIn: true, GroupID: &other}, false, gateNow), ErrNotFound) {
			t.Fatal("want ErrNotFound")
		}
	})
}

func TestUsablePrices(t *testing.T) {
	gid := uuid.MustParse("018e0000-0000-7000-8000-000000000002")
	other := uuid.MustParse("018e0000-0000-7000-8000-000000000003")
	past := gateNow.Add(-time.Hour)

	pGroup := activePrice()
	pGroup.UserGroupID = &gid
	pGroup.UnitAmount = decimal.New(1, 0)
	pPublic := activePrice()
	pPublic.UnitAmount = decimal.New(2, 0)
	pExpired := activePrice()
	pExpired.UnitAmount = decimal.New(3, 0)
	pExpired.ValidUntil = &past
	prices := []Price{pGroup, pPublic, pExpired}

	out := usablePrices(prices, UseNew, viewer{LoggedIn: true, GroupID: &other}, false, gateNow)
	if out == nil {
		t.Fatal("不应 nil")
	}
	if len(out) != 1 || !out[0].UnitAmount.Equal(decimal.New(2, 0)) {
		t.Fatalf("got %v", out)
	}
	out2 := usablePrices(prices, UseNew, viewer{LoggedIn: true, GroupID: &gid}, false, gateNow)
	if len(out2) != 2 {
		t.Fatalf("want 2, got %d", len(out2))
	}
	if !out2[0].UnitAmount.Equal(decimal.New(1, 0)) || !out2[1].UnitAmount.Equal(decimal.New(2, 0)) {
		t.Fatalf("order: %+v", out2)
	}
	empty := usablePrices(nil, UseNew, viewer{}, false, gateNow)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty=%v", empty)
	}
}
