package service

// 会话管理单测共用的假数据面与小工具（不连库）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeBackend 实现 SessionBackend：名单与配置取自可改的表，入账与落库只记录调用。
type fakeBackend struct {
	mu sync.Mutex
	// rosters、configs：服务器 id → 当前结果
	rosters map[uuid.UUID][]NodeRoster
	configs map[uuid.UUID][]NodeConfigDoc
	// rosterErr、configErr 非 nil 时 Rosters / ServerConfigs 返回它
	rosterErr, configErr error
	// trafficErr 是 IngestTraffic 的返回值
	trafficErr error
	// renewErr 是 RenewCertificate 的返回值
	renewErr error

	// 调用记录
	rosterCalls, configCalls [][]uuid.UUID
	traffic                  []TrafficReport
	online                   [][]OnlineEntry
	metrics                  []HostMetrics
	hellos                   []Hello
	heartbeats               [][]NodeRuntime
	configAcks               []ConfigAck
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{rosters: map[uuid.UUID][]NodeRoster{}, configs: map[uuid.UUID][]NodeConfigDoc{}}
}

func (f *fakeBackend) setRosters(serverID uuid.UUID, r []NodeRoster) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rosters[serverID] = r
}

func (f *fakeBackend) setConfigs(serverID uuid.UUID, c []NodeConfigDoc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs[serverID] = c
}

func (f *fakeBackend) Rosters(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]NodeRoster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rosterCalls = append(f.rosterCalls, append([]uuid.UUID(nil), ids...))
	if f.rosterErr != nil {
		return nil, f.rosterErr
	}
	out := map[uuid.UUID][]NodeRoster{}
	for _, id := range ids {
		if r, ok := f.rosters[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (f *fakeBackend) ServerConfigs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configCalls = append(f.configCalls, append([]uuid.UUID(nil), ids...))
	if f.configErr != nil {
		return nil, f.configErr
	}
	out := map[uuid.UUID][]NodeConfigDoc{}
	for _, id := range ids {
		if c, ok := f.configs[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (f *fakeBackend) IngestTraffic(_ context.Context, _ uuid.UUID, r TrafficReport, _ RosterMembership) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.traffic = append(f.traffic, r)
	return f.trafficErr
}

func (f *fakeBackend) IngestOnline(_ context.Context, _ uuid.UUID, e []OnlineEntry, _ RosterMembership) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = append(f.online, e)
	return nil
}

func (f *fakeBackend) RecordMetrics(_ context.Context, _ uuid.UUID, m HostMetrics, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = append(f.metrics, m)
	return nil
}

func (f *fakeBackend) RenewCertificate(_ context.Context, _ uuid.UUID, _ []byte) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return nil, nil, f.renewErr
	}
	return []byte("CHAIN"), []byte("CA"), nil
}

func (f *fakeBackend) RecordHello(_ context.Context, _ uuid.UUID, h Hello, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hellos = append(f.hellos, h)
	return nil
}

func (f *fakeBackend) RecordHeartbeat(_ context.Context, _ uuid.UUID, nodes []NodeRuntime, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, nodes)
	return nil
}

func (f *fakeBackend) RecordConfigAck(_ context.Context, _ uuid.UUID, a ConfigAck, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configAcks = append(f.configAcks, a)
	return nil
}

// counts 返回 Rosters、ServerConfigs 各被调了几次。
func (f *fakeBackend) counts() (rosters, configs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rosterCalls), len(f.configCalls)
}

// fastOptions 是测试用的短间隔参数。
func fastOptions() SessionOptions {
	return SessionOptions{
		QueueLimit:     64,
		EpochPoll:      20 * time.Millisecond,
		FullResync:     60 * time.Millisecond,
		MinRoundGap:    50 * time.Millisecond,
		RetryDelay:     30 * time.Millisecond,
		ForcedFullGap:  80 * time.Millisecond,
		TerminateGrace: 200 * time.Millisecond,
		ShutdownBase:   time.Second,
		ShutdownSpread: 3 * time.Second,
	}
}

// mkRoster 造一份名单：版本 "u1-"+tag，用户 uid 依次为 uids（须升序）。
func mkRoster(nodeID uuid.UUID, tag string, uids ...int64) NodeRoster {
	r := NodeRoster{NodeID: nodeID, Version: "u1-" + tag}
	for _, uid := range uids {
		r.Users = append(r.Users, RosterUser{UID: uid, UUID: uuid.NewSHA1(uuid.Nil, []byte{byte(uid)})})
	}
	return r
}

// mkDoc 造一份配置文档，摘要是 64 位 hex（由 tag 重复填充，测试里只比字符串）。
func mkDoc(nodeID uuid.UUID, tag byte) NodeConfigDoc {
	sha := make([]byte, 64)
	for i := range sha {
		sha[i] = tag
	}
	return NodeConfigDoc{NodeID: nodeID, NodeType: "vless", JSON: []byte(`{}`), SHA256: string(sha)}
}

// nextNow 在 d 内取下一条消息；超时或出错时测试失败。
func nextNow(t *testing.T, s *Session, d time.Duration) Outbound {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := s.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return out
}

// pending 不阻塞地取走队列里现有的全部消息。
func pending(s *Session) []Outbound {
	var out []Outbound
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		o, err := s.Next(ctx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, o)
	}
}

// waitFor 轮询 cond 直到为真或超时。
func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}
