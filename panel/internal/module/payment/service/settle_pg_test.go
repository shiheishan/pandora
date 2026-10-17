package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/google/uuid"
)

// 验签失败的回调只留白名单字段，每个字段截断，总量不超过 2 KiB。
func TestWebhookUnverifiedEvidenceWhitelisted(t *testing.T) {
	e, ctx := newEnv(t)
	code := "fake-ev-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)

	huge := strings.Repeat("x", 5000)
	body, _ := json.Marshal(map[string]any{
		"event_id":   "evt-" + huge,
		"event_type": "payment.succeeded",
		"payment_id": "pay-" + huge,
		"order_no":   "ORD-" + huge,
		"amount":     990,
		"raw": map[string]any{
			"out_trade_no": "T" + huge,
			"trade_status": "TRADE_SUCCESS",
			"junk":         strings.Repeat("y", 5000),
			"sign":         "deadbeef",
		},
	})
	h := http.Header{}
	h.Set("X-Aegis-Signature", strings.Repeat("0", 64))
	ack, err := e.svc.HandleWebhook(ctx, code, provider.Inbound{Method: http.MethodPost, Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("验签失败应 401，得 %d", ack.HTTPStatus)
	}

	var raw string
	var size int
	if err := e.row(ctx, `
SELECT raw_payload::text, octet_length(raw_payload::text)
FROM payment_events e JOIN payment_providers p ON p.id = e.provider_id
WHERE p.code = ? AND NOT e.signature_verified`, code).Scan(&raw, &size); err != nil {
		t.Fatal(err)
	}
	if size > 2048 {
		t.Fatalf("raw_payload %d 字节，超过 2 KiB", size)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"junk", "sign"} {
		if _, ok := m[k]; ok {
			t.Fatalf("不在白名单的 %q 不应留下: %v", k, m)
		}
	}
	if m["trade_status"] != "TRADE_SUCCESS" {
		t.Fatalf("白名单字段应保留: %v", m)
	}
	for _, k := range []string{"out_trade_no", "parsed_event_id", "parsed_out_trade_no", "parsed_payment_ref"} {
		if v, ok := m[k]; !ok || len(v) > unverifiedEvidenceValueMax {
			t.Fatalf("%s 缺失或未截断（%d 字节）", k, len(v))
		}
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments`) != 0 {
		t.Fatal("验签失败不应入账")
	}
}

// 回调卡在 Settler 等订单锁时，另一个事务先锁住订单再调 BeginIntent：不能死锁。
// 回调事务插 payments 时外键检查对意图行加了 KEY SHARE；LockActiveIntent 用 NO KEY UPDATE 与它兼容。
// 订单锁用 advisory 锁模拟。
func TestWebhookSettleVsCreateIntentNoDeadlock(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-dl-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	orderLock := "order:" + orderID.String()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	aAtSettler := make(chan struct{})
	e.settler.mu.Lock()
	e.settler.before = func(ctx context.Context, in SettleInput) error {
		if in.OrderID != orderID {
			return nil
		}
		close(aAtSettler)
		return e.exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, orderLock)
	}
	e.settler.mu.Unlock()

	bLocked := make(chan struct{})
	bGo := make(chan struct{})
	bDone := make(chan error, 1)
	var bRes IntentResult
	var bDraft IntentDraft
	go func() {
		bDone <- e.txm.Do(ctx, func(ctx context.Context) error {
			if err := e.exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, orderLock); err != nil {
				close(bLocked)
				return err
			}
			close(bLocked)
			select {
			case <-bGo:
			case <-ctx.Done():
				return ctx.Err()
			}
			var err error
			bDraft, err = e.svc.BeginIntent(ctx, CreateIntentInput{
				OrderID:      orderID,
				OrderNo:      orderNo,
				UserID:       user,
				Amount:       dec("9.90"),
				ProviderCode: code,
				Title:        "测试订单",
				ClientIP:     "203.0.113.1",
			})
			return err
		})
	}()
	<-bLocked

	aDone := make(chan error, 1)
	var aAck provider.AckOutput
	go func() {
		var err error
		aAck, err = e.svc.HandleWebhook(ctx, code, fakeInbound("evt-dl-1", "payment.succeeded", "pay-dl-1", intent.OutTradeNo, 990))
		aDone <- err
	}()

	select {
	case <-aAtSettler:
	case err := <-aDone:
		t.Fatalf("回调没走到 Settler 就结束: %v", err)
	case <-ctx.Done():
		t.Fatal("等回调走到 Settler 超时")
	}
	close(bGo)

	if err := <-bDone; err != nil {
		t.Fatalf("锁订单后 BeginIntent 失败（死锁？）: %v", err)
	}
	bRes, completeErr := e.svc.CompleteIntent(ctx, bDraft)
	if completeErr != nil {
		t.Fatalf("CompleteIntent: %v", completeErr)
	}
	if err := <-aDone; err != nil {
		t.Fatalf("回调失败（死锁？）: %v", err)
	}
	if aAck.HTTPStatus != http.StatusOK {
		t.Fatalf("回调应 200: %d %s", aAck.HTTPStatus, string(aAck.Body))
	}
	if bRes.OutTradeNo == "" {
		t.Fatal("BeginIntent/CompleteIntent 应返回意图")
	}
	if e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, orderID) != 1 {
		t.Fatal("应入账 1 笔")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 1 {
		t.Fatal("应有 1 笔 order_paid")
	}
}
