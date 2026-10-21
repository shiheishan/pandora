package service

import (
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// TestBeginIntentBelowMethodMinimum 应付低于渠道最低额回 below_method_minimum，且不落意图；等于最低额可发起。
func TestBeginIntentBelowMethodMinimum(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	if err := e.exec(ctx, "UPDATE payment_providers SET min_amount = ? WHERE code = ?", "20.00", code); err != nil {
		t.Fatal(err)
	}
	orderID, orderNo := e.mkOrder(t, ctx, user)

	_, err := e.svc.CreateIntent(ctx, CreateIntentInput{
		OrderID:      orderID,
		OrderNo:      orderNo,
		UserID:       user,
		Amount:       dec("9.90"),
		ProviderCode: code,
		Title:        "测试订单",
		ClientIP:     "203.0.113.1",
	})
	if err == nil {
		t.Fatal("9.90 低于最低额应失败")
	}
	pe := errs.As(err)
	if pe.Code != errs.BelowMethodMinimum {
		t.Fatalf("code=%s", pe.Code)
	}
	if pe.Status() != 409 {
		t.Fatalf("status=%d", pe.Status())
	}
	if pe.Message != "这个付款方式最低付 ¥20.00，请换一种付款方式" {
		t.Fatalf("message=%q", pe.Message)
	}
	if e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ?`, orderID) != 0 {
		t.Fatal("低于最低额不应插入意图")
	}

	res, err := e.svc.CreateIntent(ctx, CreateIntentInput{
		OrderID:      orderID,
		OrderNo:      orderNo,
		UserID:       user,
		Amount:       dec("20.00"),
		ProviderCode: code,
		Title:        "测试订单",
		ClientIP:     "203.0.113.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IntentID == uuid.Nil {
		t.Fatal("20.00 应成功发起")
	}
}

// TestPaymentMethodsMinAmount 列表支付方式时带上渠道的最低付款额。
func TestPaymentMethodsMinAmount(t *testing.T) {
	e, ctx := newEnv(t)
	code := "fake-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	if err := e.exec(ctx, "UPDATE payment_providers SET min_amount = ? WHERE code = ?", "12.34", code); err != nil {
		t.Fatal(err)
	}

	methods, err := e.svc.PaymentMethods(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *PaymentMethod
	for i := range methods {
		if methods[i].Provider == code {
			found = &methods[i]
			break
		}
	}
	if found == nil {
		t.Fatal("未找到该渠道")
	}
	if !found.MinAmount.Equal(dec("12.34")) {
		t.Fatalf("MinAmount=%s", found.MinAmount)
	}
}
