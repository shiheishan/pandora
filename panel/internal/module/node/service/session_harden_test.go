package service

// 会话加固的单测：新会话开头的计费、增量上限、强制全量退避、Welcome 一定第一条、分开取号、上报与续期的回执。

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHasFallsBackToLatestBeforeSent(t *testing.T) {
	b := newFakeBackend()
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2)})
	_, s := openRosterSession(t, b, Hello{})

	// 全量还在队列里（sent 为空）：按面板算出的最新名单判定
	if !s.Has(nodeA, 1) {
		t.Fatal("名单还没发出时，latest 里的用户也应计费")
	}
	if s.Has(nodeA, 3) {
		t.Fatal("不在名单里的用户不应计费")
	}
	drainSession(t, s)
	if !s.Has(nodeA, 1) {
		t.Fatal("名单发出后用户仍应计费")
	}
}

func TestDeltaCappedAtChunkSize(t *testing.T) {
	n := 4 * UsersChunkSize
	uids := make([]int64, n)
	for i := range uids {
		uids[i] = int64(i + 1)
	}
	v1 := mkRoster(nodeA, "v1", uids...)
	b := newFakeBackend()
	b.setRosters(rosterServerID, []NodeRoster{v1})
	m, s := openRosterSession(t, b, Hello{
		Features:     []string{FeatureUsersDelta},
		AppliedUsers: map[uuid.UUID]string{nodeA: v1.Version},
	})
	drainSession(t, s)

	// 变更 UsersChunkSize+1 条：超过一块，发全量
	v2 := mkRoster(nodeA, "v2", uids[UsersChunkSize+1:]...)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v2})
	out := nextNow(t, s, time.Second)
	if out.Kind != OutUsersFull || out.Roster.Version != v2.Version {
		t.Fatalf("变更超过一块应发全量，得 %v", out.Kind)
	}
	s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: v2.Version, OK: true})

	// 变更正好 UsersChunkSize 条、且少于全量人数：发增量
	v3 := mkRoster(nodeA, "v3", uids[2*UsersChunkSize+1:]...)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v3})
	out = nextNow(t, s, time.Second)
	if out.Kind != OutUsersDelta || out.DeltaFrom != v2.Version {
		t.Fatalf("变更正好一块应发增量，得 kind=%v from=%q", out.Kind, out.DeltaFrom)
	}
	if len(out.Removed) != UsersChunkSize || len(out.Upserted) != 0 {
		t.Fatalf("增量内容：removed=%d upserted=%d", len(out.Removed), len(out.Upserted))
	}
}

func TestForcedFullBackoffDoublesAndResets(t *testing.T) {
	clk := newFakeClock()
	opt := fastOptions()
	opt.ForcedFullGap = 50 * time.Millisecond
	opt.ForcedFullMax = 150 * time.Millisecond
	b := newFakeBackend()
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2)})
	_, s := openRosterSessionWith(t, b, Hello{}, opt, clk)
	drainSession(t, s)

	fail := func() {
		s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: "u1-v1", OK: false, ReasonCode: "apply_failed"})
	}
	gap := func() time.Duration {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.nodes[nodeA].forceGap
	}
	expectFull := func(step string, wantGap time.Duration) {
		t.Helper()
		msgs := pending(s)
		if len(msgs) != 1 || msgs[0].Kind != OutUsersFull {
			t.Fatalf("%s：应立即排一条全量，得 %d 条", step, len(msgs))
		}
		if g := gap(); g != wantGap {
			t.Fatalf("%s：forceGap=%v，期望 %v", step, g, wantGap)
		}
	}

	fail()
	expectFull("第 1 次失败", 50*time.Millisecond)
	clk.Add(60 * time.Millisecond)
	fail()
	expectFull("第 2 次失败", 100*time.Millisecond)
	clk.Add(110 * time.Millisecond)
	fail()
	expectFull("第 3 次失败", 150*time.Millisecond)
	clk.Add(160 * time.Millisecond)
	fail()
	expectFull("第 4 次失败（封顶）", 150*time.Millisecond)

	s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: "u1-v1", OK: true})
	if g := gap(); g != 0 {
		t.Fatalf("成功回执应复位，forceGap=%v", g)
	}
	clk.Add(time.Millisecond)
	fail()
	expectFull("复位后失败", 50*time.Millisecond)
}

func TestForcedFullNoDuplicateWithNewVersion(t *testing.T) {
	clk := newFakeClock()
	opt := fastOptions()
	opt.ForcedFullGap = 100 * time.Millisecond
	b := newFakeBackend()
	b.setRosters(rosterServerID, []NodeRoster{mkRoster(nodeA, "v1", 1, 2)})
	m, s := openRosterSessionWith(t, b, Hello{}, opt, clk)
	drainSession(t, s)

	fail := func(version string) {
		s.HandleUsersAck(UsersAck{NodeID: nodeA, Version: version, OK: false, ReasonCode: "apply_failed"})
	}
	fail("u1-v1")
	if msgs := pending(s); len(msgs) != 1 || msgs[0].Kind != OutUsersFull {
		t.Fatalf("第一次失败应立即全量，得 %d 条", len(msgs))
	}
	fail("u1-v1")
	if msgs := pending(s); len(msgs) != 0 {
		t.Fatalf("间隔内不应再排，得 %d 条", len(msgs))
	}

	// 定时器在等时新版本到了：它的全量已回应这次失败，定时器到点不再补发
	v2 := mkRoster(nodeA, "v2", 1, 3)
	s.applyRosters(m.seq.Add(1), []NodeRoster{v2})
	time.Sleep(400 * time.Millisecond)
	msgs := pending(s)
	if len(msgs) != 1 || msgs[0].Kind != OutUsersFull || msgs[0].Roster.Version != v2.Version {
		t.Fatalf("应只有一条 v2 全量，得 %d 条", len(msgs))
	}

	// v2 的回执又失败：拨过间隔（假时钟不动时定时器到点仍算在间隔内），应补发 v2 全量
	clk.Add(200 * time.Millisecond)
	fail(v2.Version)
	var got []Outbound
	waitFor(t, 2*time.Second, func() bool {
		got = append(got, pending(s)...)
		return len(got) >= 1
	}, "v2 失败后的全量")
	if got[0].Kind != OutUsersFull || got[0].Roster.Version != v2.Version {
		t.Fatalf("应补发 v2 全量，得 %v", got[0].Kind)
	}
}

func TestOpenQueuesWelcomeFirstDespiteConcurrentRound(t *testing.T) {
	b := newFakeBackend()
	serverID, nodeID := uuid.New(), uuid.New()
	b.setConfigs(serverID, []NodeConfigDoc{mkDoc(nodeID, 'a')})
	b.setRosters(serverID, []NodeRoster{mkRoster(nodeID, "v1", 1)})
	m := NewSessionManager(b, nil, fastOptions())
	fired := false
	// Open 读配置时会话已登记：此刻插进一轮重算，它给这条会话排的配置与名单不能排到 Welcome 前面
	b.configsHook = func() {
		if fired {
			return
		}
		fired = true
		m.round(context.Background(), true, true)
	}
	s, err := m.Open(context.Background(), serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !fired {
		t.Fatal("钩子没触发")
	}
	if out := nextNow(t, s, time.Second); out.Kind != OutWelcome {
		t.Fatalf("第一条应为 Welcome，得 %v", out.Kind)
	}
	kinds := map[OutKind]int{}
	for _, o := range pending(s) {
		kinds[o.Kind]++
	}
	if kinds[OutConfig] != 1 || kinds[OutUsersFull] != 1 {
		t.Fatalf("应各有一条配置与全量，得 %v", kinds)
	}
}

func TestTombstoneDuringOpenKeepsWelcomeFirst(t *testing.T) {
	b := newFakeBackend()
	serverID := uuid.New()
	m := NewSessionManager(b, nil, fastOptions())
	fired := false
	b.configsHook = func() {
		if fired {
			return
		}
		fired = true
		m.Tombstone(serverID, TombstoneIdentityRevoked)
	}
	s, err := m.Open(context.Background(), serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if out := nextNow(t, s, time.Second); out.Kind != OutWelcome {
		t.Fatalf("第一条应为 Welcome，得 %v", out.Kind)
	}
	out := nextNow(t, s, time.Second)
	if out.Kind != OutTombstone || out.Reason != TombstoneIdentityRevoked {
		t.Fatalf("第二条应为 Tombstone，得 %v %q", out.Kind, out.Reason)
	}
	waitFor(t, 3*time.Second, func() bool {
		select {
		case <-s.Done():
			return true
		default:
			return false
		}
	}, "Tombstone 发出后会话结束")
	if !errors.Is(s.Err(), ErrSessionTombstoned) {
		t.Fatalf("Err 应为 ErrSessionTombstoned，得 %v", s.Err())
	}
}

func TestOpenAndRoundTakeSeparateSeqs(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	ctx := context.Background()
	before := m.seq.Load()
	if _, err := m.Open(ctx, uuid.New(), Hello{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := m.seq.Load() - before; got != 2 {
		t.Fatalf("Open 应取 2 个序号（配置、名单各一），得 %d", got)
	}
	before = m.seq.Load()
	m.round(ctx, true, true)
	if got := m.seq.Load() - before; got != 2 {
		t.Fatalf("两样都算的一轮应取 2 个序号，得 %d", got)
	}
	before = m.seq.Load()
	m.round(ctx, true, false)
	if got := m.seq.Load() - before; got != 1 {
		t.Fatalf("只算名单的一轮应取 1 个序号，得 %d", got)
	}
}

func TestTrafficInvalidStillAcked(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	ctx := context.Background()
	s, err := m.Open(ctx, uuid.New(), Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = nextNow(t, s, time.Second) // Welcome

	setErr := func(e error) {
		b.mu.Lock()
		b.trafficErr = e
		b.mu.Unlock()
	}
	for _, c := range []struct {
		id  string
		err error
	}{
		{"r-inv", fmt.Errorf("%w: 条目过多", ErrTrafficReportInvalid)},
		{"r-conf", fmt.Errorf("%w: 内容不同", ErrTrafficReportConflict)},
	} {
		setErr(c.err)
		s.HandleTraffic(ctx, TrafficReport{ReportID: c.id})
		msgs := pending(s)
		if len(msgs) != 1 || msgs[0].Kind != OutTrafficAck || msgs[0].ReportID != c.id {
			t.Fatalf("%s 应照样回 ack，得 %d 条", c.id, len(msgs))
		}
	}
	setErr(errors.New("db"))
	s.HandleTraffic(ctx, TrafficReport{ReportID: "r-db"})
	if msgs := pending(s); len(msgs) != 0 {
		t.Fatalf("入账失败不应回 ack，得 %d 条", len(msgs))
	}
}

func TestRenewPassesSerialAndTombstones(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	ctx := context.Background()
	s, err := m.Open(ctx, uuid.New(), Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = nextNow(t, s, time.Second) // Welcome

	s.HandleRenew(ctx, big.NewInt(42), []byte("csr"))
	if out := nextNow(t, s, time.Second); out.Kind != OutCertificate {
		t.Fatalf("续期成功应回 Certificate，得 %v", out.Kind)
	}
	b.mu.Lock()
	serials := append([]*big.Int(nil), b.renewSerials...)
	b.mu.Unlock()
	if len(serials) != 1 || serials[0].Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("续期应带建流证书的序列号，得 %v", serials)
	}

	b.mu.Lock()
	b.renewErr = ErrSessionRevoked
	b.mu.Unlock()
	s.HandleRenew(ctx, big.NewInt(42), []byte("csr"))
	if !s.Ending() {
		t.Fatal("身份失效后会话应在结束中")
	}
	out := nextNow(t, s, time.Second)
	if out.Kind != OutTombstone || out.Reason != TombstoneIdentityRevoked {
		t.Fatalf("应发 Tombstone（%s），得 %v %q", TombstoneIdentityRevoked, out.Kind, out.Reason)
	}
}

func TestEndingAfterTombstone(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	s, err := m.Open(context.Background(), uuid.New(), Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Ending() {
		t.Fatal("新会话不应在结束中")
	}
	s.Tombstone("x")
	if !s.Ending() {
		t.Fatal("Tombstone 后应在结束中")
	}
}
