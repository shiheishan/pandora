package service

import (
	"testing"
	"time"
)

func TestOpenTicketStatsPG(t *testing.T) {
	e, ctx := newEnv(t)
	now := time.Now().Add(time.Hour)

	s0, err := e.svc.OpenTicketStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}

	u1 := e.mkUser(t, ctx, nil)
	u2 := e.mkUser(t, ctx, nil)

	// urgent 优先级，留在待处理队列
	_, err = e.svc.Create(asUser(ctx, u1), CreateInput{
		Body:     createBody("urgent"),
		Category: "abuse",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 消息只追加、不能改时刻；now 取现在 + 1 小时，刚建的单就已等了约 3600 秒

	// 普通优先级，建完即 resolved，不计入待处理
	tkNorm, err := e.svc.Create(asUser(ctx, u2), CreateInput{Body: createBody("norm")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx,
		`UPDATE tickets SET status = 'resolved', resolved_at = now() WHERE id = ?`, tkNorm.ID); err != nil {
		t.Fatal(err)
	}

	s1, err := e.svc.OpenTicketStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Count != s0.Count+1 {
		t.Fatalf("Count 期望 %d，得到 %d", s0.Count+1, s1.Count)
	}
	if s1.HighPriority != s0.HighPriority+1 {
		t.Fatalf("HighPriority 期望 %d，得到 %d", s0.HighPriority+1, s1.HighPriority)
	}
	if s1.OldestWaitSeconds == nil {
		t.Fatal("OldestWaitSeconds 不应为 nil")
	}
	if *s1.OldestWaitSeconds < 3500 {
		t.Fatalf("OldestWaitSeconds 期望 ≥ 3500，得到 %d", *s1.OldestWaitSeconds)
	}
}
