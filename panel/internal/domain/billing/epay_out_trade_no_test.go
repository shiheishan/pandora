package billing

import (
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 易支付同一订单换方式（用户 2026-10-07 定）：对外单号按订单取序号，存进 provider_ref；
// 回调与查单先按 provider_ref 找订单；查单按每笔支付自己的对外单号问渠道、按订单 id 结算。
func TestEpayOutTradeNoContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	create := pkg.Decl("PaymentService.CreatePaymentIntent")
	seq := strings.Index(create, "nextOutTradeNo(ctx, tx, tenantID, in.OrderID, orderNo)")
	call := strings.Index(create, "OutTradeNo: outTradeNo,")
	insert := strings.Index(create, "providerRef, payload, expiresAt,")
	if seq < 0 || call < seq || insert < call || !strings.Contains(create, `"out_trade_no": outTradeNo`) {
		t.Fatal("CreatePaymentIntent must number the attempt, send it to the channel and store it as provider_ref")
	}
	next := pkg.Decl("nextOutTradeNo")
	if !strings.Contains(next, `fmt.Sprintf("%s-%d", orderNo, prior+1)`) || !strings.Contains(next, "return orderNo, nil") {
		t.Fatal("the first attempt keeps the order number, later ones are <order number>-<n>")
	}
	parse := pkg.Decl("PaymentService.ParseNotification")
	lookup := strings.Index(parse, "s.orderIDByProviderRef(ctx, tenantID, rec.ID, n.OutTradeNo)")
	input := strings.Index(parse, "OrderID:           orderID,")
	if lookup < 0 || input < lookup {
		t.Fatal("callbacks must locate the order by provider_ref before settlement")
	}
	query := pkg.Decl("PaymentService.queryProvider")
	if !strings.Contains(query, "prov.QueryPayment(qctx, ref.OutTradeNo)") {
		t.Fatal("active queries must ask by each attempt's out_trade_no")
	}
	reconcile := pkg.Decl("PaymentService.reconcileQueried")
	if !strings.Contains(reconcile, "OrderID:           out.OrderID,") || strings.Contains(reconcile, "OrderNo:") {
		t.Fatal("reconciliation must settle by order id, not by the channel's order number")
	}
	if !strings.Contains(reconcile, "s.settle.settlePaymentTx(ctx, tx, tenantID, in, &settled)") ||
		!strings.Contains(reconcile, "hook(ctx, tx, out)") {
		t.Fatal("reconciliation must settle through settlePaymentTx and run the audit hook in the same transaction")
	}
}

// 易支付只出支付宝和微信：后台可勾的、下单可用的都是这两种。
func TestEpayOffersOnlyAlipayAndWechat(t *testing.T) {
	if !slices.Equal(EpayMethods, []string{"alipay", "wxpay"}) {
		t.Fatalf("EpayMethods=%v", EpayMethods)
	}
	if got := providerMethods(map[string]any{"methods": []any{"qqpay", "alipay", "wxpay"}}); !slices.Equal(got, []string{"alipay", "wxpay"}) {
		t.Fatalf("providerMethods kept %v", got)
	}
}
