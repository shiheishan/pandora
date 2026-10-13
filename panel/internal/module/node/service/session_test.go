package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOpenSendsWelcomeFirst(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{
		AgentVersion: "v1",
		Features:     []string{FeatureUsersDelta, "foo"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	out := nextNow(t, s, time.Second)
	if out.Kind != OutWelcome {
		t.Fatalf("第一条应为 Welcome，实际 %v", out.Kind)
	}
	if out.Welcome.SessionID != s.ID() {
		t.Fatalf("SessionID 不一致")
	}
	if len(out.Welcome.Features) != 1 || out.Welcome.Features[0] != FeatureUsersDelta {
		t.Fatalf("Features 应为仅 users_delta，得 %v", out.Welcome.Features)
	}
	if out.Welcome.PushInterval != 60*time.Second || out.Welcome.HeartbeatInterval != 30*time.Second {
		t.Fatalf("间隔应为 60s/30s，得 %v/%v", out.Welcome.PushInterval, out.Welcome.HeartbeatInterval)
	}
	if len(b.hellos) != 1 {
		t.Fatalf("RecordHello 应记 1 次，得 %d", len(b.hellos))
	}
}

func TestOpenSupersedesOldStream(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s1, err := m.Open(ctx, serverID, Hello{})
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	s2, err := m.Open(ctx, serverID, Hello{})
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	if m.Len() != 1 {
		t.Fatalf("Len 应为 1，得 %d", m.Len())
	}

	_, err = s1.Next(context.Background())
	if !errors.Is(err, ErrSessionSuperseded) {
		t.Fatalf("旧流 Next 应为 ErrSessionSuperseded，得 %v", err)
	}

	s1.Close(nil)
	if !m.Online(serverID) {
		t.Fatalf("关闭旧流后新流应仍在")
	}

	s2.Close(nil)
	if m.Len() != 0 {
		t.Fatalf("关闭新流后 Len 应为 0，得 %d", m.Len())
	}
}

func TestOpenBackendErrorClosesSession(t *testing.T) {
	b := newFakeBackend()
	want := fmt.Errorf("配置不可用")
	b.configErr = want
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()

	_, err := m.Open(context.Background(), serverID, Hello{})
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("Open 应返回配置错误，得 %v", err)
	}
	if m.Len() != 0 {
		t.Fatalf("失败后 Len 应为 0，得 %d", m.Len())
	}
}

func TestQueueOverflowClosesSession(t *testing.T) {
	b := newFakeBackend()
	opt := fastOptions()
	opt.QueueLimit = 3
	m := NewSessionManager(b, nil, opt)
	serverID := uuid.New()

	s, err := m.Open(context.Background(), serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		s.AckTraffic(fmt.Sprintf("r%d", i))
	}
	_, err = s.Next(context.Background())
	if !errors.Is(err, ErrSessionOverflow) {
		t.Fatalf("Next 应为 ErrSessionOverflow，得 %v", err)
	}
}

func TestTombstone(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()

	s, err := m.Open(context.Background(), serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.AckTraffic("before-tomb")

	m.Tombstone(serverID, "revoked")

	// 还没发出的 Welcome 留在队首，终止消息紧随其后
	if out := nextNow(t, s, time.Second); out.Kind != OutWelcome {
		t.Fatalf("第一条仍应是 Welcome，得 %v", out.Kind)
	}
	out := nextNow(t, s, time.Second)
	if out.Kind != OutTombstone || out.Reason != "revoked" {
		t.Fatalf("应先 OutTombstone，得 kind=%v reason=%q", out.Kind, out.Reason)
	}
	_, err = s.Next(context.Background())
	if !errors.Is(err, ErrSessionTombstoned) {
		t.Fatalf("再 Next 应为 ErrSessionTombstoned，得 %v", err)
	}

	// 终止前排的普通消息不应再发出
	rest := pending(s)
	for _, o := range rest {
		if o.Kind == OutTrafficAck {
			t.Fatalf("Tombstone 后不应再发 TrafficAck")
		}
	}

	m.Tombstone(uuid.New(), "noop")
}

func TestTombstoneForceClosesWhenNotDrained(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()

	s, err := m.Open(context.Background(), serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m.Tombstone(serverID, "revoked")

	// TerminateGrace 是 200ms；上限放宽到 3 秒，繁忙的 CI 上 -race 也不偶发失败
	waitFor(t, 3*time.Second, func() bool {
		select {
		case <-s.Done():
			return true
		default:
			return false
		}
	}, "Done 在 TerminateGrace 后关闭")

	if !errors.Is(s.Err(), ErrSessionTombstoned) {
		t.Fatalf("Err 应为 ErrSessionTombstoned，得 %v", s.Err())
	}
}

var (
	shutID1 = uuid.MustParse("00000000-0000-4000-8000-000000000001")
	shutID2 = uuid.MustParse("00000000-0000-4000-8000-000000000002")
	shutID3 = uuid.MustParse("00000000-0000-4000-8000-000000000003")
)

func TestShutdownStaggersReconnect(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	ctx := context.Background()
	ids := []uuid.UUID{shutID1, shutID2, shutID3}

	sessions := make([]*Session, 0, 3)
	for _, id := range ids {
		s, err := m.Open(ctx, id, Hello{})
		if err != nil {
			t.Fatalf("Open %s: %v", id, err)
		}
		sessions = append(sessions, s)
	}

	var mu sync.Mutex
	reconnects := make(map[uuid.UUID]Reconnect)
	for _, s := range sessions {
		go func(sess *Session) {
			for {
				out, err := sess.Next(ctx)
				if err != nil {
					sess.Close(err)
					return
				}
				if out.Kind == OutReconnect {
					mu.Lock()
					reconnects[sess.ServerID()] = out.Reconnect
					mu.Unlock()
				}
			}
		}(s)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := m.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	wantIn := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}
	for i, id := range ids {
		mu.Lock()
		rc := reconnects[id]
		mu.Unlock()
		if rc.In != wantIn[i] || rc.Jitter != time.Second {
			t.Fatalf("%s reconnect 应为 in=%v jitter=1s，得 in=%v jitter=%v", id, wantIn[i], rc.In, rc.Jitter)
		}
	}
	if m.Len() != 0 {
		t.Fatalf("Shutdown 后 Len 应为 0，得 %d", m.Len())
	}

	_, err := m.Open(ctx, shutID1, Hello{})
	if !errors.Is(err, ErrSessionShutdown) {
		t.Fatalf("Shutdown 后再 Open 应为 ErrSessionShutdown，得 %v", err)
	}
}

func TestTrafficAck(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = nextNow(t, s, time.Second) // Welcome

	b.trafficErr = nil
	s.HandleTraffic(ctx, TrafficReport{ReportID: "ok1"})
	out := nextNow(t, s, time.Second)
	if out.Kind != OutTrafficAck || out.ReportID != "ok1" {
		t.Fatalf("成功入账应 ack，得 %v", out)
	}

	b.trafficErr = fmt.Errorf("wrap: %w", ErrTrafficReportConflict)
	s.HandleTraffic(ctx, TrafficReport{ReportID: "conflict"})
	out = nextNow(t, s, time.Second)
	if out.Kind != OutTrafficAck || out.ReportID != "conflict" {
		t.Fatalf("冲突也应 ack，得 %v", out)
	}

	b.trafficErr = fmt.Errorf("db down")
	s.HandleTraffic(ctx, TrafficReport{ReportID: "fail"})
	if len(pending(s)) != 0 {
		t.Fatalf("其它错误不应 ack")
	}
}

func TestRenew(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = nextNow(t, s, time.Second)

	b.renewErr = nil
	s.HandleRenew(ctx, big.NewInt(7), []byte("csr"))
	out := nextNow(t, s, time.Second)
	if out.Kind != OutCertificate || string(out.CertChainPEM) != "CHAIN" || string(out.CAPEM) != "CA" {
		t.Fatalf("续期成功应 Certificate，得 %v", out.Kind)
	}

	b.renewErr = ErrSessionRevoked
	s2, err := m.Open(ctx, uuid.New(), Hello{})
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	_ = nextNow(t, s2, time.Second)
	s2.HandleRenew(ctx, big.NewInt(7), []byte("csr"))
	out = nextNow(t, s2, time.Second)
	if out.Kind != OutTombstone || out.Reason != TombstoneIdentityRevoked {
		t.Fatalf("吊销应 Tombstone（%s），得 %v %q", TombstoneIdentityRevoked, out.Kind, out.Reason)
	}
	_, err = s2.Next(context.Background())
	if !errors.Is(err, ErrSessionTombstoned) {
		t.Fatalf("再 Next 应为 ErrSessionTombstoned，得 %v", err)
	}

	b.renewErr = fmt.Errorf("temp fail")
	s3, err := m.Open(ctx, uuid.New(), Hello{})
	if err != nil {
		t.Fatalf("Open s3: %v", err)
	}
	_ = nextNow(t, s3, time.Second)
	s3.HandleRenew(ctx, big.NewInt(7), []byte("csr"))
	if len(pending(s3)) != 0 {
		t.Fatalf("其它续期错误不应发消息")
	}
	if s3.Err() != nil {
		t.Fatalf("会话不应结束，Err=%v", s3.Err())
	}
}

func TestCloseUnregistersOnlySelf(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		s, err := m.Open(ctx, uuid.New(), Hello{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		s.Close(nil)
	}
	if m.Len() != 0 {
		t.Fatalf("全部 Close 后 Len 应为 0，得 %d", m.Len())
	}
}
