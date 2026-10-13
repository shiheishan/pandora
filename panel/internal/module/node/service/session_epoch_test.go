package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// startRun 在后台跑 m.Run，测试结束时 cancel 并等待退出。
func startRun(t *testing.T, m *Sessions) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run 未在限时内退出")
		}
	})
	return ctx
}

func waitSubscribed(t *testing.T, ctx context.Context, rdb *redis.Client, ch string) {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool {
		m, err := rdb.PubSubNumSub(ctx, ch).Result()
		if err != nil {
			return false
		}
		return m[ch] >= 1
	}, "订阅 "+ch)
}

func hasUsersOutbound(ss []Outbound) bool {
	for _, o := range ss {
		if o.Kind == OutUsersFull || o.Kind == OutUsersDelta {
			return true
		}
	}
	return false
}

func waitInitialPollRound(t *testing.T, b *fakeBackend, baseR, baseC int) {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool {
		r, c := b.counts()
		return r >= baseR+1 && c >= baseC+1
	}, "首轮 poll 全量比对结束")
}

func openTestSession(t *testing.T, m *Sessions, b *fakeBackend) (*Session, uuid.UUID, uuid.UUID) {
	t.Helper()
	serverID := uuid.MustParse("00000000-0000-4000-8000-000000000010")
	nodeID := uuid.MustParse("00000000-0000-4000-8000-000000000011")
	doc := mkDoc(nodeID, 'a')
	roster := mkRoster(nodeID, "v1", 1)
	b.setConfigs(serverID, []NodeConfigDoc{doc})
	b.setRosters(serverID, []NodeRoster{roster})
	h := Hello{
		AgentVersion:   "t",
		Features:       []string{FeatureUsersDelta},
		AppliedConfigs: map[uuid.UUID]string{nodeID: doc.SHA256},
		AppliedUsers:   map[uuid.UUID]string{nodeID: roster.Version},
	}
	s, err := m.Open(context.Background(), serverID, h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = pending(s)
	return s, serverID, nodeID
}

func waitUsersOnSession(t *testing.T, s *Session) {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool {
		if hasUsersOutbound(pending(s)) {
			return true
		}
		return false
	}, "会话收到名单下发")
}

func TestPublishTriggersRound(t *testing.T) {
	_, rdb := testkit.Redis(t)
	b := newFakeBackend()
	m := NewSessionManager(b, rdb, fastOptions())
	s, serverID, nodeID := openTestSession(t, m, b)
	baseR, baseC := b.counts()
	ctx := startRun(t, m)
	waitSubscribed(t, ctx, rdb, ChangedChannel)
	waitInitialPollRound(t, b, baseR, baseC)
	ro0, cf0 := b.counts()

	b.setRosters(serverID, []NodeRoster{mkRoster(nodeID, "v2", 1, 2)})
	if err := rdb.Publish(ctx, ChangedChannel, "users").Err(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	waitUsersOnSession(t, s)
	ro1, cf1 := b.counts()
	if ro1 <= ro0 {
		t.Fatalf("Rosters 应增加: before=%d after=%d", ro0, ro1)
	}
	if cf1 != cf0 {
		t.Fatalf("users 通知不应增 ServerConfigs: before=%d after=%d", cf0, cf1)
	}
}

func TestPollDetectsEpochChange(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	b := newFakeBackend()
	m := NewSessionManager(b, rdb, fastOptions())
	_, _, _ = openTestSession(t, m, b)
	baseR, baseC := b.counts()
	mr.Set(UsersEpochKey, "1")
	mr.Set(ConfigEpochKey, "1")
	startRun(t, m)
	waitInitialPollRound(t, b, baseR, baseC)
	ro0, cf0 := b.counts()

	mr.Set(UsersEpochKey, "7")
	waitFor(t, 3*time.Second, func() bool {
		r, _ := b.counts()
		return r > ro0
	}, "poll 发现 users 纪元变化")
	_, cf1 := b.counts()
	if cf1 != cf0 {
		t.Fatalf("仅 users 纪元变不应调 ServerConfigs: %d -> %d", cf0, cf1)
	}
}

func TestNoRedisFullResync(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	_, _, _ = openTestSession(t, m, b)
	startRun(t, m)
	waitFor(t, 500*time.Millisecond, func() bool {
		r, c := b.counts()
		// Open 各调一次，之后至少两轮全量比对
		return r >= 3 && c >= 3
	}, "无 Redis 时 FullResync 全量比对")
}

func TestRoundsCoalesceAndThrottle(t *testing.T) {
	b := newFakeBackend()
	op := fastOptions()
	op.FullResync = time.Hour
	op.MinRoundGap = 100 * time.Millisecond
	op.EpochPoll = 20 * time.Millisecond
	m := NewSessionManager(b, nil, op)
	_, _, _ = openTestSession(t, m, b)
	baseR, _ := b.counts()
	startRun(t, m)
	for i := 0; i < 20; i++ {
		m.markDirty(true, false)
		time.Sleep(time.Millisecond)
	}
	// 300ms 内：首轮 + 至少隔 100ms 的后续轮次，合并后不超过 4 轮
	time.Sleep(300 * time.Millisecond)
	if r, _ := b.counts(); r-baseR < 1 || r-baseR > 4 {
		t.Fatalf("300ms 内 Rosters 应调 1–4 次，得 %d", r-baseR)
	}
}

func TestRoundSkipsWhenNoSessions(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	startRun(t, m)
	m.markDirty(true, true)
	time.Sleep(200 * time.Millisecond)
	if r, c := b.counts(); r != 0 || c != 0 {
		t.Fatalf("无会话不应调后端: rosters=%d configs=%d", r, c)
	}
}

func TestRoundRetriesAfterError(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	s, serverID, nodeID := openTestSession(t, m, b)
	roBase, _ := b.counts()
	b.mu.Lock()
	b.rosterErr = errors.New("暂不可用")
	b.mu.Unlock()
	startRun(t, m)
	m.markDirty(true, false)
	waitFor(t, 3*time.Second, func() bool {
		r, _ := b.counts()
		return r >= roBase+2
	}, "失败后重试 Rosters")
	b.mu.Lock()
	b.rosterErr = nil
	b.mu.Unlock()
	b.setRosters(serverID, []NodeRoster{mkRoster(nodeID, "v9", 9)})
	m.markDirty(true, false)
	waitUsersOnSession(t, s)
}

func TestMissingServerMeansEmptyConfig(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	s, serverID, _ := openTestSession(t, m, b)
	for _, o := range pending(s) {
		if o.Kind == OutConfig && len(o.Config.Nodes) == 0 {
			t.Fatal("初始配置不应为空")
		}
	}
	b.mu.Lock()
	delete(b.configs, serverID)
	b.mu.Unlock()
	startRun(t, m)
	m.markDirty(false, true)
	waitFor(t, 3*time.Second, func() bool {
		for _, o := range pending(s) {
			if o.Kind == OutConfig && len(o.Config.Nodes) == 0 {
				return true
			}
		}
		return false
	}, "缺配置时下发空 Nodes")
}
