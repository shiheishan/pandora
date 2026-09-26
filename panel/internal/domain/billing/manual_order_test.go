// [INPUT]: 依赖 manual_order.go 的 CreateManualOrder、offlinePaymentInput 与 ManualSettlement*
// [OUTPUT]: 对外提供 TestManualOrderSettlementValidatedBeforeCheckout、TestOfflinePaymentInputShape、TestMarkPaidQuarantinedConflict
// [POS]: billing 人工单的单元测试：结算方式与凭证号在进下单事务之前被拒；两条线下收款路径（标记已支付、人工单线下已收款）共用同一个回调形状
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 服务没有数据库：走到 CreateOrder 就会因为幂等声明为空而报别的错，
// 所以拿到的是 422 就说明拒绝发生在下单之前。
func TestManualOrderSettlementValidatedBeforeCheckout(t *testing.T) {
	svc := NewService(nil, nil)
	const id = "73300000-0000-7000-8000-000000000001"
	cases := []struct {
		settlement, reference, field string
	}{
		{ManualSettlementOffline, "", "reference"},
		{ManualSettlementOffline, "   ", "reference"},
		{ManualSettlementOffline, strings.Repeat("号", 129), "reference"},
		{"balance", "", "settlement"},
		{"cash", "", "settlement"},
	}
	for _, tc := range cases {
		_, err := svc.CreateManualOrder(t.Context(), id, CreateManualOrderInput{
			UserID: id, PlanID: id, PriceID: id, ActorID: id,
			Reason: "单元测试开单原因", Settlement: tc.settlement, Reference: tc.reference,
		})
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields[tc.field] == "" {
			t.Errorf("settlement=%q reference=%q err=%v, want 422 on %s",
				tc.settlement, tc.reference, err, tc.field)
		}
	}
}

func TestOfflinePaymentInputShape(t *testing.T) {
	in := offlinePaymentInput("order-1", "CNY", 1000, "actor-1",
		OfflineReceipt{Reference: "BANK-1", Reason: "银行转账已到账"})
	if in.ProviderCode != OfflineProviderCode || in.EventType != "payment.succeeded" ||
		in.ProviderEventID != "offline:order-1:BANK-1" || in.ProviderPaymentID != "offline:BANK-1" ||
		in.OrderID != "order-1" || in.Amount != 1000 || in.Currency != "CNY" ||
		in.FeeAmount != 0 || !in.SignatureVerified {
		t.Fatalf("offline payment input=%+v", in)
	}
	payload := in.RawPayload
	if payload["source"] != "admin_offline" || payload["actor_id"] != "actor-1" ||
		payload["reference"] != "BANK-1" || payload["reason"] != "银行转账已到账" {
		t.Fatalf("offline raw payload=%#v", in.RawPayload)
	}
}

// 标记已付的钱进了挂账时回 409（R117），两种来由各有说明，都指向挂账。
func TestMarkPaidQuarantinedConflict(t *testing.T) {
	for kind, fragment := range map[string]string{
		"ineligible_subscription": "订阅已结束",
		"released_order":          "不在待支付状态",
	} {
		err := markPaidQuarantined(kind)
		if err.Code != httpx.CodeConflict || !strings.Contains(err.Message, fragment) ||
			!strings.Contains(err.Message, "挂账") {
			t.Errorf("%s: code=%s message=%q", kind, err.Code, err.Message)
		}
	}
}
