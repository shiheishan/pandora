package service

// 在线升级 service 的 PG 集成测试。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// fakeUpgradeSessions 实现 SessionControl，供升级下发与前置检查。
type fakeUpgradeSessions struct {
	mu     sync.Mutex
	info   map[uuid.UUID]SessionUpgradeInfo
	refuse map[uuid.UUID]bool
	sent   []struct {
		serverID uuid.UUID
		cmd      UpgradeCommand
	}
}

func newFakeUpgradeSessions() *fakeUpgradeSessions {
	return &fakeUpgradeSessions{
		info:   make(map[uuid.UUID]SessionUpgradeInfo),
		refuse: make(map[uuid.UUID]bool),
	}
}

func (f *fakeUpgradeSessions) Tombstone(serverID uuid.UUID, reason string) {}

func (f *fakeUpgradeSessions) UpgradeInfo(serverID uuid.UUID) (SessionUpgradeInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inf, ok := f.info[serverID]
	return inf, ok
}

func (f *fakeUpgradeSessions) SendUpgrade(serverID uuid.UUID, cmd UpgradeCommand) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse[serverID] {
		return false
	}
	if _, ok := f.info[serverID]; !ok {
		return false
	}
	f.sent = append(f.sent, struct {
		serverID uuid.UUID
		cmd      UpgradeCommand
	}{serverID, cmd})
	return true
}

func (f *fakeUpgradeSessions) setOnline(serverID uuid.UUID, inf SessionUpgradeInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info[serverID] = inf
}

func (f *fakeUpgradeSessions) setRefuse(serverID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse[serverID] = true
}

func (f *fakeUpgradeSessions) commands() []struct {
	serverID uuid.UUID
	cmd      UpgradeCommand
} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]struct {
		serverID uuid.UUID
		cmd      UpgradeCommand
	}{}, f.sent...)
}

func upgradeOnlineInfo() SessionUpgradeInfo {
	return SessionUpgradeInfo{UpgradeV1: true, BuildSerial: 1, Arch: "amd64"}
}

// upgradeDist 写入双架构发布包并装配分发；返回 amd64 摘要。
func upgradeDist(t *testing.T, svc *Service) string {
	t.Helper()
	dir := t.TempDir()
	amd64 := []byte("fake-amd64-" + t.Name())
	arm64 := []byte("fake-arm64-" + t.Name())
	sha := distSum(amd64)
	writeDist(t, dir, map[string][]byte{
		PdndArtifactAMD64: amd64,
		PdndArtifactARM64: arm64,
		PdndManifestFile:  twoArchManifest(amd64, arm64),
		PdndSignatureFile: validSig(),
	})
	svc.UsePdndDist(dir, "https://panel.example.test")
	return sha
}

func upgradeJobID(t *testing.T, e *invEnv, ctx context.Context, serverID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `SELECT id FROM server_upgrades WHERE server_id = ? ORDER BY created_at DESC LIMIT 1`, serverID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func upgradeOpenCount(t *testing.T, e *invEnv, ctx context.Context, serverID uuid.UUID) int {
	t.Helper()
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM server_upgrades WHERE server_id = ? AND status IN ('sent', 'staged')`, serverID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRequestUpgradesCreatesAndDispatches(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	sess := newFakeUpgradeSessions()
	e.svc.UseSessions(sess)
	amd64SHA := upgradeDist(t, e.svc)

	a := e.readyServer(t, ctx, "upg-a")
	b := e.readyServer(t, ctx, "upg-b")
	c := e.readyServer(t, ctx, "upg-c")
	sess.setOnline(a.ID, upgradeOnlineInfo())
	sess.setOnline(b.ID, upgradeOnlineInfo())
	sess.setOnline(c.ID, upgradeOnlineInfo())
	sess.setRefuse(c.ID)

	missing := uuid.New()
	batch, err := e.svc.RequestUpgrades(ctx, []uuid.UUID{a.ID, b.ID, missing, a.ID, c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if batch.TargetVersion != "v2026.10.21" {
		t.Fatalf("TargetVersion=%q", batch.TargetVersion)
	}
	if len(batch.Results) != 4 {
		t.Fatalf("Results len=%d", len(batch.Results))
	}
	wantOrder := []uuid.UUID{a.ID, b.ID, missing, c.ID}
	for i, id := range wantOrder {
		if batch.Results[i].ServerID != id {
			t.Fatalf("[%d] server=%v", i, batch.Results[i].ServerID)
		}
	}
	if !batch.Results[0].OK || batch.Results[0].Reason != "" {
		t.Fatalf("A: %+v", batch.Results[0])
	}
	if !batch.Results[1].OK {
		t.Fatalf("B: %+v", batch.Results[1])
	}
	if batch.Results[2].OK || batch.Results[2].Reason != UpgradeBlockNotFound {
		t.Fatalf("missing: %+v", batch.Results[2])
	}
	if batch.Results[3].OK || batch.Results[3].Reason != UpgradeBlockNotConnected {
		t.Fatalf("C: %+v", batch.Results[3])
	}

	assertUpgradeSentRow(t, e, ctx, a.ID, amd64SHA)
	assertUpgradeSentRow(t, e, ctx, b.ID, amd64SHA)

	var cStatus, cReason sql.NullString
	var cFinished sql.NullTime
	if err := e.row(ctx, `SELECT status, reason_code, finished_at FROM server_upgrades WHERE server_id = ?`, c.ID).
		Scan(&cStatus, &cReason, &cFinished); err != nil {
		t.Fatal(err)
	}
	if cStatus.String != UpgradeFailed || cReason.String != UpgradeBlockNotConnected || !cFinished.Valid {
		t.Fatalf("C 行: status=%s reason=%s finished=%v", cStatus.String, cReason.String, cFinished.Valid)
	}

	jobA := upgradeJobID(t, e, ctx, a.ID)
	cmds := sess.commands()
	var foundA bool
	for _, sc := range cmds {
		if sc.serverID != a.ID {
			continue
		}
		foundA = true
		if sc.cmd.JobID != jobA {
			t.Fatalf("JobID %v want %v", sc.cmd.JobID, jobA)
		}
		wantURL := "https://panel.example.test/pdnd/bin/" + PdndArtifactAMD64
		if sc.cmd.BinaryURL != wantURL {
			t.Fatalf("BinaryURL=%q", sc.cmd.BinaryURL)
		}
		if sc.cmd.ConfirmWithinSec != UpgradeConfirmWithinSec {
			t.Fatalf("ConfirmWithinSec=%d", sc.cmd.ConfirmWithinSec)
		}
		if len(sc.cmd.Manifest) == 0 || len(sc.cmd.Signature) == 0 {
			t.Fatal("Manifest/Signature 应非空")
		}
	}
	if !foundA {
		t.Fatal("未下发 A 的命令")
	}

	batch2, err := e.svc.RequestUpgrades(ctx, []uuid.UUID{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if batch2.Results[0].OK || batch2.Results[0].Reason != UpgradeBlockInProgress {
		t.Fatalf("二次 A: %+v", batch2.Results[0])
	}
	if upgradeOpenCount(t, e, ctx, a.ID) != 1 {
		t.Fatal("A 应仍只有一条未结束任务")
	}
}

func assertUpgradeSentRow(t *testing.T, e *invEnv, ctx context.Context, serverID uuid.UUID, sha string) {
	t.Helper()
	var status string
	var sentAt sql.NullTime
	var targetSHA string
	var createdAt, deadlineAt time.Time
	if err := e.row(ctx, `SELECT status, sent_at, target_sha256, created_at, deadline_at FROM server_upgrades WHERE server_id = ? AND status = 'sent'`, serverID).
		Scan(&status, &sentAt, &targetSHA, &createdAt, &deadlineAt); err != nil {
		t.Fatal(err)
	}
	if !sentAt.Valid || targetSHA != sha || !deadlineAt.After(createdAt) {
		t.Fatalf("server %s: sent=%v sha=%s deadline order", serverID, sentAt.Valid, targetSHA)
	}
}

func TestRequestUpgradesPreconditions(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	sess := newFakeUpgradeSessions()
	e.svc.UseSessions(sess)

	_, err := e.svc.RequestUpgrades(ctx, []uuid.UUID{uuid.New()})
	invWantCode(t, err, errs.Unavailable)

	upgradeDist(t, e.svc)

	a := e.readyServer(t, ctx, "pre-a")
	b := e.readyServer(t, ctx, "pre-b")
	sess.setOnline(b.ID, SessionUpgradeInfo{UpgradeV1: false, BuildSerial: 1, Arch: "amd64"})

	batch, err := e.svc.RequestUpgrades(ctx, []uuid.UUID{a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].OK || batch.Results[0].Reason != UpgradeBlockNotConnected {
		t.Fatalf("A 不在线: %+v", batch.Results[0])
	}
	var nA int
	if err := e.row(ctx, `SELECT count(*) FROM server_upgrades WHERE server_id = ?`, a.ID).Scan(&nA); err != nil {
		t.Fatal(err)
	}
	if nA != 0 {
		t.Fatal("A 不应有升级行")
	}

	batch, err = e.svc.RequestUpgrades(ctx, []uuid.UUID{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].OK || batch.Results[0].Reason != UpgradeBlockNoSupport {
		t.Fatalf("B: %+v", batch.Results[0])
	}

	_, err = e.svc.RequestUpgrades(ctx, nil)
	if ee := invWantCode(t, err, errs.ValidationFailed); ee.Fields["server_ids"] == "" {
		t.Fatal("应有 server_ids 字段")
	}

	many := make([]uuid.UUID, 51)
	for i := range many {
		many[i] = uuid.New()
	}
	_, err = e.svc.RequestUpgrades(ctx, many)
	invWantCode(t, err, errs.ValidationFailed)

	dir := t.TempDir()
	amd64 := []byte("manifest-only")
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  twoArchManifest(amd64, []byte("arm")),
		PdndArtifactAMD64: amd64,
	})
	e.svc.UsePdndDist(dir, "https://panel.example.test")
	_, err = e.svc.RequestUpgrades(ctx, []uuid.UUID{b.ID})
	if err == nil {
		t.Fatal("缺签名应失败")
	}
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Code != errs.Conflict {
		t.Fatalf("want conflict: %v", err)
	}
	if !strings.HasPrefix(ee.Message, "现在不能在线升级：") {
		t.Fatalf("消息: %s", ee.Message)
	}
}

func requestOneUpgrade(t *testing.T, e *invEnv, ctx context.Context, sess *fakeUpgradeSessions, serverID uuid.UUID) {
	t.Helper()
	sess.setOnline(serverID, upgradeOnlineInfo())
	batch, err := e.svc.RequestUpgrades(ctx, []uuid.UUID{serverID})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Results[0].OK {
		t.Fatalf("发起失败: %+v", batch.Results[0])
	}
}

func TestRecordUpgradeReportTransitions(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	sess := newFakeUpgradeSessions()
	e.svc.UseSessions(sess)

	a := e.readyServer(t, ctx, "rpt-a")
	b := e.readyServer(t, ctx, "rpt-b")
	upgradeDist(t, e.svc)
	requestOneUpgrade(t, e, ctx, sess, a.ID)
	requestOneUpgrade(t, e, ctx, sess, b.ID)

	jobA := upgradeJobID(t, e, ctx, a.ID)
	jobB := upgradeJobID(t, e, ctx, b.ID)

	var targetSHA string
	if err := e.row(ctx, `SELECT target_sha256 FROM server_upgrades WHERE id = ?`, jobA).Scan(&targetSHA); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{JobID: jobA, Phase: UpgradePhaseStaged}, now); err != nil {
		t.Fatal(err)
	}
	var st string
	var stagedAt sql.NullTime
	if err := e.row(ctx, `SELECT status, staged_at FROM server_upgrades WHERE id = ?`, jobA).Scan(&st, &stagedAt); err != nil {
		t.Fatal(err)
	}
	if st != UpgradeStaged || !stagedAt.Valid {
		t.Fatalf("staged: %s %v", st, stagedAt.Valid)
	}

	if err := e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{JobID: jobA, Phase: UpgradePhaseStaged}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT status FROM server_upgrades WHERE id = ?`, jobA).Scan(&st); err != nil || st != UpgradeStaged {
		t.Fatalf("重复 staged: %s err=%v", st, err)
	}

	if err := e.svc.RecordHello(ctx, a.ID, Hello{BinarySHA256: targetSHA}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT status FROM server_upgrades WHERE id = ?`, jobA).Scan(&st); err != nil || st != UpgradeSucceeded {
		t.Fatalf("hello 对账 succeeded: %s", st)
	}

	// Hello 已把 A 的摘要写成目标；改回旧摘要才能再发起一次（否则是 up_to_date）
	if err := e.exec(ctx, `UPDATE servers SET binary_sha256 = ? WHERE id = ?`, strings.Repeat("d", 64), a.ID); err != nil {
		t.Fatal(err)
	}
	requestOneUpgrade(t, e, ctx, sess, a.ID)
	jobA2 := upgradeJobID(t, e, ctx, a.ID)
	if err := e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{
		JobID: jobA2, Phase: UpgradePhaseStaged,
	}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{
		JobID: jobA2, Phase: UpgradePhaseRolledBack, ReasonCode: "confirm_timeout",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	var reason sql.NullString
	if err := e.row(ctx, `SELECT status, reason_code FROM server_upgrades WHERE id = ?`, jobA2).Scan(&st, &reason); err != nil {
		t.Fatal(err)
	}
	if st != UpgradeRolledBack || reason.String != "confirm_timeout" {
		t.Fatalf("rolled_back: %s %s", st, reason.String)
	}

	detail := strings.Repeat("x", 2000)
	if err := e.svc.RecordUpgradeReport(ctx, b.ID, UpgradeReport{
		JobID: jobB, Phase: UpgradePhaseFailed, ReasonCode: "", Detail: detail,
	}, now); err != nil {
		t.Fatal(err)
	}
	var detailLen int
	if err := e.row(ctx, `SELECT status, reason_code, octet_length(detail) FROM server_upgrades WHERE id = ?`, jobB).
		Scan(&st, &reason, &detailLen); err != nil {
		t.Fatal(err)
	}
	if st != UpgradeFailed || reason.String != "unknown" || detailLen > 1024 {
		t.Fatalf("failed B: %s %s len=%d", st, reason.String, detailLen)
	}

	beforeA, beforeB := jobA2, jobB
	_ = e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{JobID: uuid.Nil, Phase: UpgradePhaseFailed}, now)
	_ = e.svc.RecordUpgradeReport(ctx, b.ID, UpgradeReport{JobID: jobA2, Phase: UpgradePhaseFailed}, now)
	if upgradeJobID(t, e, ctx, a.ID) != beforeA {
		t.Fatal("Nil JobID 不应改行")
	}
	if upgradeJobID(t, e, ctx, b.ID) != beforeB {
		t.Fatal("错 server 的 JobID 不应改 B")
	}
}

func TestWatchUpgradesTimesOut(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	sess := newFakeUpgradeSessions()
	e.svc.UseSessions(sess)

	a := e.readyServer(t, ctx, "watch-a")
	upgradeDist(t, e.svc)
	requestOneUpgrade(t, e, ctx, sess, a.ID)

	if err := e.exec(ctx, `UPDATE server_upgrades SET created_at = now() - interval '1 hour', deadline_at = now() - interval '1 minute' WHERE server_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}

	n, err := e.svc.WatchUpgrades(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("WatchUpgrades=%d", n)
	}
	var st string
	var finished sql.NullTime
	if err := e.row(ctx, `SELECT status, finished_at FROM server_upgrades WHERE server_id = ?`, a.ID).Scan(&st, &finished); err != nil {
		t.Fatal(err)
	}
	if st != UpgradeTimedOut || !finished.Valid {
		t.Fatalf("timeout: %s finished=%v", st, finished.Valid)
	}

	if err := e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{
		JobID: upgradeJobID(t, e, ctx, a.ID), Phase: UpgradePhaseRolledBack, ReasonCode: "confirm_timeout",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT status FROM server_upgrades WHERE server_id = ?`, a.ID).Scan(&st); err != nil || st != UpgradeRolledBack {
		t.Fatalf("超时后 rolled_back: %s", st)
	}
}

func TestServerUpgradeViewsInList(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	sess := newFakeUpgradeSessions()
	e.svc.UseSessions(sess)
	upgradeDist(t, e.svc)

	a := e.readyServer(t, ctx, "list-a")
	b := e.readyServer(t, ctx, "list-b")
	c := e.readyServer(t, ctx, "list-c")

	sess.setOnline(a.ID, upgradeOnlineInfo())
	requestOneUpgrade(t, e, ctx, sess, a.ID)
	jobA := upgradeJobID(t, e, ctx, a.ID)
	now := time.Now()
	_ = e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{JobID: jobA, Phase: UpgradePhaseStaged}, now)
	_ = e.svc.RecordUpgradeReport(ctx, a.ID, UpgradeReport{
		JobID: jobA, Phase: UpgradePhaseRolledBack, ReasonCode: "confirm_timeout",
	}, now)

	sess.setOnline(b.ID, upgradeOnlineInfo())

	list, err := e.svc.ListServers(ctx, ServerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var srvA, srvB, srvC *Server
	for i := range list {
		switch list[i].ID {
		case a.ID:
			srvA = &list[i]
		case b.ID:
			srvB = &list[i]
		case c.ID:
			srvC = &list[i]
		}
	}
	if srvA == nil || srvB == nil || srvC == nil {
		t.Fatal("列表缺服务器")
	}
	if srvA.LastUpgrade == nil || srvA.LastUpgrade.Status != UpgradeRolledBack {
		t.Fatalf("A LastUpgrade: %+v", srvA.LastUpgrade)
	}
	if !srvA.Upgrade.Available {
		t.Fatalf("A 应可升: %+v", srvA.Upgrade)
	}
	if srvB.LastUpgrade != nil {
		t.Fatalf("B LastUpgrade 应为 nil: %+v", srvB.LastUpgrade)
	}
	if srvB.Upgrade.TargetVersion != "v2026.10.21" || !srvB.Upgrade.Available {
		t.Fatalf("B Upgrade: %+v", srvB.Upgrade)
	}
	if srvC.Upgrade.Reason != UpgradeBlockNotConnected {
		t.Fatalf("C: %+v", srvC.Upgrade)
	}
}

func TestServerUpgradesConstraints(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "cnst-srv")
	sha := strings.Repeat("c", 64)
	ver := "v2026.10.21"

	insertOpen := func() error {
		return e.exec(ctx, `INSERT INTO server_upgrades
			(server_id, target_version, target_serial, target_sha256, status, created_at, deadline_at)
			VALUES (?, ?, 1, ?, 'sent', now(), now() + interval '1 hour')`, srv.ID, ver, sha)
	}
	if err := insertOpen(); err != nil {
		t.Fatal(err)
	}
	err := e.invSavepoint(t, ctx, insertOpen)
	if err == nil {
		t.Fatal("第二条未结束任务应违反唯一索引")
	}
	if testkit.SQLState(err) != "23505" {
		t.Fatalf("唯一索引: state=%q err=%v", testkit.SQLState(err), err)
	}

	err = e.invSavepoint(t, ctx, func() error {
		return e.exec(ctx, `INSERT INTO server_upgrades
			(server_id, target_version, target_serial, target_sha256, status, finished_at, created_at, deadline_at)
			VALUES (?, ?, 1, ?, 'failed', now(), now(), now() + interval '1 hour')`, srv.ID, ver, sha)
	})
	if err == nil {
		t.Fatal("failed 无 reason_code 应失败")
	}
	if testkit.SQLState(err) != "23514" {
		t.Fatalf("reason_shape: %q %v", testkit.SQLState(err), err)
	}

	err = e.invSavepoint(t, ctx, func() error {
		return e.exec(ctx, `INSERT INTO server_upgrades
			(server_id, target_version, target_serial, target_sha256, status, finished_at, created_at, deadline_at)
			VALUES (?, ?, 1, ?, 'succeeded', now(), now(), now())`, srv.ID, ver, sha)
	})
	if err == nil {
		t.Fatal("deadline_at <= created_at 应失败")
	}
	if testkit.SQLState(err) != "23514" {
		t.Fatalf("deadline_order: %q %v", testkit.SQLState(err), err)
	}
}
