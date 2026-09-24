package billing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// POST v1/orders/{id}/mark-paid 直接把 PaymentWebhookOutput 写回响应。
// 没有 json tag 时它是 PascalCase（缺陷 10），SignatureFailed 也会漏出去。
func TestPaymentWebhookOutputJSONShape(t *testing.T) {
	body, err := json.Marshal(PaymentWebhookOutput{
		Processed: true, AlreadyHandled: false, SignatureFailed: true,
		PaymentID: "p", SubscriptionID: "s", LedgerTxnID: "l",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"processed": true, "already_handled": false,
		"payment_id": "p", "subscription_id": "s", "ledger_txn_id": "l",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mark-paid response shape=%s want keys %v", body, want)
	}
}
