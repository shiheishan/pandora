package service

// 路由组成员、接入与吊销写路径在事务提交后向管理端推 nodes.changed。

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

func TestRealtimeRouteGroupMembersPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	e.svc.UseRealtime(pub)
	rtCleanupGroups(t, e)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21060)
	g := rtGroup(t, ctx, e, "rt-mem", 0)
	joinRouteGroup(t, ctx, e, g.ID, nid)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "UPDATE", "id": nid.String(),
	})
}

func TestRealtimeIdentityEnrollPG(t *testing.T) {
	e, ctx := newIdentEnv(t, true)
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	e.svc.UseRealtime(pub)
	sid := seedIdentServer(t, ctx, e, "ready")
	identEnroll(t, ctx, e, sid)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": sid.String(),
	})
}

func TestRealtimeIdentityRevokePG(t *testing.T) {
	e, ctx := newIdentEnv(t, true)
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	e.svc.UseRealtime(pub)
	sid := seedIdentServer(t, ctx, e, "ready")
	nodeID := seedIdentNode(t, ctx, e, sid)
	identEnroll(t, ctx, e, sid)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": sid.String(),
	})
	if err := e.svc.RevokeNodeIdentity(e.admin, nodeID); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": sid.String(),
	})
}

func TestRealtimeRoutingUncommittedDoesNotPublishPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	e.svc.UseRealtime(pub)
	// 回滚环境不用 rtCleanupGroups（它只给真提交环境用），建的组随事务回滚
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21061)
	g := rtGroup(t, ctx, e, "rt-nopub", 0)
	joinRouteGroup(t, ctx, e, g.ID, nid)
	// ctx 来自 testkit.TxCtx，用例结束回滚；Changed 只在提交后发布，故不应收到推送。
	spy.expectNone(t, 300*time.Millisecond)
}
