package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// TestGrantBonusPG 送的流量按套餐类型落在 cycle / total 行，可累加，返回加后的 bonus。
func TestGrantBonusPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)

	t.Run("订阅套餐加在 cycle 行并可累加", func(t *testing.T) {
		planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
		sub := provision(t, e, ctx, user, planID, versionID, "")

		b1, err := e.svc.GrantBonus(ctx, BonusGrant{
			SubscriptionID: sub.ID,
			Bytes:          10 * gib,
			Source:         BonusGiftCard,
		})
		if err != nil || b1 != 10*gib {
			t.Fatalf("第一次: bytes=%d err=%v", b1, err)
		}
		cycle := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
		if cycle.Bonus != 10*gib {
			t.Fatalf("cycle bonus: %+v", cycle)
		}

		b2, err := e.svc.GrantBonus(ctx, BonusGrant{
			SubscriptionID: sub.ID,
			Bytes:          5 * gib,
			Source:         BonusGiftCard,
		})
		if err != nil || b2 != 15*gib {
			t.Fatalf("累加: bytes=%d err=%v", b2, err)
		}
	})

	t.Run("一次性套餐加在 total 行", func(t *testing.T) {
		planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(80 * gib)})
		sub, err := e.svc.Provision(ctx, Provision{
			UserID: user, PlanID: planID, PlanVersionID: versionID,
			Price: oneTime(intp(14)), Actor: Actor{Kind: ActorSystem},
		})
		if err != nil {
			t.Fatal(err)
		}
		bonus, err := e.svc.GrantBonus(ctx, BonusGrant{
			SubscriptionID: sub.ID,
			Bytes:          3 * gib,
			Source:         BonusGiftCard,
		})
		if err != nil || bonus != 3*gib {
			t.Fatalf("GrantBonus: %d %v", bonus, err)
		}
		q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
		if q.Bonus != 3*gib {
			t.Fatalf("total bonus: %+v", q)
		}
	})
}

// TestGrantBonusRejectPG 不限量、已过期、后台缺操作人、字节数非正都被拒。
func TestGrantBonusRejectPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)

	cases := []struct {
		name string
		prep func(t *testing.T) (uuid.UUID, BonusGrant)
		want error
	}{
		{
			name: "不限量",
			prep: func(t *testing.T) (uuid.UUID, BonusGrant) {
				planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, nil})
				sub := provision(t, e, ctx, user, planID, versionID, "")
				return sub.ID, BonusGrant{SubscriptionID: sub.ID, Bytes: gib, Source: BonusGiftCard}
			},
			want: ErrUnlimited,
		},
		{
			name: "已过期",
			prep: func(t *testing.T) (uuid.UUID, BonusGrant) {
				planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
				sub := provision(t, e, ctx, user, planID, versionID, "")
				e.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '31 days', current_period_end = now() - interval '1 day' WHERE id = ?`, sub.ID)
				return sub.ID, BonusGrant{SubscriptionID: sub.ID, Bytes: gib, Source: BonusGiftCard}
			},
			want: ErrNotActive,
		},
		{
			// 本期已结束、还没滚动（订阅提前续过，期末在后面）：送进去会被滚动清零，拒绝（审查 1016-A2 应改 2）
			name: "周期切换中",
			prep: func(t *testing.T) (uuid.UUID, BonusGrant) {
				planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
				sub := provision(t, e, ctx, user, planID, versionID, "")
				e.exec(t, ctx, `UPDATE quota_balances SET period_start = now() - interval '31 days', period_end = now() - interval '1 minute' WHERE subscription_id = ? AND period = 'cycle'`, sub.ID)
				return sub.ID, BonusGrant{SubscriptionID: sub.ID, Bytes: gib, Source: BonusGiftCard}
			},
			want: ErrBonusPeriodSwitching,
		},
		{
			name: "后台缺 ActorID",
			prep: func(t *testing.T) (uuid.UUID, BonusGrant) {
				planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
				sub := provision(t, e, ctx, user, planID, versionID, "")
				return sub.ID, BonusGrant{SubscriptionID: sub.ID, Bytes: gib, Source: BonusAdmin}
			},
			want: nil, // internal
		},
		{
			name: "Bytes 非正",
			prep: func(t *testing.T) (uuid.UUID, BonusGrant) {
				planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
				sub := provision(t, e, ctx, user, planID, versionID, "")
				return sub.ID, BonusGrant{SubscriptionID: sub.ID, Bytes: 0, Source: BonusGiftCard}
			},
			want: nil, // validation
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subID, grant := tc.prep(t)
			grant.SubscriptionID = subID
			_, err := e.svc.GrantBonus(ctx, grant)
			switch tc.name {
			case "后台缺 ActorID":
				if errs.As(err).Code != errs.Internal {
					t.Fatalf("期望 internal: %v", err)
				}
			case "Bytes 非正":
				if errs.As(err).Code != errs.ValidationFailed {
					t.Fatalf("期望 validation: %v", err)
				}
			default:
				if !errors.Is(err, tc.want) {
					t.Fatalf("期望 %v: %v", tc.want, err)
				}
			}
		})
	}
}

// TestAdminGrantTrafficPG 后台加流量：结果带本次与累计送量，记一条审计。
func TestAdminGrantTrafficPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")

	out, err := e.svc.AdminGrantTraffic(actx, sub.ID, 10*gib, "测试加流量原因")
	if err != nil {
		t.Fatal(err)
	}
	if out.GrantedBytes != 10*gib || out.BonusBytes != 10*gib {
		t.Fatalf("结果: %+v", out)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionTrafficGranted, sub.ID); n != 1 {
		t.Fatalf("审计 traffic_granted %d 条", n)
	}
}
