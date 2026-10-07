package node

import (
	"context"
	"testing"
	"time"
)

// readyTableCore 是入站就绪探针恒为通过的用户表内核，health_passed 才报得出去。
type readyTableCore struct{ *userTableCore }

func (readyTableCore) InboundReady(string) error { return nil }

// 让刚应用的发布越过 5 秒稳定窗口，下一轮就能报 health_passed。
func pastStabilityWindow(n *Node) { n.appliedAt = time.Now().Add(-2 * effectiveHealthStabilityWindow) }

func (p *fakeSignedPanel) counters() (fetches, unchanged, keyChecks int, applied string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effectiveFetches, p.unchangedReplies, p.keyChecks, p.lastApplied
}

// switched / health_passed 被面板收下之后不再每轮重报（老面板总回全量时也一样）。
func TestSignedPhasesReportedOnceThenQuiet(t *testing.T) {
	kernel := readyTableCore{newUserTableCore()}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()

	n.syncOnce(ctx)
	pastStabilityWindow(n)
	for i := 0; i < 4; i++ {
		n.syncOnce(ctx)
	}
	phases := fake.phases(releaseGood)
	if countPhase(phases, "switched") != 1 || countPhase(phases, "health_passed") != 1 || len(phases) != 2 {
		t.Fatalf("settled phases were re-reported: %v", phases)
	}
}

// 回执没送到（503）就留给下一轮补报，送到之后停。
func TestSignedPhaseRetriedUntilDelivered(t *testing.T) {
	kernel := readyTableCore{newUserTableCore()}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()
	fake.mu.Lock()
	fake.failReports = 1
	fake.mu.Unlock()

	n.syncOnce(ctx) // 应用成功，switched 撞上 503
	if got := fake.phases(releaseGood); len(got) != 0 {
		t.Fatalf("unexpected delivered reports: %v", got)
	}
	n.syncOnce(ctx) // 补报 switched
	pastStabilityWindow(n)
	n.syncOnce(ctx) // 报 health_passed
	n.syncOnce(ctx)
	phases := fake.phases(releaseGood)
	if countPhase(phases, "switched") != 1 || countPhase(phases, "health_passed") != 1 {
		t.Fatalf("phases after a lost switched receipt: %v", phases)
	}
}

// 新面板：节点带上已应用版本，面板回 204，这一轮不验签、不重装、不上报；发布一变
// 就照常拿到全量并应用。
func TestSignedUnchangedReleaseShortCircuits(t *testing.T) {
	kernel := readyTableCore{newUserTableCore()}
	n, fake := newSignedFixture(t, kernel)
	fake.mu.Lock()
	fake.honorApplied = true
	fake.mu.Unlock()
	ctx := context.Background()

	n.syncOnce(ctx)
	if _, _, _, applied := fake.counters(); applied != "" {
		t.Fatalf("a node that has nothing applied must not claim a release: %q", applied)
	}
	pastStabilityWindow(n)
	n.syncOnce(ctx) // 204，顺带补报 health_passed
	n.syncOnce(ctx) // 204，没有任何上报
	fetches, unchanged, _, applied := fake.counters()
	if fetches != 3 || unchanged != 2 || applied != releaseGood+"/1" {
		t.Fatalf("fetches=%d unchanged=%d applied=%q", fetches, unchanged, applied)
	}
	if phases := fake.phases(releaseGood); len(phases) != 2 {
		t.Fatalf("reports on unchanged rounds: %v", phases)
	}

	fake.publish(releaseNext, 2, 18081)
	n.syncOnce(ctx)
	if kernel.port != 18081 || n.appliedReleaseID != releaseNext {
		t.Fatalf("new release not applied after 204 rounds: port=%d release=%s", kernel.port, n.appliedReleaseID)
	}
}

// 节点已停（没装上任何发布）时不报已应用版本：必须拿全量重试。
func TestSignedStoppedNodeAlwaysFetchesFullConfig(t *testing.T) {
	kernel := &rejectingCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18080: true}}
	n, fake := newSignedFixture(t, kernel)
	fake.mu.Lock()
	fake.honorApplied = true
	fake.mu.Unlock()
	for i := 0; i < 2; i++ {
		n.syncOnce(context.Background())
	}
	if _, unchanged, _, applied := fake.counters(); unchanged != 0 || applied != "" {
		t.Fatalf("stopped node short-circuited: unchanged=%d applied=%q", unchanged, applied)
	}
}

// 换钥检查十分钟一次，不再每轮都问；验签失败时立刻补问一次。
func TestSignedConfigKeyCheckedLazily(t *testing.T) {
	kernel := readyTableCore{newUserTableCore()}
	n, fake := newSignedFixture(t, kernel)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		n.syncOnce(ctx)
	}
	if _, _, checks, _ := fake.counters(); checks != 1 {
		t.Fatalf("config signing key checked %d times in 5 rounds, want 1", checks)
	}

	// 面板换了钥匙、节点还没拿到过渡证明：新发布验不过，立刻补问一次。
	fake.mu.Lock()
	fake.keyID = "AAAAAAAAAAA"
	fake.mu.Unlock()
	fake.publish(releaseNext, 2, 18081)
	n.syncOnce(ctx)
	if _, _, checks, _ := fake.counters(); checks != 2 {
		t.Fatalf("verification failure did not force a key check: %d checks", checks)
	}
	if got := countPhase(fake.phases(releaseNext), "failed"); got != 1 {
		t.Fatalf("unverifiable release reported failed %d times", got)
	}
}

// 兼容：老面板不认已应用版本，总回 200 全量，节点按「已应用」处理，不重装。
func TestSignedOldPanelFullRepliesStayApplied(t *testing.T) {
	kernel := &rejectingCore{userTableCore: newUserTableCore()}
	n, _ := newSignedFixture(t, kernel)
	ctx := context.Background()
	n.syncOnce(ctx)
	before := kernel.addInbound
	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	if kernel.addInbound != before {
		t.Fatalf("full replies for the applied release rebuilt the inbound %d times", kernel.addInbound-before)
	}
	if n.appliedRelease() == nil || n.appliedRelease().ReleaseID != releaseGood {
		t.Fatalf("applied release = %+v", n.appliedRelease())
	}
}
