package service

// 节点会话写入与在线巡检的实时推送（连库）。

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

func TestRealtimeHelloFlipsOnlinePG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	srv := e.server(t, ctx, "rt-live")
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	at := time.Now().UTC()
	if err := e.svc.RecordHello(ctx, srv.ID, Hello{}, at); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": srv.ID.String(),
	})

	if err := e.svc.RecordHeartbeat(ctx, srv.ID, nil, at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	spy.expectNone(t, 300*time.Millisecond)
}

func TestRealtimeHeartbeatAfterGapPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	srv := e.server(t, ctx, "rt-gap")
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	t0 := time.Now().UTC()
	if err := e.svc.RecordHeartbeat(ctx, srv.ID, nil, t0); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": srv.ID.String(),
	})

	if err := e.svc.RecordHeartbeat(ctx, srv.ID, nil, t0.Add(NodeStaleAfter)); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "servers", "op": "UPDATE", "id": srv.ID.String(),
	})
}

func TestRealtimeRuntimeChangePG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	srv := e.server(t, ctx, "rt-live-n-srv")
	n := e.ssNode(t, ctx, srv.ID, "rt-live-n", 21170)
	pub, spy := newRTSpy(t, realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	at := time.Now().UTC()
	if err := e.svc.RecordHeartbeat(ctx, srv.ID, []NodeRuntime{{
		NodeID: n.ID, Status: RuntimeDegraded, Reason: "port_in_use",
	}}, at); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicNodes, map[string]any{
		"table": "nodes", "op": "UPDATE", "id": n.ID.String(),
	})

	if err := e.svc.RecordHeartbeat(ctx, srv.ID, []NodeRuntime{{
		NodeID: n.ID, Status: RuntimeDegraded, Reason: "port_in_use",
	}}, at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	spy.expectNone(t, 300*time.Millisecond)
}
