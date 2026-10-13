package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	rosterServerID = uuid.MustParse("00000000-0000-4000-8000-000000000010")
	nodeA          = uuid.MustParse("00000000-0000-4000-8000-000000000011")
	nodeB          = uuid.MustParse("00000000-0000-4000-8000-000000000012")
)

func openRosterSession(t *testing.T, b *fakeBackend, h Hello) (*Sessions, *Session) {
	t.Helper()
	m := NewSessionManager(b, nil, fastOptions())
	s, err := m.Open(context.Background(), rosterServerID, h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, s
}

func drainSession(t *testing.T, s *Session) {
	t.Helper()
	for _, o := range pending(s) {
		_ = o
	}
}

func TestHelloAlignedSendsNothing(t *testing.T) {
	b := newFakeBackend()
	docA := mkDoc(nodeA, 'a')
	b.setConfigs(rosterServerID, []NodeConfigDoc{docA})
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2)})

	_, s := openRosterSession(t, b, Hello{
		Features:       []string{FeatureUsersDelta},
		AppliedConfigs: map[uuid.UUID]string{nodeA: docA.SHA256},
		AppliedUsers:   map[uuid.UUID]string{nodeA: "u1-v1"},
	})

	msgs := pending(s)
	if len(msgs) != 1 || msgs[0].Kind != OutWelcome {
		t.Fatalf("除 Welcome 外不应有消息，得 %d 条", len(msgs))
	}
}

func TestHelloMismatchSendsFull(t *testing.T) {
	b := newFakeBackend()
	b.setConfigs(rosterServerID, []NodeConfigDoc{mkDoc(nodeA, 'a')})
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2)})

	_, s := openRosterSession(t, b, Hello{Features: []string{FeatureUsersDelta}})

	w := nextNow(t, s, time.Second)
	if w.Kind != OutWelcome {
		t.Fatalf("第一条应为 Welcome")
	}
	cfg := nextNow(t, s, time.Second)
	if cfg.Kind != OutConfig {
		t.Fatalf("第二条应为 Config，得 %v", cfg.Kind)
	}
	if len(cfg.Config.Nodes) != 1 {
		t.Fatalf("Config 应含 1 个节点")
	}
	if cfg.Config.ReleaseID == "" || cfg.Config.ReleaseID[:3] != "r1-" {
		t.Fatalf("ReleaseID 应以 r1- 开头，得 %q", cfg.Config.ReleaseID)
	}
	full := nextNow(t, s, time.Second)
	if full.Kind != OutUsersFull || full.Roster == nil || full.Roster.Version != "u1-v1" {
		t.Fatalf("第三条应为 UsersFull u1-v1，得 %v", full.Kind)
	}
}

func TestConfigOnlyLatestKept(t *testing.T) {
	b := newFakeBackend()
	m, s := openRosterSession(t, b, Hello{})
	drainSession(t, s)

	docs1 := []NodeConfigDoc{mkDoc(nodeA, 'a')}
	docs2 := []NodeConfigDoc{mkDoc(nodeA, 'b')}
	s.applyConfig(m.seq.Add(1), docs1)
	s.applyConfig(m.seq.Add(1), docs2)

	msgs := pending(s)
	if len(msgs) != 1 {
		t.Fatalf("应只排一条 Config，得 %d", len(msgs))
	}
	if msgs[0].Config.Nodes[0].SHA256 != docs2[0].SHA256 {
		t.Fatalf("应是 docs2 的内容")
	}

	s.applyConfig(m.seq.Add(1), docs2)
	if len(pending(s)) != 0 {
		t.Fatalf("相同 want 不应再排 Config")
	}
}

func TestRosterOnlyLatestPerNode(t *testing.T) {
	b := newFakeBackend()
	m, s := openRosterSession(t, b, Hello{Features: []string{FeatureUsersDelta}})
	drainSession(t, s)

	rA1 := mkRoster(nodeA, "a1", 1)
	rA2 := mkRoster(nodeA, "a2", 2)
	rB := mkRoster(nodeB, "b1", 10)
	s.applyRosters(m.seq.Add(1), []NodeRoster{rA1, rB})
	s.applyRosters(m.seq.Add(1), []NodeRoster{rA2, rB})

	msgs := pending(s)
	var nodeAMsg, nodeBMsg *Outbound
	for i := range msgs {
		if msgs[i].Roster == nil {
			continue
		}
		switch msgs[i].Roster.NodeID {
		case nodeA:
			nodeAMsg = &msgs[i]
		case nodeB:
			nodeBMsg = &msgs[i]
		}
	}
	if nodeAMsg == nil || nodeAMsg.Roster.Version != "u1-a2" {
		t.Fatalf("nodeA 应为后一份名单")
	}
	if nodeBMsg == nil || nodeBMsg.Roster.Version != "u1-b1" {
		t.Fatalf("nodeB 应有一份名单")
	}
	if len(msgs) != 2 {
		t.Fatalf("应两条名单消息，得 %d", len(msgs))
	}
}

func TestStaleSeqIgnored(t *testing.T) {
	b := newFakeBackend()
	_, s := openRosterSession(t, b, Hello{})
	drainSession(t, s)

	docsX := []NodeConfigDoc{mkDoc(nodeA, 'a')}
	docsY := []NodeConfigDoc{mkDoc(nodeA, 'b')}
	s.applyConfig(5, docsX)
	s.applyConfig(3, docsY)

	msgs := pending(s)
	if len(msgs) != 1 || msgs[0].Config.Nodes[0].SHA256 != docsX[0].SHA256 {
		t.Fatalf("应仍为序号 5 的 X")
	}

	r1 := mkRoster(nodeA, "r1", 1)
	r2 := mkRoster(nodeA, "r2", 2)
	s.applyRosters(5, []NodeRoster{r1})
	s.applyRosters(3, []NodeRoster{r2})
	msgs = pending(s)
	if len(msgs) != 1 || msgs[0].Roster.Version != "u1-r1" {
		t.Fatalf("名单应仍为 r1")
	}
}

func TestDeltaRequiresConfirmedFrom(t *testing.T) {
	b := newFakeBackend()
	// 名单要够大：增量（变更条数）比全量（人数）小才发增量
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2, 4, 5)})
	_, s := openRosterSession(t, b, Hello{
		Features:     []string{FeatureUsersDelta},
		AppliedUsers: map[uuid.UUID]string{nodeA: "u1-v1"},
	})
	drainSession(t, s)

	m := s.m
	v2 := mkRoster(nodeA, "v2", 1, 3, 4, 5)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v2})
	delta := nextNow(t, s, time.Second)
	if delta.Kind != OutUsersDelta || delta.DeltaFrom != "u1-v1" {
		t.Fatalf("v2 应为增量，得 kind=%v from=%q", delta.Kind, delta.DeltaFrom)
	}
	if len(delta.Upserted) != 1 || delta.Upserted[0].UID != 3 || len(delta.Removed) != 1 || delta.Removed[0] != 2 {
		t.Fatalf("增量内容不对: upsert=%v remove=%v", delta.Upserted, delta.Removed)
	}

	v3 := mkRoster(nodeA, "v3", 1, 3, 4, 5, 6)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v3})
	full := nextNow(t, s, time.Second)
	if full.Kind != OutUsersFull {
		t.Fatalf("未 ack 的 v3 应全量，得 %v", full.Kind)
	}
	_ = full

	s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: "u1-v3", OK: true})
	v4 := mkRoster(nodeA, "v4", 1, 3, 4, 5, 6, 7)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v4})
	delta2 := nextNow(t, s, time.Second)
	if delta2.Kind != OutUsersDelta || delta2.DeltaFrom != "u1-v3" {
		t.Fatalf("v4 应从 u1-v3 增量，得 kind=%v from=%q", delta2.Kind, delta2.DeltaFrom)
	}

	// 已发 [1]，新名单 [7,8] 增量不比全量小
	sentOnly := mkRoster(nodeA, "s1", 1)
	s.applyRosters(m.seq.Add(1), []NodeRoster{sentOnly})
	_ = nextNow(t, s, time.Second)
	s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: "u1-s1", OK: true})
	big := mkRoster(nodeA, "big", 7, 8)
	s.applyRosters(m.seq.Add(1), []NodeRoster{big})
	bigOut := nextNow(t, s, time.Second)
	if bigOut.Kind != OutUsersFull {
		t.Fatalf("大改应全量，得 %v", bigOut.Kind)
	}
}

func TestNoDeltaWithoutFeature(t *testing.T) {
	b := newFakeBackend()
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2)})
	m, s := openRosterSession(t, b, Hello{
		AppliedUsers: map[uuid.UUID]string{nodeA: "u1-v1"},
	})
	drainSession(t, s)

	v2 := mkRoster(nodeA, "v2", 1, 3)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v2})
	out := nextNow(t, s, time.Second)
	if out.Kind != OutUsersFull {
		t.Fatalf("无 users_delta 应全量，得 %v", out.Kind)
	}
}

func TestUsersAckFailureSendsFull(t *testing.T) {
	b := newFakeBackend()
	m, s := openRosterSession(t, b, Hello{
		Features:     []string{FeatureUsersDelta},
		AppliedUsers: map[uuid.UUID]string{nodeA: "u1-v1"},
	})
	drainSession(t, s)

	v2 := mkRoster(nodeA, "v2", 1, 3)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v2})
	_ = nextNow(t, s, time.Second)

	s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: "u1-v2", OK: false, ReasonCode: "version_mismatch"})
	full := nextNow(t, s, time.Second)
	if full.Kind != OutUsersFull || full.Roster.Version != "u1-v2" {
		t.Fatalf("失败应全量 v2，得 %v", full)
	}

	s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: "u1-v2", OK: false, ReasonCode: "version_mismatch"})
	if len(pending(s)) != 0 {
		t.Fatalf("间隔内不应再排全量")
	}
	var got []Outbound
	waitFor(t, 500*time.Millisecond, func() bool {
		got = append(got, pending(s)...)
		return len(got) >= 1
	}, "ForcedFullGap 后第二条全量")
	if again := got[0]; again.Kind != OutUsersFull {
		t.Fatalf("间隔后应全量，得 %v", again.Kind)
	}
}

func TestNodeRemovedThenBack(t *testing.T) {
	b := newFakeBackend()
	m, s := openRosterSession(t, b, Hello{Features: []string{FeatureUsersDelta}})
	drainSession(t, s)

	rA := mkRoster(nodeA, "a", 1)
	rB := mkRoster(nodeB, "b", 2)
	s.applyRosters(m.seq.Add(1), []NodeRoster{rA, rB})
	// nodeB 仍在队列、未出队时从结果里拿掉
	s.applyRosters(m.seq.Add(1), []NodeRoster{rA})
	msgs := pending(s)
	for _, o := range msgs {
		if o.Roster != nil && o.Roster.NodeID == nodeB {
			t.Fatalf("未发出的 nodeB 名单应被撤掉")
		}
	}

	s.applyRosters(m.seq.Add(1), []NodeRoster{rA, rB})
	back := nextNow(t, s, time.Second)
	if back.Kind != OutUsersFull || back.Roster.NodeID != nodeB {
		t.Fatalf("nodeB 回来应全量，得 %v", back)
	}
}

func TestMembershipFollowsSentRoster(t *testing.T) {
	b := newFakeBackend()
	m, s := openRosterSession(t, b, Hello{Features: []string{FeatureUsersDelta}})
	drainSession(t, s)

	v1 := mkRoster(nodeA, "v1", 1, 2)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v1})
	_ = nextNow(t, s, time.Second)

	if !s.Has(nodeA, 1) || s.Has(nodeA, 3) || s.Has(nodeB, 1) {
		t.Fatalf("v1 出队后 Has 不符合预期")
	}

	v2 := mkRoster(nodeA, "v2", 3)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v2})
	if s.Has(nodeA, 3) {
		t.Fatalf("v2 未出队前 Has(3) 应为假")
	}
	_ = nextNow(t, s, time.Second)
	if !s.Has(nodeA, 3) {
		t.Fatalf("v2 出队后 Has(3) 应为真")
	}
}

func TestConfigAckOnlyLatest(t *testing.T) {
	b := newFakeBackend()
	docA := mkDoc(nodeA, 'a')
	b.setConfigs(rosterServerID, []NodeConfigDoc{docA})
	ctx := context.Background()

	_, s := openRosterSession(t, b, Hello{})
	_ = nextNow(t, s, time.Second) // Welcome

	ack := ConfigAck{NodeID: nodeA, SHA256: docA.SHA256, Phase: ConfigSwitched}
	s.HandleConfigAck(ctx, ack)
	if len(b.configAcks) != 0 {
		t.Fatalf("出队前 ack 不应落库")
	}

	cfg := nextNow(t, s, time.Second)
	if cfg.Kind != OutConfig {
		t.Fatalf("应有 Config 待出队")
	}
	s.HandleConfigAck(ctx, ack)
	if len(b.configAcks) != 1 {
		t.Fatalf("出队后应落库一次，得 %d", len(b.configAcks))
	}

	bad := ConfigAck{NodeID: nodeA, SHA256: "wrong", Phase: ConfigSwitched}
	s.HandleConfigAck(ctx, bad)
	if len(b.configAcks) != 1 {
		t.Fatalf("摘要不对不应再落库，得 %d 次", len(b.configAcks))
	}
}
