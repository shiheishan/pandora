package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	nodepanel "github.com/aegispanel/nodeagent/panel"
)

// 夹具是按面板契约独立实现的；这里用节点自己的接入与会话客户端对打，保证节点信得过它的证书、
// 它认得节点的客户端证书与消息。任何一边的协议改了而另一边没跟上，CI 在这里变红，
// 而不是等到有人在验收机上跑脚本。

const testToken = "acceptance-token"

func startPanel(t *testing.T) (*fakePanel, string) {
	t.Helper()
	p, err := newFakePanel(options{
		serverName: "127.0.0.1", nodePort: 18443, token: testToken,
		statePath: filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.serve(lis) }()
	t.Cleanup(func() { _ = lis.Close() })
	return p, lis.Addr().String()
}

func enroll(t *testing.T, p *fakePanel, addr string) (*nodepanel.Identity, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return nodepanel.Enroll(ctx, nodepanel.EnrollOptions{
		Panel: addr, CAFingerprint: p.caFingerprint(), Token: testToken,
		Dir: t.TempDir(), AgentVersion: "test", Timeout: 5 * time.Second,
	})
}

// startSession 接入并开会话，测试结束时取消并等 Run 返回。
func startSession(t *testing.T, addr string, id *nodepanel.Identity, applied nodepanel.AppliedFunc) *nodepanel.Session {
	t.Helper()
	sess, err := nodepanel.NewSession(nodepanel.SessionOptions{
		Address: addr, Identity: id, AgentVersion: "test", Applied: applied,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = sess.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sess
}

func nextEvent(t *testing.T, sess *nodepanel.Session, kind nodepanel.EventKind) nodepanel.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == kind {
				return ev
			}
			if ev.Kind != nodepanel.EventDisconnected {
				t.Fatalf("等 %v，先来了 %v", kind, ev.Kind)
			}
		case <-timeout:
			t.Fatalf("5 秒内没等到事件 %v", kind)
		}
	}
}

// waitState 轮询状态直到 ok，至多 5 秒。
func waitState(t *testing.T, p *fakePanel, what string, ok func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := p.snapshot()
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			raw, _ := json.Marshal(s)
			t.Fatalf("5 秒内没看到%s：%s", what, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEnrollAndSessionAgainstNodeClient(t *testing.T) {
	p, addr := startPanel(t)
	id, err := enroll(t, p, addr)
	if err != nil {
		t.Fatal(err)
	}
	if id.ServerID() != p.serverID {
		t.Fatalf("server_id = %s，期望 %s", id.ServerID(), p.serverID)
	}
	sess := startSession(t, addr, id, func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) { return nil, nil })

	nextEvent(t, sess, nodepanel.EventWelcome)
	cfg := nextEvent(t, sess, nodepanel.EventConfig).Config
	if len(cfg.GetNodes()) != 1 {
		t.Fatalf("配置里应有一个节点：%v", cfg)
	}
	nc := cfg.GetNodes()[0]
	var doc map[string]any
	if err := json.Unmarshal(nc.GetConfigJson(), &doc); err != nil || doc["server_port"] != float64(18443) || nc.GetNodeType() != "socks" {
		t.Fatalf("节点配置不对：%s %s %v", nc.GetNodeType(), nc.GetConfigJson(), err)
	}
	sum := sha256.Sum256(nc.GetConfigJson())
	if nc.GetContentSha256() != hex.EncodeToString(sum[:]) {
		t.Fatal("content_sha256 与 config_json 对不上")
	}
	users := nextEvent(t, sess, nodepanel.EventUsersFull).UsersFull
	if len(users.Users) != 1 || users.Version != usersVersion {
		t.Fatalf("名单不对：%+v", users)
	}

	if !sess.SendHeartbeat(&nodev1.Heartbeat{Metrics: &nodev1.HostMetrics{CpuBp: 100},
		Nodes: []*nodev1.NodeRuntime{{NodeId: nc.GetNodeId(), Status: nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING}}}) {
		t.Fatal("心跳没发出去")
	}
	if !sess.SendConfigAck(&nodev1.ConfigAck{NodeId: nc.GetNodeId(), ContentSha256: nc.GetContentSha256(),
		Phase: nodev1.ConfigPhase_CONFIG_PHASE_HEALTH_PASSED}) {
		t.Fatal("配置回执没发出去")
	}
	if !sess.SendUsersAck(&nodev1.UsersAck{NodeId: nc.GetNodeId(), Version: usersVersion, Ok: true}) {
		t.Fatal("名单回执没发出去")
	}
	if !sess.SendTraffic(&nodev1.TrafficReport{ReportId: "r1",
		Entries: []*nodev1.TrafficEntry{{NodeId: nc.GetNodeId(), Uid: 1, Upload: 1, Download: 2}}}) {
		t.Fatal("流量没发出去")
	}
	if ack := nextEvent(t, sess, nodepanel.EventTrafficAck); ack.ReportID != "r1" {
		t.Fatalf("TrafficAck report_id = %q", ack.ReportID)
	}
	s := waitState(t, p, "心跳、回执与流量", func(s State) bool {
		return s.HeartbeatsWithMetrics >= 1 && s.UsersAcksOK >= 1 && s.TrafficReports >= 1 &&
			slices.Contains(s.ConfigPhases, "CONFIG_PHASE_HEALTH_PASSED")
	})
	if s.Enrolls != 1 || s.Hellos < 1 || s.BadClientCerts != 0 || s.MissingRequestIDs != 0 {
		t.Fatalf("状态不对：%+v", s)
	}
	if !slices.Equal(s.LastRuntime, []string{"RUNTIME_STATUS_RUNNING"}) {
		t.Fatalf("last_runtime = %v", s.LastRuntime)
	}
}

// 同一令牌换一个身份目录（新的 request_id）再接入：令牌已用过，必须被拒。
func TestEnrollRejectsReusedToken(t *testing.T) {
	p, addr := startPanel(t)
	if _, err := enroll(t, p, addr); err != nil {
		t.Fatal(err)
	}
	if _, err := enroll(t, p, addr); err == nil {
		t.Fatal("令牌重用没被拒")
	}
	if s := p.snapshot(); s.Enrolls != 1 || s.EnrollRejections != 1 {
		t.Fatalf("状态不对：%+v", s)
	}
}

// Hello 报的已应用版本与面板当前一致时，面板不再重发配置与名单。
func TestHelloWithAppliedVersionsSkipsResend(t *testing.T) {
	p, addr := startPanel(t)
	id, err := enroll(t, p, addr)
	if err != nil {
		t.Fatal(err)
	}
	sess := startSession(t, addr, id, func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) {
		return []*nodev1.AppliedConfig{{NodeId: p.nodeID, ContentSha256: p.configSHA}},
			[]*nodev1.AppliedUsers{{NodeId: p.nodeID, Version: usersVersion}}
	})
	nextEvent(t, sess, nodepanel.EventWelcome)
	select {
	case ev := <-sess.Events():
		t.Fatalf("Welcome 之后不该再有事件，来了 %v", ev.Kind)
	case <-time.After(300 * time.Millisecond):
	}
	if s := p.snapshot(); s.HellosWithAppliedConfig != 1 {
		t.Fatalf("hellos_with_applied_config = %d", s.HellosWithAppliedConfig)
	}
}

// 在线升级往返：节点在 Hello 里声明 upgrade_v1 并自报 build_serial，面板在 Welcome 里回 upgrade_v1、
// 下发一次 Upgrade（原样带清单、签名与地址），节点回 UpgradeReport，面板记下 phase 与原因码；重连不重发。
func TestUpgradeRoundTrip(t *testing.T) {
	job := &upgradeJob{manifest: []byte(`{"schema":1}`), signature: []byte(`{"key_id":"x"}`), url: "https://dl.example.test/pandora-native"}
	p, err := newFakePanel(options{
		serverName: "127.0.0.1", nodePort: 18443, token: testToken,
		statePath: filepath.Join(t.TempDir(), "state.json"), upgrade: job,
	})
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.serve(lis) }()
	t.Cleanup(func() { _ = lis.Close() })
	addr := lis.Addr().String()

	id, err := enroll(t, p, addr)
	if err != nil {
		t.Fatal(err)
	}
	applied := func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) {
		return []*nodev1.AppliedConfig{{NodeId: p.nodeID, ContentSha256: p.configSHA}},
			[]*nodev1.AppliedUsers{{NodeId: p.nodeID, Version: usersVersion}}
	}
	sess, err := nodepanel.NewSession(nodepanel.SessionOptions{
		Address: addr, Identity: id, AgentVersion: "test", Applied: applied,
		BuildSerial: 7, Features: []string{nodepanel.FeatureUsersDelta, nodepanel.FeatureUpgrade},
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = sess.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	w := nextEvent(t, sess, nodepanel.EventWelcome).Welcome
	if !slices.Contains(w.GetFeatures(), nodepanel.FeatureUpgrade) {
		t.Fatalf("Welcome.features = %v，应含 upgrade_v1", w.GetFeatures())
	}
	u := nextEvent(t, sess, nodepanel.EventUpgrade).Upgrade
	if u.GetJobId() != p.upgradeID || string(u.GetManifest()) != string(job.manifest) ||
		string(u.GetSignature()) != string(job.signature) || u.GetBinaryUrl() != job.url || u.GetConfirmWithinSec() == 0 {
		t.Fatalf("Upgrade 指令不对：%+v", u)
	}
	if !sess.SendUpgradeReport(&nodev1.UpgradeReport{JobId: u.GetJobId(),
		Phase: nodev1.UpgradePhase_UPGRADE_PHASE_FAILED, ReasonCode: "signature_invalid"}) {
		t.Fatal("升级回报没发出去")
	}
	s := waitState(t, p, "升级回报", func(s State) bool { return len(s.UpgradeReports) >= 1 })
	if !slices.Equal(s.UpgradeReports, []string{"UPGRADE_PHASE_FAILED:signature_invalid"}) || s.HelloBuildSerial != 7 || s.UpgradesSent != 1 {
		t.Fatalf("状态不对：%+v", s)
	}
	if p.takeUpgrade() {
		t.Fatal("同一进程不应再发第二次 Upgrade")
	}
}

// snapshot 返回状态的一份拷贝（测试用）。
func (p *fakePanel) snapshot() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state
	s.ConfigPhases = append([]string(nil), s.ConfigPhases...)
	s.LastRuntime = append([]string(nil), s.LastRuntime...)
	s.UpgradeReports = append([]string(nil), s.UpgradeReports...)
	return s
}
