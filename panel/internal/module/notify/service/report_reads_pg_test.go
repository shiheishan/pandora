package service

import (
	"testing"
	"time"
)

func TestDeliveryBacklogPG(t *testing.T) {
	e := newPG(t)
	now := time.Now().Add(time.Minute)

	b0, err := e.svc.DeliveryBacklog(e.ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	m0, err := e.svc.DeliveryCountsByChannel(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	email0 := m0["email"]

	t0 := time.Now().Add(-time.Second)
	e.send(t, "auth.email_verify")
	e.send(t, "auth.email_verify")

	// 只改刚写的那两条之一（按写入时刻圈定，不碰库里已有的投递）
	var one int
	if err := e.scan(e.ctx, &one, `
		UPDATE notification_deliveries
		   SET attempts = 1, next_retry_at = now() + interval '1 hour'
		 WHERE id = (SELECT id FROM notification_deliveries
		               WHERE status = 'queued' AND created_at >= ? ORDER BY id DESC LIMIT 1)
		RETURNING 1`, t0); err != nil {
		t.Fatal(err)
	}
	if one != 1 {
		t.Fatalf("应更新一条投递，got %d", one)
	}

	b1, err := e.svc.DeliveryBacklog(e.ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if b1.Ready != b0.Ready+1 {
		t.Fatalf("Ready: b0=%d b1=%d", b0.Ready, b1.Ready)
	}
	if b1.Scheduled != b0.Scheduled+1 {
		t.Fatalf("Scheduled: b0=%d b1=%d", b0.Scheduled, b1.Scheduled)
	}
	if b1.ScheduledRetry != b0.ScheduledRetry+1 {
		t.Fatalf("ScheduledRetry: b0=%d b1=%d", b0.ScheduledRetry, b1.ScheduledRetry)
	}
	if b1.OldestReadyAt == nil {
		t.Fatal("OldestReadyAt 应为非 nil")
	}

	m1, err := e.svc.DeliveryCountsByChannel(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	email1 := m1["email"]
	if email1.Queued != email0.Queued+2 {
		t.Fatalf("email Queued: m0=%d m1=%d", email0.Queued, email1.Queued)
	}
	if email1.Retrying != email0.Retrying+1 {
		t.Fatalf("email Retrying: m0=%d m1=%d", email0.Retrying, email1.Retrying)
	}
}
