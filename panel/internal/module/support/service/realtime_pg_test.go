package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// TestRealtimeUncommittedDoesNotPublish 在会回滚的测试事务里建单，提交钩子不应执行，故一条推送都不该出现。
func TestRealtimeUncommittedDoesNotPublish(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	pub, spy := newRTSpy(t, realtime.ChannelUser(user), realtime.ChannelAdmin(), realtime.ChannelPublic())
	e.svc.UseRealtime(pub)

	_, err := e.svc.Create(asUser(ctx, user), CreateInput{
		Category: "general",
		Subject:  "未提交事务",
		Body:     createBody(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	// ctx 来自 testkit.TxCtx，这个事务最后回滚，提交后钩子不会执行，所以一条都不该发。
	spy.expectNone(t, 300*time.Millisecond)
}
