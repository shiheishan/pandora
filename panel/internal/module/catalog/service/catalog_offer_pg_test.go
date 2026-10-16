package service

// 门户目录、下单快照与用户组外键的 PG 测试。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPortalCatalogVisibility(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := seedGroup(t, ctx, e, "")

	public := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "公开套餐"})
	authOnly := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:       "登录可见",
		Visibility: VisibilityAuthenticated,
	})
	grpOnly := groupOnlyPublishedPlan(t, ctx, e, g)
	hidden := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "隐藏套餐", Visibility: VisibilityHidden})

	oneTime := publishedPlan(t, ctx, e, publishedPlanOpts{
		Kind:    KindOneTime,
		Name:    "一次性门户",
		OneTime: &OneTimeInput{UnitAmount: decimal.RequireFromString("19.90"), ValidityDays: nil},
	})

	older := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "普通早建"})
	time.Sleep(2 * time.Millisecond)
	rec := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "推荐套餐"})
	rec, err = e.svc.UpdateSales(ctx, rec.ID, SalesUpdate{
		ExpectedRowVersion: rec.RowVersion,
		Sales: SalesInput{
			Visibility:       VisibilityPublic,
			AllowNewPurchase: true,
			AllowRenewal:     true,
			AllowUpgrade:     true,
			Recommended:      true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	anon, err := e.svc.PortalCatalog(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !portalHasPlan(anon, public.ID) {
		t.Fatal("匿名应看到 public 套餐")
	}
	if portalHasPlan(anon, authOnly.ID) {
		t.Fatal("匿名不应看到 authenticated 套餐")
	}
	if portalHasPlan(anon, grpOnly.ID) {
		t.Fatal("匿名不应看到 group 套餐")
	}
	if portalHasPlan(anon, hidden.ID) {
		t.Fatal("匿名不应看到 hidden 套餐")
	}
	if !portalHasPlan(anon, oneTime.ID) {
		t.Fatal("匿名应看到一次性套餐")
	}

	in := seedUser(t, ctx, e, &g)
	inGrp, err := e.svc.PortalCatalog(ctx, &in)
	if err != nil {
		t.Fatal(err)
	}
	if !portalHasPlan(inGrp, authOnly.ID) {
		t.Fatal("登录用户应看到 authenticated 套餐")
	}
	if !portalHasPlan(inGrp, grpOnly.ID) {
		t.Fatal("组内用户应看到 group 套餐")
	}

	out := seedUser(t, ctx, e, nil)
	outGrp, err := e.svc.PortalCatalog(ctx, &out)
	if err != nil {
		t.Fatal(err)
	}
	if portalHasPlan(outGrp, grpOnly.ID) {
		t.Fatal("组外用户不应看到 group 套餐")
	}

	assertPortalOrder(t, anon, rec.ID, older.ID)
	assertPortalOrder(t, inGrp, rec.ID, older.ID)

	draft := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "草稿不出", Draft: true})
	list, _ := e.svc.PortalCatalog(ctx, nil)
	if portalHasPlan(list, draft.ID) {
		t.Fatal("草稿套餐不应出现在门户")
	}
}

func assertPortalOrder(t *testing.T, list []OfferPlan, recommendedID, olderID uuid.UUID) {
	t.Helper()
	ri, oi := -1, -1
	for i, o := range list {
		if o.Plan.ID == recommendedID {
			ri = i
		}
		if o.Plan.ID == olderID {
			oi = i
		}
	}
	if ri < 0 || oi < 0 {
		t.Fatalf("门户列表缺推荐或对照套餐: rec=%d older=%d", ri, oi)
	}
	if ri >= oi {
		t.Fatalf("推荐套餐应排在普通套餐前: rec=%d older=%d", ri, oi)
	}
}

func TestPortalSkipsNoActivePrice(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	noprice := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "无价"})
	if err := e.exec(ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE plan_id = ?`, noprice.ID); err != nil {
		t.Fatal(err)
	}

	closed := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:             "关新购",
		AllowNewPurchase: boolPtr(false),
	})

	list, err := e.svc.PortalCatalog(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if portalHasPlan(list, noprice.ID) {
		t.Fatal("无在售价套餐不应出现")
	}
	if !portalHasPlan(list, closed.ID) {
		t.Fatal("关新购仍应出现在目录（只是不能新购）")
	}
}

func TestOfferGates(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	buyer := seedUser(t, ctx, e, nil)
	uid := &buyer

	sub := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "订阅门控"})
	subPrice := sub.Prices[0].ID

	onetime := publishedPlan(t, ctx, e, publishedPlanOpts{
		Kind:    KindOneTime,
		Name:    "一次性门控",
		OneTime: &OneTimeInput{UnitAmount: decimal.RequireFromString("8.00"), ValidityDays: nil},
	})
	otPrice := onetime.Prices[0].ID

	closedNew := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:             "关新购门控",
		AllowNewPurchase: boolPtr(false),
	})
	closedNewPrice := closedNew.Prices[0].ID

	noUpgrade := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "关变更"})
	_, err := e.svc.UpdateSales(ctx, noUpgrade.ID, SalesUpdate{
		ExpectedRowVersion: noUpgrade.RowVersion,
		Sales: SalesInput{
			Visibility:       VisibilityPublic,
			AllowNewPurchase: true,
			AllowRenewal:     true,
			AllowUpgrade:     false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	noUpgrade, _ = e.svc.GetPlan(ctx, noUpgrade.ID)

	hidden := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "隐藏门控", Visibility: VisibilityHidden})
	hiddenPrice := hidden.Prices[0].ID

	arch := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "待归档"})
	archPrice := arch.Prices[0].ID
	archRV := arch.RowVersion
	_, err = e.svc.ArchivePlan(ctx, arch.ID, archRV)
	if err != nil {
		t.Fatal(err)
	}

	archPricePlan := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name: "归档价",
		Tiers: []TierInput{
			{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("9.90")},
			{BillingInterval: IntervalMonth, IntervalCount: 3, UnitAmount: decimal.RequireFromString("27.00")},
		},
	})
	var month1, month3 uuid.UUID
	for _, p := range archPricePlan.Prices {
		if p.Status != PriceActive {
			continue
		}
		if p.BillingInterval == IntervalMonth && p.IntervalCount == 1 {
			month1 = p.ID
		}
		if p.BillingInterval == IntervalMonth && p.IntervalCount == 3 {
			month3 = p.ID
		}
	}
	archPricePlan, err = e.svc.GetPlan(ctx, archPricePlan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.UpdatePrices(ctx, archPricePlan.ID, PricesUpdate{
		ExpectedRowVersion: archPricePlan.RowVersion,
		Tiers: []TierChange{
			{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: nil},
			{BillingInterval: IntervalMonth, IntervalCount: 3, UnitAmount: ptrDec("27.00")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	other := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "另一套餐"})
	otherPrice := other.Prices[0].ID

	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{
			name: "UseNew 正常",
			run: func() error {
				_, err := e.svc.Offer(ctx, subPrice, OfferRequest{Use: UseNew, UserID: uid})
				return err
			},
		},
		{
			name: "关新购",
			run: func() error {
				_, err := e.svc.Offer(ctx, closedNewPrice, OfferRequest{Use: UseNew, UserID: uid})
				return err
			},
			want: ErrNewPurchaseClosed,
		},
		{
			name: "UseStack 订阅不允许",
			run: func() error {
				_, err := e.svc.Offer(ctx, subPrice, OfferRequest{Use: UseStack, UserID: uid, PlanID: &sub.ID})
				return err
			},
			want: ErrStackNotAllowed,
		},
		{
			name: "UseStack 一次性正常",
			run: func() error {
				_, err := e.svc.Offer(ctx, otPrice, OfferRequest{Use: UseStack, UserID: uid, PlanID: &onetime.ID})
				return err
			},
		},
		{
			name: "续费用一次性价",
			run: func() error {
				_, err := e.svc.Offer(ctx, otPrice, OfferRequest{Use: UseRenewal, UserID: uid, PlanID: &onetime.ID})
				return err
			},
			want: ErrChangeNotAllowed,
		},
		{
			name: "续费归档套餐",
			run: func() error {
				_, err := e.svc.Offer(ctx, archPrice, OfferRequest{Use: UseRenewal, UserID: uid, PlanID: &arch.ID})
				return err
			},
			want: ErrRenewalNotAllowed,
		},
		{
			name: "续费归档价",
			run: func() error {
				_, err := e.svc.Offer(ctx, month1, OfferRequest{Use: UseRenewal, UserID: uid, PlanID: &archPricePlan.ID})
				return err
			},
			want: ErrRenewPriceGone,
		},
		{
			name: "新购归档价",
			run: func() error {
				_, err := e.svc.Offer(ctx, month1, OfferRequest{Use: UseNew, UserID: uid})
				return err
			},
			want: ErrPriceArchived,
		},
		{
			name: "UseChange 关升级",
			run: func() error {
				_, err := e.svc.Offer(ctx, noUpgrade.Prices[0].ID, OfferRequest{Use: UseChange, UserID: uid})
				return err
			},
			want: ErrChangeNotAllowed,
		},
		{
			name: "Manual 跳过可见性",
			run: func() error {
				_, err := e.svc.Offer(ctx, hiddenPrice, OfferRequest{Use: UseNew, Manual: true, UserID: uid})
				return err
			},
		},
		{
			name: "续费 PlanID 不符",
			run: func() error {
				_, err := e.svc.Offer(ctx, otherPrice, OfferRequest{Use: UseRenewal, UserID: uid, PlanID: &sub.ID})
				return err
			},
			want: ErrRenewPriceGone,
		},
		{
			name: "新购 PlanID 不符",
			run: func() error {
				_, err := e.svc.Offer(ctx, otherPrice, OfferRequest{Use: UseNew, UserID: uid, PlanID: &sub.ID})
				return err
			},
			want: ErrNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if tc.want == nil {
				if err != nil {
					t.Fatalf("期望成功，得到 %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("期望 %v，得到 %v", tc.want, err)
			}
		})
	}

	// 续费仍可用在售价
	_, err = e.svc.Offer(ctx, month3, OfferRequest{Use: UseRenewal, UserID: uid, PlanID: &archPricePlan.ID})
	if err != nil {
		t.Fatalf("月3 续费应成功: %v", err)
	}
}

func TestPlanOffersTierSort(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name: "多档排序",
		Tiers: []TierInput{
			{BillingInterval: IntervalYear, IntervalCount: 1, UnitAmount: decimal.RequireFromString("99.00")},
			{BillingInterval: IntervalMonth, IntervalCount: 6, UnitAmount: decimal.RequireFromString("49.00")},
			{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("9.90")},
			{BillingInterval: IntervalMonth, IntervalCount: 3, UnitAmount: decimal.RequireFromString("27.00")},
		},
	})

	op, err := e.svc.PlanOffers(ctx, plan.ID, OfferRequest{Use: UseNew, UserID: uuidPtr(seedUser(t, ctx, e, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if len(op.Prices) != 4 {
		t.Fatalf("期望 4 档价，得到 %d", len(op.Prices))
	}
	want := []struct {
		interval string
		count    int
	}{
		{IntervalMonth, 1}, {IntervalMonth, 3}, {IntervalMonth, 6}, {IntervalYear, 1},
	}
	for i, w := range want {
		p := op.Prices[i]
		if p.BillingInterval != w.interval || p.IntervalCount != w.count {
			t.Fatalf("下标 %d: 得到 %s/%d，期望 %s/%d", i, p.BillingInterval, p.IntervalCount, w.interval, w.count)
		}
	}
}

func TestListOffersUseChange(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	buyer := seedUser(t, ctx, e, nil)

	sub := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "换套餐订阅"})
	ot := publishedPlan(t, ctx, e, publishedPlanOpts{
		Kind:    KindOneTime,
		Name:    "换套餐一次性",
		OneTime: &OneTimeInput{UnitAmount: decimal.RequireFromString("15.00"), ValidityDays: nil},
	})
	closed := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "不许变更"})
	_, err := e.svc.UpdateSales(ctx, closed.ID, SalesUpdate{
		ExpectedRowVersion: closed.RowVersion,
		Sales: SalesInput{
			Visibility:       VisibilityPublic,
			AllowNewPurchase: true,
			AllowRenewal:     true,
			AllowUpgrade:     false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := e.svc.ListOffers(ctx, OfferRequest{Use: UseChange, UserID: &buyer})
	if err != nil {
		t.Fatal(err)
	}
	if !listHasPlan(list, sub.ID) || !listHasPlan(list, ot.ID) {
		t.Fatal("应列出订阅与一次性换套餐目标")
	}
	if listHasPlan(list, closed.ID) {
		t.Fatal("关了升级的套餐不应出现在换套餐列表")
	}
}

func listHasPlan(list []OfferPlan, id uuid.UUID) bool {
	for _, o := range list {
		if o.Plan.ID == id {
			return true
		}
	}
	return false
}

func TestDeleteUserGroupCascadesPlanVisibility(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := seedGroup(t, ctx, e, "")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{
		Visibility:      VisibilityGroup,
		VisibleGroupIDs: []uuid.UUID{g},
	})
	if err := e.exec(ctx, `DELETE FROM user_groups WHERE id = ?`, g); err != nil {
		t.Fatalf("只被套餐可见组引用的分组应能删除：%v", err)
	}
	if n := scanInt64(t, ctx, e, `SELECT count(*) FROM plan_visible_groups WHERE plan_id = ?`, plan.ID); n != 0 {
		t.Fatalf("可见组关联行应被级联删除，剩 %d", n)
	}
	got, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != VisibilityGroup || len(got.VisibleGroupIDs) != 0 {
		t.Fatalf("可见性应仍是 group 且组为空：%s %v", got.Visibility, got.VisibleGroupIDs)
	}
	other := seedGroup(t, ctx, e, "")
	viewer := seedUser(t, ctx, e, &other)
	list, err := e.svc.PortalCatalog(ctx, &viewer)
	if err != nil {
		t.Fatal(err)
	}
	if portalHasPlan(list, plan.ID) {
		t.Fatal("删掉最后一个可见组后套餐不应对任何人可见")
	}
}

func TestPriceReferencesAreRestrict(t *testing.T) {
	e, ctx := newEnv(t)
	total := scanInt64(t, ctx, e, `SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND confrelid = 'prices'::regclass`)
	if total < 3 {
		t.Fatalf("引用 prices 的外键应至少有订单项、订阅、礼品卡 3 条，得到 %d", total)
	}
	if n := scanInt64(t, ctx, e, `SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND confrelid = 'prices'::regclass AND confdeltype <> 'r'`); n != 0 {
		t.Fatalf("引用 prices 的外键必须全是 ON DELETE RESTRICT，有 %d 条不是", n)
	}
}

func groupOnlyPublishedPlan(t *testing.T, ctx context.Context, e *env, g uuid.UUID) PlanDetail {
	t.Helper()
	return publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:            "仅组可见",
		Visibility:      VisibilityGroup,
		VisibleGroupIDs: []uuid.UUID{g},
	})
}

func portalHasPlan(list []OfferPlan, id uuid.UUID) bool {
	for _, o := range list {
		if o.Plan.ID == id {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }

func ptrDec(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}
