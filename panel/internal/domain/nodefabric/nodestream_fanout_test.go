package nodefabric

import (
	"encoding/json"
	"testing"
)

// 扇出把同池节点合成一次用户集计算，全量载荷按版本只编码一次。
func TestPushUsersSharesFullPayloadAcrossNodes(t *testing.T) {
	h := NewStreamHub()
	a := NewStreamConn("t1", "n1", 4)
	b := NewStreamConn("t1", "n2", 4)
	h.Add(a)
	h.Add(b)
	users := []ProxyUser{{ID: 1, UUID: "u-1"}}
	version := UserSetVersion(users)
	shared := fullUsersPayloads{}
	h.pushUsers("t1", "n1", users, version, nil, shared)
	h.pushUsers("t1", "n2", users, version, nil, shared)
	if len(shared) != 1 {
		t.Fatalf("payload encoded %d times for one version", len(shared))
	}
	for _, c := range []*StreamConn{a, b} {
		m := drain(t, c)
		var p SyncUsersPayload
		if m.Event != EventSyncUsers || json.Unmarshal(m.Data, &p) != nil || p.Version != version || len(p.Users) != 1 {
			t.Fatalf("conn %s got %+v", c.NodeID, m)
		}
	}
}

func TestStreamPushQueueCoalesces(t *testing.T) {
	q := newStreamPushQueue()
	q.addNode("n1")
	q.addNode("n1")
	q.addAllUsers()
	q.addAllUsers()
	select {
	case <-q.wake:
	default:
		t.Fatal("queue did not wake the worker")
	}
	select {
	case <-q.wake:
		t.Fatal("queue woke the worker once per event instead of coalescing")
	default:
	}
	nodes, all := q.take()
	if len(nodes) != 1 || nodes[0] != "n1" || !all {
		t.Fatalf("take = %v %v", nodes, all)
	}
	if nodes, all := q.take(); len(nodes) != 0 || all {
		t.Fatalf("queue kept work after take: %v %v", nodes, all)
	}
}
