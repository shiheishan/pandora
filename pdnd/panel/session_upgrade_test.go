package panel

import (
	"bytes"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

func TestSessionHelloBuildSerialAndLastUpgrade(t *testing.T) {
	pki := newTestPKI(t)
	sid := newServerID()
	id := newTestIdentity(t, pki, sid, time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, func(o *SessionOptions) {
		o.BuildSerial = 42
		o.LastUpgrade = func() *nodev1.UpgradeReport {
			return &nodev1.UpgradeReport{
				JobId:      "j1",
				Phase:      nodev1.UpgradePhase_UPGRADE_PHASE_ROLLED_BACK,
				ReasonCode: "confirm_timeout",
			}
		}
	})

	st := fc.takeStream(t, 5*time.Second)
	if err := st.SendWelcome(60, 30); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, s, EventWelcome)
	if ev.HelloUpgrade == nil || ev.HelloUpgrade.GetJobId() != "j1" {
		t.Fatalf("EventWelcome.HelloUpgrade 应带 j1: %v", ev.HelloUpgrade)
	}

	h := fc.firstHello(0)
	if h.GetBuildSerial() != 42 {
		t.Fatalf("Hello.BuildSerial: got %d, want 42", h.GetBuildSerial())
	}
	lu := h.GetLastUpgrade()
	if lu == nil || lu.GetJobId() != "j1" || lu.GetPhase() != nodev1.UpgradePhase_UPGRADE_PHASE_ROLLED_BACK || lu.GetReasonCode() != "confirm_timeout" {
		t.Fatalf("Hello.LastUpgrade 内容不一致: %v", lu)
	}
	st.end()
}

func TestSessionHelloWithoutLastUpgrade(t *testing.T) {
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
	if ev.HelloUpgrade != nil {
		t.Fatalf("EventWelcome.HelloUpgrade 应为 nil, got %v", ev.HelloUpgrade)
	}

	h := fc.firstHello(0)
	if h.GetLastUpgrade() != nil {
		t.Fatalf("Hello.LastUpgrade 应为 nil, got %v", h.GetLastUpgrade())
	}
	st.end()
}

func TestSessionCommandUpgrade(t *testing.T) {
	pki := newTestPKI(t)
	sid := newServerID()
	id := newTestIdentity(t, pki, sid, time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)

	st := fc.takeStream(t, 5*time.Second)
	if err := st.SendWelcome(60, 30); err != nil {
		t.Fatal(err)
	}
	_ = nextEvent(t, s, EventWelcome)

	want := &nodev1.Upgrade{
		JobId:            "j2",
		Manifest:         []byte("m"),
		Signature:        []byte("s"),
		BinaryUrl:        "https://example.com/x",
		ConfirmWithinSec: 180,
	}
	if err := st.Send(&nodev1.SessionResponse{
		Body: &nodev1.SessionResponse_Command{
			Command: &nodev1.Command{
				Kind: &nodev1.Command_Upgrade{Upgrade: want},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	ev := nextEvent(t, s, EventUpgrade)
	if ev.Upgrade == nil {
		t.Fatal("ev.Upgrade 应非空")
	}
	if ev.Upgrade.GetJobId() != want.JobId ||
		!bytes.Equal(ev.Upgrade.GetManifest(), want.Manifest) ||
		!bytes.Equal(ev.Upgrade.GetSignature(), want.Signature) ||
		ev.Upgrade.GetBinaryUrl() != want.BinaryUrl ||
		ev.Upgrade.GetConfirmWithinSec() != want.ConfirmWithinSec {
		t.Fatalf("ev.Upgrade 不一致: got %v, want %v", ev.Upgrade, want)
	}

	// 确认流未因 EventUpgrade 结束：之后发心跳依然成功
	if !s.SendHeartbeat(&nodev1.Heartbeat{Metrics: &nodev1.HostMetrics{CpuBp: 123}}) {
		t.Fatal("流结束或心跳发送失败")
	}
	req, err := st.Recv()
	if err != nil {
		t.Fatalf("st.Recv(): %v", err)
	}
	if hb := req.GetHeartbeat(); hb == nil || hb.GetMetrics().GetCpuBp() != 123 {
		t.Fatalf("假面板收到消息不符合心跳: %v", req)
	}
	st.end()
}

func TestSessionSendUpgradeReport(t *testing.T) {
	pki := newTestPKI(t)
	sid := newServerID()
	id := newTestIdentity(t, pki, sid, time.Now().Add(30*24*time.Hour))
	fc := newHandoffControl()
	s, _, _ := newSessionFixture(t, pki, id, fc, nil)

	rep := &nodev1.UpgradeReport{
		JobId:      "j3",
		Phase:      nodev1.UpgradePhase_UPGRADE_PHASE_STAGED,
		ReasonCode: "pre_check_ok",
	}

	// Welcome 之前发应返回 false
	if s.SendUpgradeReport(rep) {
		t.Fatal("Welcome 前 SendUpgradeReport 应返回 false")
	}

	st := fc.takeStream(t, 5*time.Second)
	if err := st.SendWelcome(60, 30); err != nil {
		t.Fatal(err)
	}
	_ = nextEvent(t, s, EventWelcome)

	// Welcome 之后发应返回 true
	if !s.SendUpgradeReport(rep) {
		t.Fatal("Welcome 后 SendUpgradeReport 应返回 true")
	}

	req, err := st.Recv()
	if err != nil {
		t.Fatalf("st.Recv(): %v", err)
	}
	got := req.GetUpgradeReport()
	if got == nil {
		t.Fatalf("假面板应收到 upgrade_report, 实际收到: %v", req)
	}
	if got.GetJobId() != rep.JobId || got.GetPhase() != rep.Phase || got.GetReasonCode() != rep.ReasonCode {
		t.Fatalf("UpgradeReport 内容不一致: got %v, want %v", got, rep)
	}
	st.end()
}
