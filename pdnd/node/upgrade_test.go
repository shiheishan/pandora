package node

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/panel"
	"github.com/aegispanel/nodeagent/upgrade"
)

type fakeUpgrader struct {
	mu sync.Mutex

	stageErr error
	block    chan struct{} // 收到后放行阻塞中的 Stage

	reqs     []upgrade.StageRequest
	healthyN int
	removed  []*upgrade.Result
	result   *upgrade.Result
	markOK   bool
}

func (f *fakeUpgrader) Stage(ctx context.Context, req upgrade.StageRequest) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	block := f.block
	err := f.stageErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeUpgrader) MarkHealthy() (bool, error) {
	f.mu.Lock()
	f.healthyN++
	ok := f.markOK
	f.mu.Unlock()
	return ok, nil
}

func (f *fakeUpgrader) Result() (*upgrade.Result, error) {
	f.mu.Lock()
	r := f.result
	f.mu.Unlock()
	return r, nil
}

func (f *fakeUpgrader) RemoveResult(r *upgrade.Result) error {
	f.mu.Lock()
	f.removed = append(f.removed, r)
	f.mu.Unlock()
	return nil
}

func (f *fakeUpgrader) lastReq() upgrade.StageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return upgrade.StageRequest{}
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeUpgrader) stageCalls() int {
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	return n
}

func welcomeUpgradeV1(ts *testServer, hello *nodev1.UpgradeReport) {
	ts.handleEvent(panel.Event{
		Kind:         panel.EventWelcome,
		Welcome:      &nodev1.Welcome{SessionId: "s", Features: []string{upgrade.Feature}},
		HelloUpgrade: hello,
	})
}

func sampleUpgrade(jobID string) *nodev1.Upgrade {
	return &nodev1.Upgrade{
		JobId:            jobID,
		Manifest:         []byte("manifest-bytes"),
		Signature:        []byte("sig-bytes"),
		BinaryUrl:        "https://cdn.example/bin",
		ConfirmWithinSec: 90,
	}
}

func waitUpgradeDone(t *testing.T, ts *testServer) upgradeOutcome {
	t.Helper()
	select {
	case out := <-ts.upgradeDone:
		return out
	case <-time.After(2 * time.Second):
		t.Fatal("等待升级暂存超时")
		return upgradeOutcome{}
	}
}

func TestUpgradeStagedOnSuccess(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	fake := &fakeUpgrader{markOK: true}
	ts.upgrader = fake

	welcomeUpgradeV1(ts, nil)
	u := sampleUpgrade("job-ok")
	ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: u})

	out := waitUpgradeDone(t, ts)
	ts.onUpgradeDone(out)

	req := fake.lastReq()
	if req.JobID != u.GetJobId() || !bytes.Equal(req.Manifest, u.GetManifest()) ||
		!bytes.Equal(req.Signature, u.GetSignature()) || req.BinaryURL != u.GetBinaryUrl() ||
		req.ConfirmWithinSec != u.GetConfirmWithinSec() {
		t.Fatalf("Stage 请求与指令不一致: %+v", req)
	}

	reps := ts.link.upgrades()
	if len(reps) != 1 || reps[0].GetJobId() != "job-ok" || reps[0].GetPhase() != nodev1.UpgradePhase_UPGRADE_PHASE_STAGED {
		t.Fatalf("回报 = %v，期望 STAGED job-ok", reps)
	}
}

func TestUpgradeFailedReasons(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"sha256", upgrade.Errorf(upgrade.ReasonSHA256Mismatch, "x"), upgrade.ReasonSHA256Mismatch},
		{"generic", context.DeadlineExceeded, upgrade.ReasonDownloadFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, newFakeKernel(), "")
			fake := &fakeUpgrader{stageErr: tc.err}
			ts.upgrader = fake
			welcomeUpgradeV1(ts, nil)
			ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("job-fail")})
			out := waitUpgradeDone(t, ts)
			ts.onUpgradeDone(out)
			reps := ts.link.upgrades()
			if len(reps) != 1 || reps[0].GetPhase() != nodev1.UpgradePhase_UPGRADE_PHASE_FAILED || reps[0].GetReasonCode() != tc.code {
				t.Fatalf("回报 = %v，期望 FAILED %s", reps, tc.code)
			}
		})
	}
}

func TestUpgradeIgnoredWithoutNegotiationOrUpgrader(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	fake := &fakeUpgrader{}
	ts.upgrader = fake
	ts.handleEvent(panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{SessionId: "s"}})
	ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("j1")})
	if fake.stageCalls() != 0 || len(ts.link.upgrades()) != 0 {
		t.Fatal("未协商时不应暂存或上报")
	}

	ts2 := newTestServer(t, newFakeKernel(), "")
	welcomeUpgradeV1(ts2, nil)
	ts2.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("j2")})
	if len(ts2.link.upgrades()) != 0 {
		t.Fatal("Upgrader 为 nil 时不应上报")
	}
}

func TestUpgradeBusyWhileStaging(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	block := make(chan struct{})
	fake := &fakeUpgrader{block: block}
	ts.upgrader = fake
	welcomeUpgradeV1(ts, nil)

	ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("job-first")})
	ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("job-second")})

	reps := ts.link.upgrades()
	if len(reps) != 1 || reps[0].GetJobId() != "job-second" || reps[0].GetReasonCode() != upgrade.ReasonBusy {
		t.Fatalf("第二条应 busy，回报 = %v", reps)
	}
	if fake.stageCalls() != 1 {
		t.Fatalf("Stage 只应调用一次，实际 %d", fake.stageCalls())
	}

	close(block)
	out := waitUpgradeDone(t, ts)
	ts.onUpgradeDone(out)
	reps = ts.link.upgrades()
	if len(reps) != 2 || reps[1].GetPhase() != nodev1.UpgradePhase_UPGRADE_PHASE_STAGED {
		t.Fatalf("第一条完成后应 STAGED，回报 = %v", reps)
	}
}

func TestUpgradePendingReportFlushedOnWelcome(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	fake := &fakeUpgrader{}
	ts.upgrader = fake
	welcomeUpgradeV1(ts, nil)

	ts.link.setDown(true)
	ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("job-pend")})
	out := waitUpgradeDone(t, ts)
	ts.onUpgradeDone(out)
	if ts.pendingUpgrade == nil {
		t.Fatal("发送失败时应保留 pendingUpgrade")
	}
	if len(ts.link.upgrades()) != 0 {
		t.Fatal("link down 时不应记入 fakeLink")
	}

	ts.link.setDown(false)
	welcomeUpgradeV1(ts, nil)
	reps := ts.link.upgrades()
	if len(reps) != 1 || reps[0].GetJobId() != "job-pend" {
		t.Fatalf("重连 Welcome 应补发，回报 = %v", reps)
	}
}

func TestUpgradeHousekeepingReportsResult(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	fake := &fakeUpgrader{
		result: &upgrade.Result{
			JobID: "job-rb", Phase: upgrade.PhaseRolledBack, ReasonCode: upgrade.ReasonConfirmTimeout, Detail: "超时",
		},
	}
	ts.upgrader = fake
	welcomeUpgradeV1(ts, nil)
	ts.housekeeping()
	reps := ts.link.upgrades()
	if len(reps) != 1 || reps[0].GetPhase() != nodev1.UpgradePhase_UPGRADE_PHASE_ROLLED_BACK {
		t.Fatalf("内务应上报 ROLLED_BACK，回报 = %v", reps)
	}
	if len(fake.removed) != 1 {
		t.Fatalf("应 RemoveResult 一次，实际 %d", len(fake.removed))
	}

	ts.disconnect()
	ts.link.upgradeReports = nil
	ts.housekeeping()
	if len(ts.link.upgrades()) != 0 {
		t.Fatal("断开后内务不应上报升级结局")
	}
}

func TestWelcomeUpgradeMarkHealthyAndRemoveResult(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	res := &upgrade.Result{JobID: "job-h", Phase: upgrade.PhaseFailed, ReasonCode: upgrade.ReasonApplyFailed, Detail: "d"}
	fake := &fakeUpgrader{result: res, markOK: true}
	ts.upgrader = fake

	hello := &nodev1.UpgradeReport{
		JobId: res.JobID, Phase: nodev1.UpgradePhase_UPGRADE_PHASE_FAILED,
		ReasonCode: res.ReasonCode, Detail: res.Detail,
	}
	welcomeUpgradeV1(ts, hello)
	if fake.healthyN != 1 {
		t.Fatalf("MarkHealthy 应调用 1 次，实际 %d", fake.healthyN)
	}
	if len(fake.removed) != 1 {
		t.Fatalf("Hello 与结果一致时应 RemoveResult，实际 %d", len(fake.removed))
	}

	fake.removed = nil
	fake.result = res
	welcomeUpgradeV1(ts, &nodev1.UpgradeReport{JobId: "other-job", Phase: nodev1.UpgradePhase_UPGRADE_PHASE_FAILED})
	if len(fake.removed) != 0 {
		t.Fatal("job_id 不一致时不应删结果")
	}
}

func TestLastUpgrade(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	fake := &fakeUpgrader{
		result: &upgrade.Result{JobID: "j", Phase: upgrade.PhaseFailed, ReasonCode: upgrade.ReasonBusy, Detail: "忙"},
	}
	ts.upgrader = fake
	rep := ts.LastUpgrade()
	if rep == nil || rep.GetJobId() != "j" || rep.GetPhase() != nodev1.UpgradePhase_UPGRADE_PHASE_FAILED {
		t.Fatalf("LastUpgrade = %v", rep)
	}

	fake.result = &upgrade.Result{JobID: "j2", Phase: "STAGED"}
	if ts.LastUpgrade() != nil {
		t.Fatal("不认识的 phase 应返回 nil")
	}

	ts.upgrader = nil
	if ts.LastUpgrade() != nil {
		t.Fatal("无 Upgrader 应返回 nil")
	}
}

func TestUpgradeReportDetailTruncatedUTF8(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	long := strings.Repeat("测", 600) // 1800 字节
	fake := &fakeUpgrader{stageErr: upgrade.Errorf(upgrade.ReasonDownloadFailed, "%s", long)}
	ts.upgrader = fake
	welcomeUpgradeV1(ts, nil)
	ts.handleEvent(panel.Event{Kind: panel.EventUpgrade, Upgrade: sampleUpgrade("job-long")})
	out := waitUpgradeDone(t, ts)
	ts.onUpgradeDone(out)
	reps := ts.link.upgrades()
	if len(reps) != 1 {
		t.Fatal("应有回报")
	}
	d := reps[0].GetDetail()
	if len(d) > 1024 || !utf8.ValidString(d) {
		t.Fatalf("detail 长度=%d valid=%v", len(d), utf8.ValidString(d))
	}
}
