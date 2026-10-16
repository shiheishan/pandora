package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// provisionOneTime 直接开通一份一次性套餐订阅（叠加、落点等用例的前置）。
func provisionOneTime(t *testing.T, ctx context.Context, e *env, user, planID, versionID, priceID uuid.UUID, cents int64, days *int) subsvc.Subscription {
	t.Helper()
	pid := priceID
	sub, err := e.subs.Provision(ctx, subsvc.Provision{
		UserID:        user,
		PlanID:        planID,
		PlanVersionID: versionID,
		Price: subsvc.PriceSnapshot{
			PriceID:       &pid,
			Amount:        money.FromCents(cents),
			Interval:      "one_time",
			IntervalCount: 1,
			ValidityDays:  days,
		},
		Actor: subsvc.Actor{Kind: subsvc.ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通一次性订阅: %v", err)
	}
	return sub
}

// assertLandings 逐项比对落点的模式与订阅。
func assertLandings(t *testing.T, got Landings, want []Landing) {
	t.Helper()
	if len(got.Options) != len(want) {
		t.Fatalf("落点 %d 项 %+v，期望 %d 项", len(got.Options), got.Options, len(want))
	}
	for i := range want {
		if got.Options[i].Mode != want[i].Mode {
			t.Fatalf("第 %d 项 Mode=%s，期望 %s", i, got.Options[i].Mode, want[i].Mode)
		}
		if want[i].SubscriptionID == nil {
			if got.Options[i].SubscriptionID != nil {
				t.Fatalf("第 %d 项 SubscriptionID=%v，期望 nil", i, got.Options[i].SubscriptionID)
			}
		} else if got.Options[i].SubscriptionID == nil || *got.Options[i].SubscriptionID != *want[i].SubscriptionID {
			t.Fatalf("第 %d 项 SubscriptionID=%v，期望 %s", i, got.Options[i].SubscriptionID, *want[i].SubscriptionID)
		}
	}
}

// 叠加单支付履约：报价、建单、回调后流量上限翻倍、已用不变、期末顺延 30 天。
func TestStackOrderSettles(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
	sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))

	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`,
		40*gib, sub.ID)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteStack, SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatalf("叠加报价: %v", err)
	}
	if len(res.Quotes) != 1 {
		t.Fatalf("报价 %d 条，期望 1", len(res.Quotes))
	}
	q := res.Quotes[0]
	mustEq(t, "Subtotal", q.Subtotal, dec("10"))
	if q.PeriodEnd == nil {
		t.Fatal("PeriodEnd 不应为 nil")
	}
	wantEnd := sub.PeriodEnd.AddDate(0, 0, 30)
	if d := q.PeriodEnd.Sub(wantEnd); d < -time.Second || d > time.Second {
		t.Fatalf("报价期末 %s 与期望 %s 偏差 %s", q.PeriodEnd, wantEnd, d)
	}

	created, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("建叠加单: %v", err)
	}
	if created.Status != StatusPending {
		t.Fatalf("状态 %s，期望 pending_payment", created.Status)
	}
	o := e.order(t, ctx, created.OrderID)
	if o.Kind != KindStack || o.SubscriptionID == nil || *o.SubscriptionID != sub.ID {
		t.Fatalf("订单 kind=%s subscription_id=%v", o.Kind, o.SubscriptionID)
	}

	code := payCode()
	e.mkPayProvider(t, ctx, code)
	ack := e.payAndNotify(t, ctx, user, created.OrderID, code, "stk-"+randHex(8))
	if !ackOK(ack) {
		t.Fatalf("回调: %s", ack.Body)
	}
	if e.order(t, ctx, created.OrderID).Status != StatusFulfilled {
		t.Fatal("付款后应 fulfilled")
	}

	var limitVal *int64
	var consumedVal int64
	if err := e.row(ctx, `SELECT limit_value, consumed FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`, sub.ID).
		Scan(&limitVal, &consumedVal); err != nil {
		t.Fatal(err)
	}
	if limitVal == nil || *limitVal != 200*gib {
		t.Fatalf("total limit_value=%v，期望 200 GiB", limitVal)
	}
	if consumedVal != 40*gib {
		t.Fatalf("consumed=%d，期望 40 GiB", consumedVal)
	}

	subAfter, err := e.subs.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := subAfter.PeriodEnd.Sub(wantEnd); d < -time.Second || d > time.Second {
		t.Fatalf("订阅期末 %s 与期望 %s 偏差 %s", subAfter.PeriodEnd, wantEnd, d)
	}
	e.assertNoDrift(t, ctx)
}

// 叠加单：余额全额付当场履约；订阅套餐、别人的订阅、已有待付叠加单都被拒。
func TestStackOrderZeroPayAndRejects(t *testing.T) {
	t.Run("余额全额付当场履约", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
		sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))
		e.giveBalance(t, ctx, user, 1000)

		c, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{
			SubscriptionID: sub.ID,
			Checkout:       Checkout{UseBalance: dec("10")},
		})
		if err != nil {
			t.Fatalf("建单: %v", err)
		}
		if c.Status != StatusFulfilled {
			t.Fatalf("状态 %s，期望 fulfilled", c.Status)
		}
		var limitVal *int64
		if err := e.row(ctx, `SELECT limit_value FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`, sub.ID).
			Scan(&limitVal); err != nil {
			t.Fatal(err)
		}
		if limitVal == nil || *limitVal != 200*gib {
			t.Fatalf("上限 %v，期望 200 GiB", limitVal)
		}
	})

	t.Run("订阅套餐不能叠加", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month")
		sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)

		_, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID})
		if err == nil {
			t.Fatal("期望报错")
		}
		if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE subscription_id = ? AND kind = 'stack'`, sub.ID); n != 0 {
			t.Fatalf("不应留 stack 订单，实际 %d", n)
		}
	})

	t.Run("别人的订阅", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		other := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
		sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))

		_, err := e.svc.CreateStackOrder(asUser(ctx, other), StackOrderInput{SubscriptionID: sub.ID})
		if codeOf(err) != errs.NotFound {
			t.Fatalf("错误码 %v，期望 not_found", codeOf(err))
		}
	})

	t.Run("已有待付叠加单", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
		sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))

		first, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID})
		if err != nil {
			t.Fatalf("第一张叠加单: %v", err)
		}
		if first.Status != StatusPending {
			t.Fatalf("第一张状态 %s", first.Status)
		}

		_, err = e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID})
		if codeOf(err) != errs.Conflict {
			t.Fatalf("第二张错误码 %v，期望 conflict", codeOf(err))
		}
	})
}

// 落点：new 恒在最前；同款一次性给 stack、同款订阅给 renew、别的套餐给 change；挂着未完结单的那份不列。
func TestLandings(t *testing.T) {
	t.Run("无订阅只有新开", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, _, _ := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))

		got, err := e.svc.Landings(asUser(ctx, user), planID)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{{Mode: LandingNew}})
	})

	t.Run("同款一次性可叠加", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
		sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))

		got, err := e.svc.Landings(asUser(ctx, user), planID)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{
			{Mode: LandingNew},
			{Mode: LandingStack, SubscriptionID: &sub.ID},
		})
	})

	t.Run("同款订阅可续费", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month")
		sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)

		got, err := e.svc.Landings(asUser(ctx, user), planID)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{
			{Mode: LandingNew},
			{Mode: LandingRenew, SubscriptionID: &sub.ID},
		})

		e.mustExec(t, ctx, `UPDATE plans SET allow_renewal = false WHERE id = ?`, planID)
		got, err = e.svc.Landings(asUser(ctx, user), planID)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{{Mode: LandingNew}})
	})

	t.Run("别的套餐可换掉", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planA, versionA, priceA := e.mkPlan(t, ctx, 1000, "month")
		sub := e.provision(t, ctx, user, planA, versionA, priceA, 1000)
		planB, _, _ := e.mkPlan(t, ctx, 2000, "month")

		got, err := e.svc.Landings(asUser(ctx, user), planB)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{
			{Mode: LandingNew},
			{Mode: LandingChange, SubscriptionID: &sub.ID},
		})

		e.mustExec(t, ctx, `UPDATE plans SET allow_upgrade = false WHERE id = ?`, planB)
		got, err = e.svc.Landings(asUser(ctx, user), planB)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{{Mode: LandingNew}})
	})

	t.Run("待付叠加单不列叠加落点", func(t *testing.T) {
		e, ctx := newEnv(t)
		user := e.mkUser(t, ctx)
		planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
		sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))

		if _, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID}); err != nil {
			t.Fatalf("建待付叠加单: %v", err)
		}

		got, err := e.svc.Landings(asUser(ctx, user), planID)
		if err != nil {
			t.Fatal(err)
		}
		assertLandings(t, got, []Landing{{Mode: LandingNew}})
	})
}

// 订阅套餐换成一次性套餐：报价带有效天数，付清后 plan_id 与 total 流量行正确。
func TestPlanChangeCrossKind(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planSub, verSub, priceSub := e.mkPlan(t, ctx, 3000, "month")
	sub := e.provision(t, ctx, user, planSub, verSub, priceSub, 3000)
	targetPlan, _, targetPrice := e.mkOneTimePlan(t, ctx, 5000, 100*gib, intp(30))

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{
		Action:         QuoteChange,
		SubscriptionID: &sub.ID,
		PlanID:         &targetPlan,
	})
	if err != nil {
		t.Fatalf("换套餐报价: %v", err)
	}
	if len(res.Quotes) < 1 {
		t.Fatalf("报价 %d 条，期望至少 1", len(res.Quotes))
	}
	q := res.Quotes[0]
	if q.PlanID == nil || *q.PlanID != targetPlan {
		t.Fatalf("PlanID %v，期望 %s", q.PlanID, targetPlan)
	}
	if q.ValidityDays == nil || *q.ValidityDays != 30 {
		t.Fatalf("ValidityDays %v，期望 30", q.ValidityDays)
	}

	e.giveBalance(t, ctx, user, 5000)
	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID,
		PlanID:         targetPlan,
		PriceID:        targetPrice,
		Checkout:       Checkout{UseBalance: dec("50")},
	})
	if err != nil {
		t.Fatalf("换套餐建单: %v", err)
	}
	if c.Status != StatusFulfilled {
		t.Fatalf("状态 %s，期望 fulfilled", c.Status)
	}

	subAfter, err := e.subs.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if subAfter.PlanID != targetPlan {
		t.Fatalf("plan_id %s，期望 %s", subAfter.PlanID, targetPlan)
	}

	var totalLimit *int64
	if err := e.row(ctx, `SELECT limit_value FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`, sub.ID).
		Scan(&totalLimit); err != nil {
		t.Fatalf("total 行: %v", err)
	}
	if totalLimit == nil || *totalLimit != 100*gib {
		t.Fatalf("total limit_value=%v，期望 100 GiB", totalLimit)
	}

	var cycleLimit sql.NullInt64
	err = e.row(ctx, `SELECT limit_value FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`, sub.ID).
		Scan(&cycleLimit)
	if errors.Is(err, sql.ErrNoRows) {
		// rebuildQuotas 若将来删掉旧 cycle 行，也算通过
	} else if err != nil {
		t.Fatal(err)
	} else if cycleLimit.Valid {
		// 当前实现保留 cycle 行并把 limit_value 置空（不限）
		t.Fatalf("遗留 cycle 行 limit_value=%d，期望 NULL", cycleLimit.Int64)
	}
}
