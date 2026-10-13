package panel

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/protobuf/proto"
)

func TestSessionHelloAndWelcome(t *testing.T) {
	pki := newTestPKI(t)
	sid := newServerID()
	id := newTestIdentity(t, pki, sid, time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)

	st := fc.takeStream(t, 5*time.Second)
	if err := st.SendWelcome(60, 30); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, s, EventWelcome)
	if ev.Welcome.GetSessionId() != "sess-1" {
		t.Fatalf("session_id: %q", ev.Welcome.GetSessionId())
	}
	if !s.Connected() {
		t.Fatal("Welcome 后应 Connected")
	}
	if !s.SendHeartbeat(&nodev1.Heartbeat{Metrics: &nodev1.HostMetrics{CpuBp: 1}}) {
		t.Fatal("SendHeartbeat 应成功")
	}
	hb := st.Expect(t, "Heartbeat").(*nodev1.Heartbeat)
	if hb.GetMetrics().GetCpuBp() != 1 {
		t.Fatal("心跳内容不一致")
	}
	msgs := st.snapshot()
	if len(msgs) < 2 || msgs[1].GetSeq() != 2 {
		t.Fatalf("心跳 seq 应为 2: %v", msgs)
	}

	if msgs[0].GetHello() == nil || msgs[0].GetSeq() != 1 {
		t.Fatalf("Hello 应是第一条、seq 为 1: %v", msgs[0])
	}
	h := fc.firstHello(0)
	if h.GetAgentVersion() != "test" || h.GetBinarySha256() != "abc" {
		t.Fatal("Hello 版本字段不对")
	}
	if !slices.Contains(h.GetFeatures(), FeatureUsersDelta) {
		t.Fatal("Hello 应带 users_delta")
	}
	if h.GetHost() == nil || h.GetHost().GetHostname() == "" {
		t.Fatal("Hello Host 应非空")
	}
	if len(h.GetAppliedConfigs()) != 1 || h.GetAppliedConfigs()[0].GetNodeId() != "n1" {
		t.Fatal("AppliedConfigs 不对")
	}
	ids := fc.requestIDs()
	if len(ids) != 1 || ids[0] == "" {
		t.Fatal("应有非空 x-request-id")
	}
	leaf := id.Certificate().Leaf
	if fc.leafSerials()[0] == "" || !leafURIMatches(leaf, sid) {
		t.Fatal("客户端证书叶子不对")
	}
	st.end()
}

func TestSessionSendBeforeWelcomeIsRejected(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	st := fc.takeStream(t, 5*time.Second)
	if s.SendHeartbeat(&nodev1.Heartbeat{}) {
		t.Fatal("Welcome 前 SendHeartbeat 应失败")
	}
	_ = st.SendWelcome(1, 1)
	nextEvent(t, s, EventWelcome)
	st.end()
}

func TestSessionConfigAndAcks(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	st := fc.takeStream(t, 5*time.Second)
	_ = st.SendWelcome(1, 1)
	nextEvent(t, s, EventWelcome)
	cfg := &nodev1.ServerConfig{ReleaseId: "r1", Nodes: []*nodev1.NodeConfig{
		{NodeId: "a", NodeType: "vless", ContentSha256: "s1"},
		{NodeId: "b", NodeType: "trojan", ContentSha256: "s2"},
	}}
	_ = st.SendConfig(cfg)
	ev := nextEvent(t, s, EventConfig)
	if !proto.Equal(ev.Config, cfg) {
		t.Fatal("EventConfig 与下发不一致")
	}
	if !s.SendConfigAck(&nodev1.ConfigAck{NodeId: "a", ContentSha256: "s1", Phase: nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED}) {
		t.Fatal("SendConfigAck")
	}
	st.Expect(t, "ConfigAck")
	if !s.SendUsersAck(&nodev1.UsersAck{NodeId: "n1", Version: "v1", Ok: true}) {
		t.Fatal("SendUsersAck")
	}
	st.Expect(t, "UsersAck")
	if !s.SendTraffic(&nodev1.TrafficReport{ReportId: "rep1"}) {
		t.Fatal("SendTraffic")
	}
	st.Expect(t, "Traffic")
	if !s.SendOnline(&nodev1.OnlineIPs{Entries: []*nodev1.OnlineEntry{{NodeId: "n1", Uid: 1}}}) {
		t.Fatal("SendOnline")
	}
	st.Expect(t, "Online")
	st.end()
}

func TestSessionTrafficAckEvent(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	st := fc.takeStream(t, 5*time.Second)
	_ = st.SendWelcome(1, 1)
	nextEvent(t, s, EventWelcome)
	_ = st.SendTrafficAck("r1")
	ev := nextEvent(t, s, EventTrafficAck)
	if ev.ReportID != "r1" {
		t.Fatalf("ReportID=%q", ev.ReportID)
	}
	st.end()
}

func TestSessionReconnectsWithNewRequestID(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	var n atomic.Int32
	var fc *fakeControl
	fc = newFakeControl(func(st *fakeStream) error {
		fc.streamCh <- st
		if n.Add(1) == 1 {
			_ = st.SendWelcome(1, 1)
			return errors.New("故意断开")
		}
		_ = st.SendWelcome(1, 1)
		<-st.done
		return nil
	})
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	nextEvent(t, s, EventWelcome)
	nextEvent(t, s, EventDisconnected)
	nextEvent(t, s, EventWelcome)
	ids := fc.requestIDs()
	if len(ids) < 2 || ids[0] == ids[1] {
		t.Fatalf("两条流 request_id 应不同: %v", ids)
	}
	fc.takeStream(t, 5*time.Second).end()
}

func TestSessionCommandReconnectDelay(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	var n atomic.Int32
	var randArg atomic.Int64
	var stream1End atomic.Int64 // UnixNano：脚本在面板 goroutine 里写、测试里读
	var fc *fakeControl
	fc = newFakeControl(func(st *fakeStream) error {
		fc.streamCh <- st
		_ = st.SendWelcome(1, 1)
		if n.Add(1) == 1 {
			stream1End.Store(time.Now().UnixNano())
			return st.SendReconnect(50, 40)
		}
		<-st.done
		return nil
	})
	s, _, _ := newSessionFixture(t, pki, id, fc, func(o *SessionOptions) {
		o.Rand = func(n int64) int64 {
			randArg.Store(n)
			return n - 1
		}
	})
	nextEvent(t, s, EventWelcome)
	nextEvent(t, s, EventDisconnected)
	nextEvent(t, s, EventWelcome)
	if gap := time.Since(time.Unix(0, stream1End.Load())); gap < 50*time.Millisecond {
		t.Fatalf("第二条流开始太早: %v", gap)
	}
	if randArg.Load() != 40 {
		t.Fatalf("Rand 应以 40 调用，得 %d", randArg.Load())
	}
	fc.takeStream(t, 5*time.Second).end()
}

func TestSessionTombstoneStopsReconnecting(t *testing.T) {
	runTombstone := func(t *testing.T, afterWelcome bool) {
		pki := newTestPKI(t)
		id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
		var fc *fakeControl
		fc = newFakeControl(func(st *fakeStream) error {
			fc.streamCh <- st
			if afterWelcome {
				_ = st.SendWelcome(1, 1)
			}
			return st.SendTombstone("revoked")
		})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		errc := make(chan error, 1)
		dialer := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), nil, fc)
		s, err := NewSession(SessionOptions{
			Address: "127.0.0.1:9443", Identity: id, AgentVersion: "t", BinarySHA256: "b",
			Applied: func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) { return nil, nil },
			Logger:  slog.New(slog.DiscardHandler),
			Dialer:  dialer, Rand: func(int64) int64 { return 0 },
		})
		if err != nil {
			t.Fatal(err)
		}
		go func() { errc <- s.Run(ctx) }()
		if afterWelcome {
			nextEvent(t, s, EventWelcome)
		}
		ev := nextEvent(t, s, EventTombstone)
		if ev.Reason != "revoked" {
			t.Fatalf("reason=%q", ev.Reason)
		}
		select {
		case err := <-errc:
			if !errors.Is(err, ErrTombstoned) {
				t.Fatalf("Run err=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run 未返回 ErrTombstoned")
		}
		n := fc.streams()
		time.Sleep(300 * time.Millisecond)
		if fc.streams() != n {
			t.Fatal("Tombstone 后不应再有新流")
		}
	}
	t.Run("after_welcome", func(t *testing.T) { runTombstone(t, true) })
	t.Run("before_welcome", func(t *testing.T) { runTombstone(t, false) })
}

func TestSessionFirstMessageMustBeWelcome(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	var n atomic.Int32
	var fc *fakeControl
	fc = newFakeControl(func(st *fakeStream) error {
		fc.streamCh <- st
		if n.Add(1) == 1 {
			_ = st.SendTrafficAck("x")
			return errors.New("非 Welcome 首条")
		}
		_ = st.SendWelcome(1, 1)
		<-st.done
		return nil
	})
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	waitStreams(t, fc, 2, 5*time.Second)
	if s.Connected() {
		t.Fatal("首条非 Welcome 时不应 Connected")
	}
	nextEvent(t, s, EventWelcome)
	fc.takeStream(t, 5*time.Second).end()
}

func TestSessionSendQueueOverflowDropsStream(t *testing.T) {
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	block := make(chan struct{})
	var fc *fakeControl
	fc = newFakeControl(func(st *fakeStream) error {
		fc.streamCh <- st
		_ = st.SendWelcome(1, 1)
		<-block
		return nil
	})
	s, _, _ := newSessionFixture(t, pki, id, fc, func(o *SessionOptions) { o.QueueSize = 1 })
	nextEvent(t, s, EventWelcome)
	big := &nodev1.Heartbeat{Metrics: &nodev1.HostMetrics{MemTotalBytes: 1 << 20}}
	var dropped bool
	for i := 0; i < 10000 && !dropped; i++ {
		if !s.SendHeartbeat(big) {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("应因队列满而 SendHeartbeat 失败")
	}
	nextEvent(t, s, EventDisconnected)
	close(block)
	waitStreams(t, fc, 2, 5*time.Second)
}

func TestSessionRenewsCertificate(t *testing.T) {
	pki := newTestPKI(t)
	sid := newServerID()
	id := newTestIdentity(t, pki, sid, time.Now().Add(5*24*time.Hour))
	var fc *fakeControl
	fc = newFakeControl(func(st *fakeStream) error {
		fc.streamCh <- st
		_ = st.SendWelcome(1, 1)
		<-st.done
		return nil
	})
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)
	st := fc.takeStream(t, 5*time.Second)
	nextEvent(t, s, EventWelcome)
	renew := st.Expect(t, "Renew").(*nodev1.RenewRequest)
	cert := pki.signCSR(t, renew.GetCsrDer(), sid, time.Now().Add(30*24*time.Hour))
	_ = st.SendCertificate(cert, pki.caPEM)
	_ = st.SendReconnect(0, 0)
	st.end()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if id.NotAfter().After(time.Now().Add(20 * 24 * time.Hour)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id.NotAfter().Before(time.Now().Add(20 * 24 * time.Hour)) {
		t.Fatal("NotAfter 未更新")
	}
	reloaded, err := LoadIdentity(id.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NotAfter().Before(time.Now().Add(20 * 24 * time.Hour)) {
		t.Fatal("磁盘证书未更新")
	}
	waitStreams(t, fc, 2, 5*time.Second)
	serials := fc.leafSerials()
	if len(serials) < 2 || serials[0] == serials[1] {
		t.Fatalf("续期后第二条流证书序列号应不同: %v", serials)
	}

	pki2 := newTestPKI(t)
	id2 := newTestIdentity(t, pki2, newServerID(), time.Now().Add(30*24*time.Hour))
	fc2 := newHandoffControl()
	s2, _, _ := newSessionFixture(t, pki2, id2, fc2, nil)
	st2 := fc2.takeStream(t, 5*time.Second)
	_ = st2.SendWelcome(1, 1)
	nextEvent(t, s2, EventWelcome)
	// 要续期的话 RenewRequest 会紧跟在 Welcome 之后、排在这条心跳前面。
	if !s2.SendHeartbeat(&nodev1.Heartbeat{}) {
		t.Fatal("SendHeartbeat 应成功")
	}
	st2.Expect(t, "Heartbeat")
	st2.end()
}
