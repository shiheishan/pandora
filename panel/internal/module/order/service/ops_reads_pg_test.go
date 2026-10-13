package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestUserOrderStatsPG(t *testing.T) {
	e, ctx := newEnv(t)

	u1 := e.mkUser(t, ctx)
	u2 := e.mkUser(t, ctx)

	insertOrder := func(uid uuid.UUID, cur, status string, amt decimal.Decimal) {
		now := time.Now().UTC()
		var paidAmt decimal.Decimal
		var paidAt, fulfilledAt *time.Time
		if status == "paid" || status == "fulfilled" {
			paidAmt = amt
			paidAt = &now
			if status == "fulfilled" {
				fulfilledAt = &now
			}
		}
		orderNo := fmt.Sprintf("TOPUP%s", strings.ToUpper(randHex(12))) // order_no 只许大写（CHECK）
		e.mustExec(t, ctx, `
			INSERT INTO orders (
				order_no, user_id, kind, status, currency,
				subtotal_amount, total_amount, payable_amount, paid_amount,
				expires_at, paid_at, fulfilled_at, created_at, updated_at
			) VALUES (
				?, ?, 'topup', ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?, ?
			)`,
			orderNo, uid, status, cur,
			amt, amt, amt, paidAmt,
			now.Add(30*time.Minute), paidAt, fulfilledAt, now, now,
		)
	}

	// u1：两笔 CNY 已付、一笔待付、一笔 USD 已付
	insertOrder(u1, "CNY", "paid", dec("10.50"))
	insertOrder(u1, "CNY", "fulfilled", dec("20.25"))
	insertOrder(u1, "CNY", "pending_payment", dec("5.00"))
	insertOrder(u1, "USD", "fulfilled", dec("15.00"))
	// u2 无订单

	stats, err := e.svc.UserOrderStats(ctx, []uuid.UUID{u1, u2})
	if err != nil {
		t.Fatalf("UserOrderStats 失败: %v", err)
	}

	st1, ok := stats[u1]
	if !ok {
		t.Fatalf("u1 统计结果不存在")
	}
	if st1.Orders != 4 {
		t.Fatalf("u1 订单数 got %d want 4", st1.Orders)
	}
	wantCNY := dec("30.75")
	if gotCNY := st1.Paid["CNY"]; !gotCNY.Equal(wantCNY) {
		t.Fatalf("u1 CNY 实付 got %s want %s", gotCNY, wantCNY)
	}
	wantUSD := dec("15.00")
	if gotUSD := st1.Paid["USD"]; !gotUSD.Equal(wantUSD) {
		t.Fatalf("u1 USD 实付 got %s want %s", gotUSD, wantUSD)
	}

	if st2, ok := stats[u2]; ok && st2.Orders != 0 {
		t.Fatalf("u2 无订单但统计结果 got %+v", st2)
	}

	// 测试空输入返回空 map
	emptyStats, err := e.svc.UserOrderStats(ctx, []uuid.UUID{})
	if err != nil {
		t.Fatalf("空输入查询失败: %v", err)
	}
	if len(emptyStats) != 0 {
		t.Fatalf("空输入期望空 map，got %+v", emptyStats)
	}
}
