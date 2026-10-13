package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// TestRealtimeAnnouncementUncommittedDoesNotPublishPG：newPG 的 ctx 来自 testkit.TxCtx，
// 用例结束会回滚，提交后钩子不会执行，因此不应收到实时推送。
func TestRealtimeAnnouncementUncommittedDoesNotPublishPG(t *testing.T) {
	a := annSetup(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	a.svc.UseRealtime(pub)
	a.annSave(t, annInput("维护通知"))
	spy.expectNone(t, 300*time.Millisecond)
}
