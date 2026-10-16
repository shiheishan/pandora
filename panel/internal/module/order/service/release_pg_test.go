package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// pendingWithEverything 建一张占了券与部分余额的待付新购单；返回用户、套餐与订单 id。
func (e *env) pendingWithEverything(t *testing.T, ctx context.Context) (user, planID, orderID uuid.UUID) {
	t.Helper()
	user = e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
	code := couponCode()
	e.mkCoupon(t, ctx, code, 500)
	e.giveBalance(t, ctx, user, 1000)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("10"), CouponCode: code})
	if c.Status != StatusPending {
		t.Fatalf("前置订单状态 %s", c.Status)
	}
	return user, planID, c.OrderID
}

// assertReleased 核对一张单占的资源全部退回。
func (e *env) assertReleased(t *testing.T, ctx context.Context, user, planID, orderID uuid.UUID, event string) {
	t.Helper()
	if st := e.reservationState(t, ctx, orderID); st != ReservationReleased {
		t.Fatalf("预留 %s，期望 released", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND event_kind = ? AND to_state = 'released'`, orderID, event); n != 1 {
		t.Fatalf("%s 流水 %d 条", event, n)
	}
	if st := e.strCol(t, ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, orderID); st != "released" {
		t.Fatalf("券 %s，期望 released", st)
	}
	if st := e.strCol(t, ctx, `SELECT status FROM balance_holds WHERE order_id = ?`, orderID); st != "released" {
		t.Fatalf("冻结 %s，期望 released", st)
	}
	mustEq(t, "余额恢复", e.balanceOf(t, ctx, user), dec("10"))
	e.assertNoDrift(t, ctx)
}

// 用户取消待付新购单：订单 cancelled（user_cancelled），券、冻结全部退回。
func TestCancelByUserReleasesEverything(t *testing.T) {
	e, ctx := newEnv(t)
	user, planID, orderID := e.pendingWithEverything(t, ctx)

	res, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: orderID})
	if err != nil {
		t.Fatalf("取消: %v", err)
	}
	if res.AlreadyTerminal || res.Order.Status != StatusCancelled {
		t.Fatalf("取消结果 %+v", res)
	}
	o := e.order(t, ctx, orderID)
	if o.Status != StatusCancelled || o.CancelReason == nil || *o.CancelReason != ReasonUserCancelled || o.CancelledAt == nil {
		t.Fatalf("订单 status=%s reason=%v cancelled_at=%v", o.Status, o.CancelReason, o.CancelledAt)
	}
	if o.StateVersion != 2 {
		t.Fatalf("state_version %d，期望 2", o.StateVersion)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND event_kind = 'cancel' AND actor_kind = 'user' AND actor_id = ?`, orderID, user); n != 1 {
		t.Fatalf("cancel 流水的操作者不对")
	}
	e.assertReleased(t, ctx, user, planID, orderID, EventCancel)
}

// 重复取消回 AlreadyTerminal；取消别人的单回 404。
func TestCancelRepeatAndForeign(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	if _, err := e.svc.Cancel(asUser(ctx, e.mkUser(t, ctx)), CancelInput{OrderID: c.OrderID}); codeOf(err) != errs.NotFound {
		t.Fatalf("取消别人的单应 404，得到 %v", err)
	}
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID}); err != nil {
		t.Fatalf("第一次取消: %v", err)
	}
	res, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID})
	if err != nil || !res.AlreadyTerminal {
		t.Fatalf("重复取消应 AlreadyTerminal，得到 %+v %v", res, err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND to_state = 'released'`, c.OrderID); n != 1 {
		t.Fatalf("释放流水 %d 条，期望 1", n)
	}
}

// 后台取消：缺版本或原因不合格 422，版本不符 409，相符成功；门户主体调后台取消 403。
func TestCancelByAdmin(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	reason := "用户来信要求取消"

	if _, err := e.svc.Cancel(actx, CancelInput{OrderID: c.OrderID, Admin: true, Reason: reason}); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("缺 expected_state_version 应 422，得到 %v", err)
	}
	v1 := int64(1)
	if _, err := e.svc.Cancel(actx, CancelInput{OrderID: c.OrderID, Admin: true, ExpectedStateVersion: &v1, Reason: "太短"}); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("原因少于 5 字应 422，得到 %v", err)
	}
	if _, err := e.svc.Cancel(asUser(ctx, admin), CancelInput{OrderID: c.OrderID, Admin: true, ExpectedStateVersion: &v1, Reason: reason}); codeOf(err) != errs.Forbidden {
		t.Fatalf("门户主体调后台取消应 403，得到 %v", err)
	}
	stale := int64(7)
	if _, err := e.svc.Cancel(actx, CancelInput{OrderID: c.OrderID, Admin: true, ExpectedStateVersion: &stale, Reason: reason}); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("版本不符应 ErrStateChanged，得到 %v", err)
	}
	res, err := e.svc.Cancel(actx, CancelInput{OrderID: c.OrderID, Admin: true, ExpectedStateVersion: &v1, Reason: reason})
	if err != nil {
		t.Fatalf("后台取消: %v", err)
	}
	if res.Order.Status != StatusCancelled || res.Order.CancelReason == nil || *res.Order.CancelReason != reason {
		t.Fatalf("取消结果 %+v", res.Order)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND event_kind = 'cancel' AND actor_kind = 'admin' AND actor_id = ?`, c.OrderID, admin); n != 1 {
		t.Fatalf("cancel 流水的操作者应是管理员")
	}
}

// 超时释放：到期的单 expired 且资源全部退回；未到期的单不动；过期的单不能再取消。
func TestExpireDue(t *testing.T) {
	e, ctx := newEnv(t)
	user, planID, dueID := e.pendingWithEverything(t, ctx)
	other := e.mkUser(t, ctx)
	freshPlan, _, freshPrice := e.mkPlan(t, ctx, 1000, "month")
	fresh := e.buyNew(t, ctx, other, freshPlan, freshPrice, Checkout{})

	e.mustExec(t, ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?`, dueID)
	e.mustExec(t, ctx, `UPDATE order_reservations SET expires_at = now() - interval '1 minute' WHERE order_id = ?`, dueID)

	stats, err := e.svc.ExpireDue(ctx, 0)
	if err != nil {
		t.Fatalf("超时释放: %v", err)
	}
	if stats.Expired < 1 || stats.Failed != 0 {
		t.Fatalf("统计 %+v", stats)
	}
	o := e.order(t, ctx, dueID)
	if o.Status != StatusExpired || o.ExpiredAt == nil || o.CancelReason != nil {
		t.Fatalf("到期单 status=%s expired_at=%v cancel_reason=%v（超时不写取消原因）", o.Status, o.ExpiredAt, o.CancelReason)
	}
	if r := e.strCol(t, ctx, `SELECT release_reason FROM order_reservations WHERE order_id = ?`, dueID); r != ReasonExpired {
		t.Fatalf("预留的释放原因 %q，期望 expired", r)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND event_kind = 'expire' AND actor_kind = 'system'`, dueID); n != 1 {
		t.Fatalf("expire 流水应由 system 写")
	}
	e.assertReleased(t, ctx, user, planID, dueID, EventExpire)

	if st := e.order(t, ctx, fresh.OrderID).Status; st != StatusPending {
		t.Fatalf("未到期的单被动了: %s", st)
	}
	if st := e.reservationState(t, ctx, fresh.OrderID); st != ReservationHeld {
		t.Fatalf("未到期单的预留 %s，期望 held", st)
	}
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: dueID}); !errors.Is(err, errOrderExpired) {
		t.Fatalf("取消已过期的单应回「订单已过期」，得到 %v", err)
	}
	// 再跑一轮：没有可释放的
	again, err := e.svc.ExpireDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if e.order(t, ctx, dueID).Status != StatusExpired || again.Failed != 0 {
		t.Fatalf("第二轮统计 %+v", again)
	}
}

// 已履约的单取消回 ErrAlreadyPaid。
func TestCancelFulfilledRejected(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	e.giveBalance(t, ctx, user, 1000)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("10")})
	if c.Status != StatusFulfilled {
		t.Fatalf("前置订单状态 %s", c.Status)
	}
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID}); !errors.Is(err, ErrAlreadyPaid) {
		t.Fatalf("期望 ErrAlreadyPaid，得到 %v", err)
	}
	v := e.order(t, ctx, c.OrderID).StateVersion
	if _, err := e.svc.Cancel(asAdmin(ctx, e.mkUser(t, ctx)), CancelInput{OrderID: c.OrderID, Admin: true, ExpectedStateVersion: &v, Reason: "后台误操作测试"}); !errors.Is(err, ErrAlreadyPaid) {
		t.Fatalf("后台取消已履约单应 ErrAlreadyPaid，得到 %v", err)
	}
}
