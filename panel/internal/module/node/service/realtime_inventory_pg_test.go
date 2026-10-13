package service

// 服务器与节点后台写入后的实时推送（连库）。

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

func TestRealtimeServerCreateAndPatch(t *testing.T) {
	pub, spy := newRTSpy(t, realtime.ChannelAdmin(), realtime.ChannelPublic())
	e, ctx := newInvEnvCommitted(t)
	e.svc.UseRealtime(pub)

	srv := e.server(t, ctx, "rt")
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "INSERT", "id": srv.ID.String(),
	})

	notes := "实时推送备注"
	_, err := e.svc.PatchServer(ctx, srv.ID, ServerPatch{RowVersion: srv.RowVersion, Notes: &notes})
	if err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": srv.ID.String(),
	})
	spy.expectNoneOn(t, realtime.ChannelPublic(), 200*time.Millisecond)
}

func TestRealtimeNodeCreateAndRetire(t *testing.T) {
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e, ctx := newInvEnvCommitted(t)
	e.svc.UseRealtime(pub)

	srv := e.server(t, ctx, "rt-node")
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "INSERT", "id": srv.ID.String(),
	})

	n := e.ssNode(t, ctx, srv.ID, "n", 21100)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "INSERT", "id": n.ID.String(),
	})

	_, err := e.svc.RetireNode(ctx, n.ID, n.RowVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "UPDATE", "id": n.ID.String(),
	})
}

func TestRealtimeNodeDelete(t *testing.T) {
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e, ctx := newInvEnvCommitted(t)
	e.svc.UseRealtime(pub)

	srv := e.server(t, ctx, "rt-del")
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "INSERT", "id": srv.ID.String(),
	})

	draft := e.ssNode(t, ctx, srv.ID, "draft", 21101)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "INSERT", "id": draft.ID.String(),
	})

	if err := e.svc.DeleteNode(ctx, draft.ID, draft.RowVersion, "建错了"); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "DELETE", "id": draft.ID.String(),
	})
}

func TestRealtimeServerDelete(t *testing.T) {
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e, ctx := newInvEnvCommitted(t)
	e.svc.UseRealtime(pub)

	srv := e.server(t, ctx, "rt-srv-del")
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "INSERT", "id": srv.ID.String(),
	})

	draft := e.ssNode(t, ctx, srv.ID, "draft", 21102)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "INSERT", "id": draft.ID.String(),
	})

	if err := e.svc.DeleteServer(ctx, srv.ID, srv.RowVersion); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "DELETE", "id": srv.ID.String(),
	})
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "UPDATE", "id": draft.ID.String(),
	})
}

func TestRealtimeInventoryUncommittedDoesNotPublish(t *testing.T) {
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e, ctx := newInvEnv(t) // 回滚事务：提交后钩子不执行，不应发出推送
	e.svc.UseRealtime(pub)

	srv := e.server(t, ctx, "rt-uncommit")
	_ = e.ssNode(t, ctx, srv.ID, "n", 21103)
	spy.expectNone(t, 300*time.Millisecond)
}
