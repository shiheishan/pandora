package repo

// 节点会话落库（session.go）的 PostgreSQL 集成测试。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func sessInsertServer(t *testing.T, d *db.DB, ctx context.Context) uuid.UUID {
	t.Helper()
	name := "srv-" + uuid.NewString()[:8]
	var id uuid.UUID
	if err := d.Conn(ctx).Raw(`INSERT INTO servers (name) VALUES (?) RETURNING id`, name).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func sessInsertNode(t *testing.T, d *db.DB, ctx context.Context, serverID uuid.UUID) uuid.UUID {
	t.Helper()
	name := "nd-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	var id uuid.UUID
	if err := d.Conn(ctx).Raw(`INSERT INTO nodes (name, server_id) VALUES (?, ?) RETURNING id`, name, serverID).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func sessSHA(c byte) string {
	return strings.Repeat(string(c), 64)
}

func TestUpdateServerAgentWritesOnlyOnChange(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	serverID := sessInsertServer(t, d, ctx)

	ver := sessStr("1.2.3")
	sha := sessStr(sessSHA('a'))
	host := sessStr("host1")
	osName := sessStr("linux")
	arch := sessStr("amd64")
	cores := sessInt32(4)
	mem := sessInt32(2048)
	disk := sessInt32(40)

	in := ServerAgentInfo{
		AgentVersion: ver, BinarySHA256: sha, Hostname: host, OSName: osName, Architecture: arch,
		CPUCores: cores, MemoryMB: mem, DiskGB: disk,
	}

	changed, err := r.UpdateServerAgent(ctx, serverID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("第一次写入: 期望 changed=true，得到 false")
	}
	assertServerAgent(t, d, ctx, serverID, in)

	changed, err = r.UpdateServerAgent(ctx, serverID, in)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("相同值再写: 期望 changed=false，得到 true")
	}

	in.AgentVersion = sessStr("1.2.4")
	changed, err = r.UpdateServerAgent(ctx, serverID, in)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("改 agent_version: 期望 changed=true，得到 false")
	}

	empty := ServerAgentInfo{}
	changed, err = r.UpdateServerAgent(ctx, serverID, empty)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("全部 nil: 期望 changed=true，得到 false")
	}
	assertServerAgentNull(t, d, ctx, serverID)

	softID := sessInsertServer(t, d, ctx)
	if err := d.Conn(ctx).Exec(`UPDATE servers SET deleted_at = now() WHERE id = ?`, softID).Error; err != nil {
		t.Fatal(err)
	}
	changed, err = r.UpdateServerAgent(ctx, softID, ServerAgentInfo{AgentVersion: sessStr("x")})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("已软删服务器: 期望 changed=false，得到 true")
	}
}

func assertServerAgent(t *testing.T, d *db.DB, ctx context.Context, serverID uuid.UUID, want ServerAgentInfo) {
	var av, sha, host, osName, arch *string
	var cores, mem, disk *int32
	err := d.Conn(ctx).Raw(`SELECT agent_version, binary_sha256, hostname, os_name, architecture, cpu_cores, memory_mb, disk_gb
		FROM servers WHERE id = ?`, serverID).Row().Scan(&av, &sha, &host, &osName, &arch, &cores, &mem, &disk)
	if err != nil {
		t.Fatal(err)
	}
	sessEqStr(t, "agent_version", av, want.AgentVersion)
	sessEqStr(t, "binary_sha256", sha, want.BinarySHA256)
	sessEqStr(t, "hostname", host, want.Hostname)
	sessEqStr(t, "os_name", osName, want.OSName)
	sessEqStr(t, "architecture", arch, want.Architecture)
	sessEqInt32(t, "cpu_cores", cores, want.CPUCores)
	sessEqInt32(t, "memory_mb", mem, want.MemoryMB)
	sessEqInt32(t, "disk_gb", disk, want.DiskGB)
}

func assertServerAgentNull(t *testing.T, d *db.DB, ctx context.Context, serverID uuid.UUID) {
	var av, sha, host, osName, arch *string
	var cores, mem, disk *int32
	err := d.Conn(ctx).Raw(`SELECT agent_version, binary_sha256, hostname, os_name, architecture, cpu_cores, memory_mb, disk_gb
		FROM servers WHERE id = ?`, serverID).Row().Scan(&av, &sha, &host, &osName, &arch, &cores, &mem, &disk)
	if err != nil {
		t.Fatal(err)
	}
	if av != nil || sha != nil || host != nil || osName != nil || arch != nil || cores != nil || mem != nil || disk != nil {
		t.Fatalf("全部 nil 后读回: 期望各列 NULL，得到 agent_version=%v binary_sha256=%v hostname=%v os_name=%v architecture=%v cpu_cores=%v memory_mb=%v disk_gb=%v",
			av, sha, host, osName, arch, cores, mem, disk)
	}
}

func TestTouchServerHeartbeat(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	serverID := sessInsertServer(t, d, ctx)

	at1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := r.TouchServerHeartbeat(ctx, serverID, at1); err != nil {
		t.Fatal(err)
	}
	sessReadHeartbeat(t, d, ctx, serverID, at1)

	at2 := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
	if err := r.TouchServerHeartbeat(ctx, serverID, at2); err != nil {
		t.Fatal(err)
	}
	sessReadHeartbeat(t, d, ctx, serverID, at2)
}

func sessReadHeartbeat(t *testing.T, d *db.DB, ctx context.Context, serverID uuid.UUID, want time.Time) {
	var got time.Time
	if err := d.Conn(ctx).Raw(`SELECT last_heartbeat_at FROM servers WHERE id = ?`, serverID).Row().Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("last_heartbeat_at: 期望 %v，得到 %v", want, got)
	}
}

func TestUpdateNodeRuntimes(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	sS := sessInsertServer(t, d, ctx)
	sT := sessInsertServer(t, d, ctx)
	n1 := sessInsertNode(t, d, ctx, sS)
	n2 := sessInsertNode(t, d, ctx, sS)
	n3 := sessInsertNode(t, d, ctx, sT)

	at1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	reason := sessStr("port_in_use")
	rows1 := []NodeRuntimeRow{
		{NodeID: n1, Status: "running"},
		{NodeID: n2, Status: "degraded", Reason: reason, ReasonNodeID: &n1},
		{NodeID: n3, Status: "down"},
	}
	n, err := r.UpdateNodeRuntimes(ctx, sS, rows1, at1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("第一次写: 期望影响 2 行，得到 %d", n)
	}
	assertNodeRuntime(t, d, ctx, n1, sessStr("running"), nil, nil, at1)
	assertNodeRuntime(t, d, ctx, n2, sessStr("degraded"), reason, &n1, at1)
	assertNodeRuntimeNull(t, d, ctx, n3)

	at2 := time.Date(2026, 2, 1, 11, 0, 0, 0, time.UTC)
	n, err = r.UpdateNodeRuntimes(ctx, sS, rows1, at2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("同样内容 at2: 期望影响 0 行，得到 %d", n)
	}
	assertNodeRuntime(t, d, ctx, n1, sessStr("running"), nil, nil, at1)
	assertNodeRuntime(t, d, ctx, n2, sessStr("degraded"), reason, &n1, at1)

	at3 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	rows3 := []NodeRuntimeRow{
		{NodeID: n1, Status: "running"},
		{NodeID: n2, Status: "running"},
	}
	n, err = r.UpdateNodeRuntimes(ctx, sS, rows3, at3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n2 改为 running: 期望影响 1 行，得到 %d", n)
	}
	assertNodeRuntime(t, d, ctx, n1, sessStr("running"), nil, nil, at1)
	assertNodeRuntime(t, d, ctx, n2, sessStr("running"), nil, nil, at3)

	n, err = r.UpdateNodeRuntimes(ctx, sS, nil, at3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("空切片: 期望影响 0 行，得到 %d", n)
	}
}

func assertNodeRuntime(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, status *string, reason *string, reasonNode *uuid.UUID, stateAt time.Time) {
	var st, rs *string
	var rn *uuid.UUID
	var at *time.Time
	err := d.Conn(ctx).Raw(`SELECT runtime_status, runtime_reason, runtime_reason_node_id, runtime_state_at FROM nodes WHERE id = ?`, nodeID).
		Row().Scan(&st, &rs, &rn, &at)
	if err != nil {
		t.Fatal(err)
	}
	sessEqStr(t, "runtime_status", st, status)
	sessEqStr(t, "runtime_reason", rs, reason)
	sessEqUUID(t, "runtime_reason_node_id", rn, reasonNode)
	if at == nil {
		t.Fatalf("runtime_state_at: 期望 %v，得到 NULL", stateAt)
	}
	if !at.Equal(stateAt) {
		t.Fatalf("runtime_state_at: 期望 %v，得到 %v", stateAt, *at)
	}
}

func assertNodeRuntimeNull(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID) {
	var st *string
	var at *time.Time
	err := d.Conn(ctx).Raw(`SELECT runtime_status, runtime_state_at FROM nodes WHERE id = ?`, nodeID).Row().Scan(&st, &at)
	if err != nil {
		t.Fatal(err)
	}
	if st != nil || at != nil {
		t.Fatalf("n3 初始 runtime: 期望 NULL，得到 status=%v state_at=%v", st, at)
	}
}

func TestMarkConfigApplied(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	sS := sessInsertServer(t, d, ctx)
	sOther := sessInsertServer(t, d, ctx)
	nodeID := sessInsertNode(t, d, ctx, sS)

	failAt := time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC)
	if err := d.Conn(ctx).Exec(`UPDATE nodes SET apply_failed_sha256 = ?, apply_failed_reason = ?, apply_failed_detail = ?, apply_failed_at = ? WHERE id = ?`,
		sessSHA('b'), "start_failed", "x", failAt, nodeID).Error; err != nil {
		t.Fatal(err)
	}

	at1 := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	shaA := sessSHA('a')
	changed, err := r.MarkConfigApplied(ctx, sS, nodeID, shaA, at1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("MarkConfigApplied: 期望 changed=true，得到 false")
	}
	assertConfigApplied(t, d, ctx, nodeID, shaA, at1)
	assertApplyFailedClear(t, d, ctx, nodeID)

	changed, err = r.MarkConfigApplied(ctx, sS, nodeID, shaA, at1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("再次同样 MarkConfigApplied: 期望 changed=false，得到 true")
	}
	readAppliedAt(t, d, ctx, nodeID, at1)

	changed, err = r.MarkConfigApplied(ctx, sOther, nodeID, sessSHA('z'), at1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("错误 serverID: 期望 changed=false，得到 true")
	}
	assertConfigApplied(t, d, ctx, nodeID, shaA, at1)
}

func TestMarkConfigFailed(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	sS := sessInsertServer(t, d, ctx)
	sOther := sessInsertServer(t, d, ctx)
	nodeID := sessInsertNode(t, d, ctx, sS)

	shaA := sessSHA('a')
	appliedAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := d.Conn(ctx).Exec(`UPDATE nodes SET applied_config_sha256 = ?, applied_config_at = ? WHERE id = ?`, shaA, appliedAt, nodeID).Error; err != nil {
		t.Fatal(err)
	}

	shaB := sessSHA('b')
	at1 := time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC)
	detail := sessStr("boom")
	changed, err := r.MarkConfigFailed(ctx, sS, nodeID, shaB, "start_failed", detail, at1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("MarkConfigFailed: 期望 changed=true，得到 false")
	}
	assertApplyFailed(t, d, ctx, nodeID, shaB, "start_failed", detail, at1)
	readAppliedSHA(t, d, ctx, nodeID, shaA)

	changed, err = r.MarkConfigFailed(ctx, sS, nodeID, shaB, "start_failed", detail, at1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("同样失败再写: 期望 changed=false，得到 true")
	}
	readApplyFailedAt(t, d, ctx, nodeID, at1)

	changed, err = r.MarkConfigFailed(ctx, sS, nodeID, shaB, "start_failed", nil, at1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("detail 改为 nil: 期望 changed=true，得到 false")
	}
	assertApplyFailed(t, d, ctx, nodeID, shaB, "start_failed", nil, at1)

	changed, err = r.MarkConfigFailed(ctx, sOther, nodeID, shaB, "start_failed", nil, at1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("错误 serverID: 期望 changed=false，得到 true")
	}

	shaC := sessSHA('c')
	at2 := time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)
	changed, err = r.MarkConfigApplied(ctx, sS, nodeID, shaC, at2)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("MarkConfigApplied 清空失败: 期望 changed=true，得到 false")
	}
	assertConfigApplied(t, d, ctx, nodeID, shaC, at2)
	assertApplyFailedClear(t, d, ctx, nodeID)
}

func assertConfigApplied(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, sha string, at time.Time) {
	var gotSHA *string
	var gotAt *time.Time
	if err := d.Conn(ctx).Raw(`SELECT applied_config_sha256, applied_config_at FROM nodes WHERE id = ?`, nodeID).Row().Scan(&gotSHA, &gotAt); err != nil {
		t.Fatal(err)
	}
	if gotSHA == nil || *gotSHA != sha {
		t.Fatalf("applied_config_sha256: 期望 %q，得到 %v", sha, gotSHA)
	}
	if gotAt == nil || !gotAt.Equal(at) {
		t.Fatalf("applied_config_at: 期望 %v，得到 %v", at, gotAt)
	}
}

func assertApplyFailedClear(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID) {
	var sha, reason, detail *string
	var at *time.Time
	err := d.Conn(ctx).Raw(`SELECT apply_failed_sha256, apply_failed_reason, apply_failed_detail, apply_failed_at FROM nodes WHERE id = ?`, nodeID).
		Row().Scan(&sha, &reason, &detail, &at)
	if err != nil {
		t.Fatal(err)
	}
	if sha != nil || reason != nil || detail != nil || at != nil {
		t.Fatalf("apply_failed_*: 期望全 NULL，得到 sha=%v reason=%v detail=%v at=%v", sha, reason, detail, at)
	}
}

func assertApplyFailed(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, sha, reason string, detail *string, at time.Time) {
	var gotSHA, gotReason, gotDetail *string
	var gotAt *time.Time
	err := d.Conn(ctx).Raw(`SELECT apply_failed_sha256, apply_failed_reason, apply_failed_detail, apply_failed_at FROM nodes WHERE id = ?`, nodeID).
		Row().Scan(&gotSHA, &gotReason, &gotDetail, &gotAt)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA == nil || *gotSHA != sha {
		t.Fatalf("apply_failed_sha256: 期望 %q，得到 %v", sha, gotSHA)
	}
	if gotReason == nil || *gotReason != reason {
		t.Fatalf("apply_failed_reason: 期望 %q，得到 %v", reason, gotReason)
	}
	sessEqStr(t, "apply_failed_detail", gotDetail, detail)
	if gotAt == nil || !gotAt.Equal(at) {
		t.Fatalf("apply_failed_at: 期望 %v，得到 %v", at, gotAt)
	}
}

func readAppliedAt(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, want time.Time) {
	var at *time.Time
	if err := d.Conn(ctx).Raw(`SELECT applied_config_at FROM nodes WHERE id = ?`, nodeID).Row().Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at == nil || !at.Equal(want) {
		t.Fatalf("applied_config_at 应保持: 期望 %v，得到 %v", want, at)
	}
}

func readAppliedSHA(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, want string) {
	var sha *string
	if err := d.Conn(ctx).Raw(`SELECT applied_config_sha256 FROM nodes WHERE id = ?`, nodeID).Row().Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if sha == nil || *sha != want {
		t.Fatalf("applied_config_sha256 应保持: 期望 %q，得到 %v", want, sha)
	}
}

func readApplyFailedAt(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, want time.Time) {
	var at *time.Time
	if err := d.Conn(ctx).Raw(`SELECT apply_failed_at FROM nodes WHERE id = ?`, nodeID).Row().Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at == nil || !at.Equal(want) {
		t.Fatalf("apply_failed_at 应保持: 期望 %v，得到 %v", want, at)
	}
}

func sessStr(s string) *string { return &s }

func sessInt32(n int32) *int32 { return &n }

func sessEqStr(t *testing.T, field string, got, want *string) {
	if (got == nil) != (want == nil) {
		t.Fatalf("%s: 期望 %v，得到 %v", field, want, got)
	}
	if got != nil && want != nil && *got != *want {
		t.Fatalf("%s: 期望 %q，得到 %q", field, *want, *got)
	}
}

func sessEqInt32(t *testing.T, field string, got, want *int32) {
	if (got == nil) != (want == nil) {
		t.Fatalf("%s: 期望 %v，得到 %v", field, want, got)
	}
	if got != nil && want != nil && *got != *want {
		t.Fatalf("%s: 期望 %d，得到 %d", field, *want, *got)
	}
}

func sessEqUUID(t *testing.T, field string, got, want *uuid.UUID) {
	if (got == nil) != (want == nil) {
		t.Fatalf("%s: 期望 %v，得到 %v", field, want, got)
	}
	if got != nil && want != nil && *got != *want {
		t.Fatalf("%s: 期望 %v，得到 %v", field, *want, *got)
	}
}
