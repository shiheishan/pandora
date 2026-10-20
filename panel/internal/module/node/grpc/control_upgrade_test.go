package grpc

// 在线升级经 ControlService 的端到端用例：能力协商、Command.upgrade 下发、UpgradeReport 入账。

import (
	"slices"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

func TestSessionUpgradeRoundTrip(t *testing.T) {
	b := newFakeBackend()
	client, m := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	job := uuid.MustParse("0190a000-0000-7000-8000-0000000000aa")
	if err := stream.Send(helloMsg(&nodev1.Hello{
		AgentVersion: "v1",
		Features:     []string{service.FeatureUpgradeV1},
		BuildSerial:  5,
		Host:         &nodev1.HostInfo{Arch: "amd64"},
	})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	wel := recvResp(t, stream)
	if wel.GetWelcome() == nil {
		t.Fatal("期望 Welcome")
	}
	if !slices.Contains(wel.GetWelcome().GetFeatures(), service.FeatureUpgradeV1) {
		t.Fatalf("features=%v", wel.GetWelcome().GetFeatures())
	}
	waitFor(t, 2*time.Second, func() bool {
		info, ok := m.UpgradeInfo(testServerID)
		return ok && info.BuildSerial == 5 && info.Arch == "amd64"
	}, "UpgradeInfo")
	if !m.SendUpgrade(testServerID, service.UpgradeCommand{
		JobID: job, Manifest: []byte("{}"), Signature: []byte("{}"),
		BinaryURL: "https://panel.example.com/pdnd/bin/pandora-native-linux-amd64", ConfirmWithinSec: 180,
	}) {
		t.Fatal("SendUpgrade 应成功")
	}
	var up *nodev1.Upgrade
	for i := 0; i < 10; i++ {
		resp := recvResp(t, stream)
		if u := resp.GetCommand().GetUpgrade(); u != nil {
			up = u
			break
		}
	}
	if up == nil {
		t.Fatal("未收到 Upgrade command")
	}
	if up.GetJobId() != job.String() {
		t.Fatalf("job_id=%q", up.GetJobId())
	}
	if up.GetBinaryUrl() != "https://panel.example.com/pdnd/bin/pandora-native-linux-amd64" || up.GetConfirmWithinSec() != 180 {
		t.Fatalf("upgrade=%+v", up)
	}
	if err := stream.Send(&nodev1.SessionRequest{
		Seq: 2,
		Body: &nodev1.SessionRequest_UpgradeReport{
			UpgradeReport: &nodev1.UpgradeReport{
				JobId: job.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED,
			},
		},
	}); err != nil {
		t.Fatalf("Send upgrade report: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		b.mu.Lock()
		n := len(b.upgradeReports)
		var ok bool
		if n == 1 {
			r := b.upgradeReports[0]
			ok = r.JobID == job && r.Phase == service.UpgradePhaseStaged
		}
		b.mu.Unlock()
		return ok
	}, "升级回报入账")
}

func TestSessionUpgradeReportInvalidIgnored(t *testing.T) {
	b := newFakeBackend()
	client, _ := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{
		Features: []string{service.FeatureUpgradeV1},
	})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream)
	goodJob := uuid.New()
	if err := stream.Send(&nodev1.SessionRequest{
		Seq: 2,
		Body: &nodev1.SessionRequest_UpgradeReport{
			UpgradeReport: &nodev1.UpgradeReport{JobId: "bad", Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED},
		},
	}); err != nil {
		t.Fatalf("Send bad: %v", err)
	}
	if err := stream.Send(&nodev1.SessionRequest{
		Seq: 3,
		Body: &nodev1.SessionRequest_UpgradeReport{
			UpgradeReport: &nodev1.UpgradeReport{
				JobId: goodJob.String(), Phase: nodev1.UpgradePhase_UPGRADE_PHASE_FAILED, ReasonCode: "download_failed",
			},
		},
	}); err != nil {
		t.Fatalf("Send good: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.upgradeReports) != 1 {
			return false
		}
		r := b.upgradeReports[0]
		return r.JobID == goodJob && r.ReasonCode == "download_failed"
	}, "仅合法回报入账")
}

func TestSessionUpgradeNotNegotiated(t *testing.T) {
	b := newFakeBackend()
	client, m := startControl(t, b, testSessionOpt, true)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if err := stream.Send(helloMsg(&nodev1.Hello{AgentVersion: "v1"})); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainWelcome(t, stream)
	job := uuid.New()
	if m.SendUpgrade(testServerID, service.UpgradeCommand{
		JobID: job, Manifest: []byte("{}"), Signature: []byte("{}"),
		BinaryURL: "https://panel.example.com/pdnd/bin/pandora-native-linux-amd64", ConfirmWithinSec: 180,
	}) {
		t.Fatal("未协商 upgrade_v1 时 SendUpgrade 应返回 false")
	}
}
