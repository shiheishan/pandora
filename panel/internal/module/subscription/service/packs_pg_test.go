package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// endSub 把订阅改成已停用（过期且关窗）。
func endSub(t *testing.T, e *env, ctx context.Context, subID uuid.UUID) {
	t.Helper()
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '90 days',
		current_period_end = now() - interval '40 days', renewal_closed_at = now() WHERE id = ?`, subID)
}

func TestPacksPG(t *testing.T) {
	e := newEnv(t)
	ctx := testkit.TxCtx(t, e.d)
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	old := provision(t, e, ctx, user, planID, versionID, "旧的")
	live := provision(t, e, ctx, user, planID, versionID, "新的")

	src := uuid.New()
	g, err := e.svc.GrantTrafficPack(ctx, PackGrant{UserID: user, SubscriptionID: &old.ID, Source: PackSourceOrder, SourceID: src, Bytes: 10 * gib})
	if err != nil {
		t.Fatal(err)
	}
	// 唯一冲突会让事务失效：在保存点里试，试完回滚到保存点
	conn := e.d.Conn(ctx)
	if err := conn.SavePoint("dup").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GrantTrafficPack(ctx, PackGrant{UserID: user, SubscriptionID: &live.ID, Source: PackSourceOrder, SourceID: src, Bytes: gib}); errs.As(err).Code != errs.Conflict {
		t.Fatalf("重复发放: %v", err)
	}
	if err := conn.RollbackTo("dup").Error; err != nil {
		t.Fatal(err)
	}
	e.exec(t, ctx, `UPDATE traffic_pack_grants SET consumed_bytes = ? WHERE id = ?`, 4*gib, g.ID)

	// 来源还在用：不能转走
	if _, err := e.svc.TransferPacks(ctx, Transfer{UserID: user, From: &old.ID, To: live.ID, Actor: Actor{Kind: ActorUser, ID: &user}}); !errors.Is(err, ErrTransferSource) {
		t.Fatalf("来源在用: %v", err)
	}
	endSub(t, e, ctx, old.ID)
	if _, err := e.svc.GrantTrafficPack(ctx, PackGrant{UserID: user, SubscriptionID: &old.ID, Source: PackSourceAdmin, SourceID: uuid.New(), Bytes: gib}); !errors.Is(err, ErrPackNeedsLive) {
		t.Fatalf("加到停用的: %v", err)
	}
	if err := e.svc.CheckPackTarget(ctx, user, old.ID); !errors.Is(err, ErrPackNeedsLive) {
		t.Fatalf("CheckPackTarget: %v", err)
	}
	if _, err := e.svc.TransferPacks(ctx, Transfer{UserID: user, From: &live.ID, To: old.ID, Actor: Actor{Kind: ActorUser, ID: &user}}); !errors.Is(err, ErrTransferTarget) {
		t.Fatalf("目标停用: %v", err)
	}
	moved, err := e.svc.MyTransferPacks(userCtx(ctx, user), &old.ID, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 6*gib {
		t.Fatalf("挪过去 %d", moved)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_pack_grants WHERE id = ? AND subscription_id = ?`, g.ID, live.ID); n != 1 {
		t.Fatal("没挂到新的上")
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_pack_transfers WHERE grant_id = ? AND remaining_bytes = ? AND actor_kind = 'user'`, g.ID, 6*gib); n != 1 {
		t.Fatal("没有转移流水")
	}
	// 再转一次没有东西可转
	if moved, err := e.svc.MyTransferPacks(userCtx(ctx, user), &old.ID, live.ID); err != nil || moved != 0 {
		t.Fatalf("重复转: %d %v", moved, err)
	}
	// 别人的订阅
	other := newUser(t, e, ctx)
	if _, err := e.svc.MyTransferPacks(userCtx(ctx, other), nil, live.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别人的: %v", err)
	}

	bal, err := e.svc.MyTrafficPacks(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if bal.RemainingBytesTotal != 6*gib || len(bal.Packs) != 1 {
		t.Fatalf("余额: %+v", bal)
	}

	// 后台加流量
	admin := newUser(t, e, ctx)
	out, err := e.svc.AdminGrantTraffic(adminCtx(ctx, admin), live.ID, 2*gib, "补偿线路故障")
	if err != nil {
		t.Fatal(err)
	}
	if out.RemainingBytesTotal != 8*gib || out.UserID != user || out.UserEmail == "" {
		t.Fatalf("后台加流量: %+v", out)
	}
	if _, err := e.svc.AdminGrantTraffic(userCtx(ctx, user), live.ID, gib, "补偿线路故障"); errs.As(err).Code != errs.Forbidden {
		t.Fatalf("门户主体调后台: %v", err)
	}
}

func TestManualResetPG(t *testing.T) {
	e := newEnv(t)
	ctx := testkit.TxCtx(t, e.d)
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)},
		quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 30)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 4)
	actx := adminCtx(ctx, admin)

	if _, err := e.svc.AdminResetTraffic(actx, sub.ID, "短"); errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("备注太短: %v", err)
	}
	freed, err := e.svc.AdminResetTraffic(actx, sub.ID, "用户反馈计量异常")
	if err != nil {
		t.Fatal(err)
	}
	if freed != 34 {
		t.Fatalf("释放 %d", freed)
	}
	if q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle); q.Consumed != 0 || *q.Limit != 100*gib {
		t.Fatalf("cycle 行: %+v", q)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ? AND reason = 'manual' AND actor_id = ? AND note = ?`,
		sub.ID, admin, "用户反馈计量异常"); n != 2 {
		t.Fatalf("manual 日志 %d", n)
	}

	logs, total, err := e.svc.ListTrafficResets(actx, ResetLogFilter{UserID: &user, Reason: ResetManual})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(logs) != 2 || logs[0].ActorEmail == "" || logs[0].UserEmail == "" || logs[0].PlanName == "" {
		t.Fatalf("日志: %d %+v", total, logs)
	}
	st, err := e.svc.TrafficResetStats(actx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ManualCount < 2 || st.ByReason[ResetManual] < 2 {
		t.Fatalf("统计: %+v", st)
	}

	lapse(t, e, ctx, sub.ID, 1)
	if _, err := e.svc.AdminResetTraffic(actx, sub.ID, "用户反馈计量异常"); !errors.Is(err, ErrNotLive) {
		t.Fatalf("不生效: %v", err)
	}
}

func TestDeviceLimitAndLabelPG(t *testing.T) {
	e := newEnv(t)
	ctx := testkit.TxCtx(t, e.d)
	user := newUser(t, e, ctx)
	admin := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	a := provision(t, e, ctx, user, planID, versionID, "妈妈的 iPad")
	b := provision(t, e, ctx, user, planID, versionID, "")

	five := 5
	if err := e.svc.AdminSetDeviceLimit(adminCtx(ctx, admin), b.ID, &five); err != nil {
		t.Fatal(err)
	}
	uctx := userCtx(ctx, user)
	mine, err := e.svc.MySubscriptions(uctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Subscriptions) != 2 {
		t.Fatalf("我的套餐: %+v", mine)
	}
	for _, s := range mine.Subscriptions {
		switch s.ID {
		case a.ID:
			if s.ClientName != "测试站 · 妈妈的 iPad" || *s.EffectiveDeviceLimit != 3 || !s.Renewable || s.State != StateLive {
				t.Fatalf("a: %+v", s)
			}
		case b.ID:
			if *s.EffectiveDeviceLimit != 5 || !strings.HasPrefix(s.ClientName, "测试站 · 测试套餐") {
				t.Fatalf("b: %+v", s)
			}
		}
		if s.RenewalPrice != nil || s.RenewUntil != nil {
			t.Fatalf("没有价格档时不该有续费价: %+v", s)
		}
	}

	// 撞名 409，文案点出那一份
	label := " 妈妈的 ipad "
	_, err = e.svc.SetMyLabel(uctx, b.ID, &label)
	if ee := errs.As(err); ee.Code != errs.Conflict || !strings.Contains(ee.Message, "妈妈的 iPad") {
		t.Fatalf("撞名: %v", err)
	}
	ok := "爸爸的手机"
	res, err := e.svc.SetMyLabel(uctx, b.ID, &ok)
	if err != nil || res.Label == nil || *res.Label != ok || res.ClientName != "测试站 · 爸爸的手机" {
		t.Fatalf("改名: %+v %v", res, err)
	}
	if _, err := e.svc.SetMyLabel(userCtx(ctx, admin), b.ID, &ok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别人的: %v", err)
	}
	res, err = e.svc.SetMyLabel(uctx, b.ID, nil)
	if err != nil || res.Label != nil {
		t.Fatalf("清名: %+v %v", res, err)
	}
}

func TestSummariesByUsersPG(t *testing.T) {
	e := newEnv(t)
	ctx := testkit.TxCtx(t, e.d)
	u1, u2, u3 := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodDay, i64(gib)},
		quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	expired := provision(t, e, ctx, u1, planID, versionID, "")
	lapse(t, e, ctx, expired.ID, 1)
	live := provision(t, e, ctx, u1, planID, versionID, "")
	provision(t, e, ctx, u2, planID, versionID, "")
	if _, err := e.svc.GrantTrafficPack(ctx, PackGrant{UserID: u3, Source: PackSourceGiftCard, SourceID: uuid.New(), Bytes: 3}); err != nil {
		t.Fatal(err)
	}

	all, err := e.svc.SummariesByUsers(ctx, []uuid.UUID{u1, u2, u3})
	if err != nil {
		t.Fatal(err)
	}
	s1 := all[u1]
	if len(s1.Subscriptions) != 2 || s1.Current == nil || s1.Current.ID != live.ID || s1.Current.State != StateLive {
		t.Fatalf("u1: %+v", s1)
	}
	if q := s1.Current.Quotas; len(q) != 2 || q[0].Period != PeriodCycle || q[1].Period != PeriodDay {
		t.Fatalf("配额顺序: %+v", q)
	}
	if s1.Current.PlanName == "" || s1.Current.PlanVersion != 1 {
		t.Fatalf("目录信息: %+v", s1.Current)
	}
	if len(all[u2].Subscriptions) != 1 {
		t.Fatalf("u2: %+v", all[u2])
	}
	if s3 := all[u3]; s3.Subscriptions == nil || len(s3.Subscriptions) != 0 || s3.Current != nil || s3.UnattachedPackBytes != 3 {
		t.Fatalf("u3: %+v", s3)
	}

	sub, ok, err := e.svc.RenewableForPlan(ctx, u1, planID, true)
	if err != nil || !ok || sub.ID != live.ID {
		t.Fatalf("可续: %v %v %v", sub.ID, ok, err)
	}
}

func TestAppendOnlyGuardsPG(t *testing.T) {
	e := newEnv(t)
	setup := testkit.TxCtx(t, e.d)
	user := newUser(t, e, setup)
	planID, versionID := newPlan(t, e, setup, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	sub := provision(t, e, setup, user, planID, versionID, "")
	if _, err := e.svc.GrantTrafficPack(setup, PackGrant{UserID: user, SubscriptionID: &sub.ID, Source: PackSourceAdmin, SourceID: uuid.New(), Bytes: 1}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE subscription_events SET payload = '{}'::jsonb WHERE subscription_id = ?`,
		`DELETE FROM subscription_events WHERE subscription_id = ?`,
		`DELETE FROM subscriptions WHERE id = ?`,
		`DELETE FROM traffic_pack_grants WHERE subscription_id = ?`,
	} {
		// 每条在自己的保存点里试：违例会让外层事务失效
		conn := e.d.Conn(setup)
		if err := conn.SavePoint("guard").Error; err != nil {
			t.Fatal(err)
		}
		err := conn.Exec(q, sub.ID).Error
		if err == nil || !strings.Contains(err.Error(), "23001") && !strings.Contains(strings.ToLower(err.Error()), "restrict") {
			t.Fatalf("%s 应被拒: %v", q, err)
		}
		if err := conn.RollbackTo("guard").Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestConcurrentRenewPG 用真实提交：两笔续费并发落到同一份上，按行锁串行，各加一期；提交后各通知一次名单。
func TestConcurrentRenewPG(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	if n := e.roster.count(sub.ID); n != 1 {
		t.Fatalf("开通后名单通知 %d", n)
	}

	var wg sync.WaitGroup
	errc := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Renew(ctx, Renewal{SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: time.Now(), Actor: Actor{Kind: ActorPayment}, Source: "order"})
			errc <- err
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.svc.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := AddInterval(AddInterval(sub.PeriodEnd, "month", 1).Truncate(time.Microsecond), "month", 1).Truncate(time.Microsecond)
	if !got.PeriodEnd.Equal(want) {
		t.Fatalf("两期: got %s want %s", got.PeriodEnd, want)
	}
	if n := e.roster.count(sub.ID); n != 3 {
		t.Fatalf("名单通知 %d", n)
	}
}
