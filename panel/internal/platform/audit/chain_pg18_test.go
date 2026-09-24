// [INPUT]: 依赖 platform/pg18test 打开 audit 域的一次性库，依赖 audit.go 的 Write 与 chain.go 的 VerifyChain、chainHashV1
// [OUTPUT]: 对外提供 TestAuditChainPG18、TestAuditChainLegacyPG18
// [POS]: platform/audit 的 PG18 测试：第二版口径的带摘要多行链能验过，改摘要 / auth_context / 时间、删中间行、抹链序号都能指出断点；第一版存量行按 VerifyChain 的规则处理
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func openAuditPG18(t *testing.T) (context.Context, *pgxpool.Pool, *db.Pool) {
	return pg18test.Open(t, pg18test.Fixture{
		Domain: "AUDIT", DatabasePrefix: "pandora_audit_",
		MarkerTable: "pandora_audit_test_marker", CommentTag: "pandora-audit-pg18",
	})
}

func auditSeedTenant(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant, slug string) {
	t.Helper()
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name,default_currency)
		VALUES($1,$2,$2,'CNY')`, tenant, slug); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
}

func auditWrite(t *testing.T, ctx context.Context, app *db.Pool, tenant string, entries ...Entry) {
	t.Helper()
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		for _, e := range entries {
			if err := Write(ctx, tx, tenant, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write audit: %v", err)
	}
}

// auditIDs 按链序返回记录 id：第一版行按 occurred_at, id，第二版行按 chain_seq。
func auditIDs(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant string) []string {
	t.Helper()
	rows, err := admin.Query(ctx, `SELECT id::text FROM audit_events WHERE tenant_id=$1
		ORDER BY chain_seq NULLS FIRST, occurred_at, id`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// verifyAfter 在一个回滚的管理事务里先执行篡改（绕过追加写触发器），再校验。
// 不篡改时 tamper 传空串。篡改随事务回滚，同一条基础链可以反复用。
func verifyAfter(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant, tamper string, args ...any) ChainReport {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if tamper != "" {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		tag, err := tx.Exec(ctx, tamper, args...)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("tamper %q: rows=%d err=%v", tamper, tag.RowsAffected(), err)
		}
	}
	report, err := VerifyChain(ctx, tx, tenant)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return report
}

func TestAuditChainPG18(t *testing.T) {
	ctx, admin, app := openAuditPG18(t)
	const (
		tenant = "85000000-0000-4000-8000-000000000001"
		actor  = "85000000-0000-4000-8000-000000000011"
	)
	auditSeedTenant(t, ctx, admin, tenant, "audit-chain-pg18")
	actorID := actor
	// 大写 uuid：库里存小写，哈希必须用库里那一种
	upperResource := strings.ToUpper("85000000-0000-4000-8000-0000000000a1")
	reauthed := httpx.WithPrincipal(ctx, &httpx.Principal{Kind: "admin", UserID: actor,
		SessionID: "s1", ReauthedRecently: true})

	type nodeDigest struct {
		Status  string   `json:"status"`
		Weight  float64  `json:"weight"`
		Tags    []string `json:"tags"`
		Comment string   `json:"comment"`
	}
	auditWrite(t, ctx, app, tenant,
		Entry{ActorKind: "admin", ActorID: &actorID, Action: "node.update", ResourceType: "node",
			ResourceID: &upperResource, BeforeDigest: nodeDigest{"active", 1.5, []string{"jp", "<b>"}, "x & y"},
			AfterDigest: map[string]any{"status": "disabled", "weight": 1e21, "tiny": 1e-7, "nested": map[string]any{"z": 1, "a": []any{nil, true}}}},
		Entry{ActorKind: "system", Action: "order.expired", AfterDigest: map[string]any{"count": 3}})
	auditWrite(t, ctx, app, tenant, Entry{ActorKind: "admin", ActorID: &actorID, Action: "user.status",
		ActorLabel: "ops@audit.invalid", SourceIP: "192.0.2.10", UserAgent: "ua/1",
		AfterDigest: map[string]any{"status": "banned", "note": "中文"}})
	if err := app.InTx(reauthed, db.Scope{TenantID: tenant, ActorID: actor}, func(tx pgx.Tx) error {
		return Write(reauthed, tx, tenant, Entry{ActorKind: "admin", ActorID: &actorID,
			Action: "user.reset_password", AfterDigest: map[string]any{"forced": true}})
	}); err != nil {
		t.Fatal(err)
	}

	// 先开事务、后拿到链锁：occurred_at（事务开始时刻）比先入链的那条早，
	// 第一版按时间排序会在这里报断链、写入端还会让下一条挂错前驱。
	early, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = early.Rollback(ctx) }()
	if _, err := early.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	auditWrite(t, ctx, app, tenant, Entry{ActorKind: "system", Action: "late.tx", AfterDigest: map[string]any{"n": 1}})
	if err := Write(ctx, early, tenant, Entry{ActorKind: "system", Action: "early.tx", AfterDigest: map[string]any{"n": 2}}); err != nil {
		t.Fatal(err)
	}
	if err := early.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	auditWrite(t, ctx, app, tenant, Entry{ActorKind: "system", Action: "tail"})

	var inverted bool
	if err := admin.QueryRow(ctx, `SELECT e.occurred_at < l.occurred_at AND e.chain_seq > l.chain_seq
		FROM audit_events e, audit_events l
		WHERE e.tenant_id=$1 AND l.tenant_id=$1 AND e.action='early.tx' AND l.action='late.tx'`, tenant).Scan(&inverted); err != nil || !inverted {
		t.Fatalf("early transaction was not ordered after the late one by chain_seq: inverted=%v err=%v", inverted, err)
	}
	var seqs []int64
	rows, err := admin.Query(ctx, `SELECT chain_seq FROM audit_events WHERE tenant_id=$1 ORDER BY chain_seq`, tenant)
	if err == nil {
		seqs, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	}
	if err != nil || len(seqs) != 7 || seqs[0] != 1 || seqs[6] != 7 {
		t.Fatalf("chain_seq=%v err=%v", seqs, err)
	}
	var storedResource string
	if err := admin.QueryRow(ctx, `SELECT resource_id::text FROM audit_events WHERE tenant_id=$1 AND action='node.update'`, tenant).Scan(&storedResource); err != nil || storedResource != strings.ToLower(upperResource) {
		t.Fatalf("resource_id=%q err=%v", storedResource, err)
	}

	// 运行时角色在 RLS 下校验：完整
	var report ChainReport
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) (err error) {
		report, err = VerifyChain(ctx, tx, tenant)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if report != (ChainReport{Rows: 7}) {
		t.Fatalf("intact chain report=%+v", report)
	}

	ids := auditIDs(t, ctx, admin, tenant)
	var reauthRow string
	if err := admin.QueryRow(ctx, `SELECT id::text FROM audit_events WHERE tenant_id=$1 AND auth_context='reauth'`, tenant).Scan(&reauthRow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sql string
		args      []any
		at        string
		reason    string
	}{
		{"digest value", `UPDATE audit_events SET after_digest = jsonb_set(after_digest, '{status}', '"active"') WHERE id=$1`, []any{ids[2]}, ids[2], "hash"},
		{"digest key added", `UPDATE audit_events SET before_digest = before_digest || '{"extra": 0}' WHERE id=$1`, []any{ids[0]}, ids[0], "hash"},
		{"auth_context", `UPDATE audit_events SET auth_context='session' WHERE id=$1`, []any{reauthRow}, reauthRow, "hash"},
		{"auth_context cleared", `UPDATE audit_events SET auth_context=NULL WHERE id=$1`, []any{reauthRow}, reauthRow, "hash"},
		{"occurred_at", `UPDATE audit_events SET occurred_at = occurred_at - interval '1 hour' WHERE id=$1`, []any{ids[1]}, ids[1], "hash"},
		{"source ip hash", `UPDATE audit_events SET source_ip_hash = '\x00' WHERE id=$1`, []any{ids[2]}, ids[2], "hash"},
		{"middle row deleted", `DELETE FROM audit_events WHERE id=$1`, []any{ids[3]}, ids[4], "link"},
		{"chain_seq erased", `UPDATE audit_events SET chain_seq=NULL WHERE id=$1`, []any{ids[4]}, ids[4], "link"},
		{"chain_seq shifted", `UPDATE audit_events SET chain_seq=chain_seq+100 WHERE id=$1`, []any{ids[6]}, ids[6], "seq"},
	} {
		got := verifyAfter(t, ctx, admin, tenant, tc.sql, tc.args...)
		if got.BrokenAt != tc.at || got.Reason != tc.reason {
			t.Errorf("%s: report=%+v, want broken at %s (%s)", tc.name, got, tc.at, tc.reason)
		}
	}

	// 链分叉在写入时就被拒绝
	_, err = admin.Exec(ctx, `INSERT INTO audit_events(tenant_id, actor_kind, action, chain_seq, entry_hash)
		VALUES($1, 'system', 'fork', 3, '\x00')`, tenant)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "audit_events_chain_seq_key" {
		t.Fatalf("duplicate chain_seq insert err=%v", err)
	}
}

// 第一版存量行：00083 之前写入、chain_seq 为空。直接按第一版口径造数，
// 再由 Write 接上第二版。
func TestAuditChainLegacyPG18(t *testing.T) {
	ctx, admin, app := openAuditPG18(t)
	const (
		tenant = "85000000-0000-4000-8000-000000000002"
		actor  = "85000000-0000-4000-8000-000000000012"
	)
	auditSeedTenant(t, ctx, admin, tenant, "audit-legacy-pg18")
	actorID := actor

	structDigest, _ := json.Marshal(struct {
		Status string `json:"status"`
		ID     int    `json:"id"`
	}{"active", 3})
	mapDigest, _ := json.Marshal(map[string]any{"status": "banned", "reason": "<spam>"})
	legacy := []struct {
		action string
		after  []byte
	}{
		{"legacy.plain", nil},
		{"legacy.struct", structDigest}, // 字段顺序已丢：只能核对链接
		{"legacy.map", mapDigest},       // 键排序紧凑形：严格复算
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var prev []byte
	for i, row := range legacy {
		e := Entry{ActorKind: "admin", ActorID: &actorID, Action: row.action, Outcome: "success"}
		hash := chainHashV1(prev, tenant, e, nil, row.after)
		var after any
		if row.after != nil {
			after = string(row.after)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO audit_events
			(tenant_id, actor_kind, actor_id, action, after_digest, outcome, prev_hash, entry_hash, occurred_at)
			VALUES($1,'admin',$2,$3,$4::jsonb,'success',$5,$6,$7)`,
			tenant, actor, row.action, after, prev, hash, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}
		prev = hash
	}
	auditWrite(t, ctx, app, tenant, Entry{ActorKind: "system", Action: "v2.first"})
	auditWrite(t, ctx, app, tenant, Entry{ActorKind: "system", Action: "v2.second", AfterDigest: map[string]any{"k": "v"}})

	var firstPrev []byte
	var firstSeq int64
	if err := admin.QueryRow(ctx, `SELECT prev_hash, chain_seq FROM audit_events WHERE tenant_id=$1 AND action='v2.first'`, tenant).Scan(&firstPrev, &firstSeq); err != nil || string(firstPrev) != string(prev) || firstSeq != 1 {
		t.Fatalf("first v2 row prev/seq=%x/%d err=%v, want legacy tail %x / 1", firstPrev, firstSeq, err, prev)
	}

	if got := verifyAfter(t, ctx, admin, tenant, ""); got != (ChainReport{Rows: 5, LegacyLinkOnly: 1}) {
		t.Fatalf("mixed chain report=%+v", got)
	}
	ids := auditIDs(t, ctx, admin, tenant) // plain, struct, map, v2.first, v2.second
	for _, tc := range []struct {
		name, sql string
		arg       string
		want      ChainReport
	}{
		// 不带摘要的存量行严格复算
		{"legacy plain action", `UPDATE audit_events SET action='legacy.other' WHERE id=$1`, ids[0],
			ChainReport{Rows: 1, BrokenAt: ids[0], Reason: "hash"}},
		// 只核对链接的存量行：改摘要验不出来，这是存量口径的边界，报告里计数
		{"legacy struct digest", `UPDATE audit_events SET after_digest='{"id": 4, "status": "active"}' WHERE id=$1`, ids[1],
			ChainReport{Rows: 5, LegacyLinkOnly: 1}},
		// 能严格复算的 map 摘要被改后退成只核对链接，计数随之变化
		{"legacy map digest", `UPDATE audit_events SET after_digest='{"reason": "ok", "status": "banned"}' WHERE id=$1`, ids[2],
			ChainReport{Rows: 5, LegacyLinkOnly: 2}},
		// 删掉存量中间行：下一行接不上
		{"legacy row deleted", `DELETE FROM audit_events WHERE id=$1`, ids[1],
			ChainReport{Rows: 2, BrokenAt: ids[2], Reason: "link"}},
		// 存量行的 entry_hash 被改：下一行接不上
		{"legacy entry_hash", `UPDATE audit_events SET entry_hash='\x00' WHERE id=$1`, ids[1],
			ChainReport{Rows: 3, LegacyLinkOnly: 1, BrokenAt: ids[2], Reason: "link"}},
		// 把第二版行降成第一版：按时间排进存量段，按第一版口径复算不出
		{"v2 downgraded", `UPDATE audit_events SET chain_seq=NULL WHERE id=$1`, ids[3],
			ChainReport{Rows: 4, LegacyLinkOnly: 1, BrokenAt: ids[3], Reason: "hash"}},
	} {
		if got := verifyAfter(t, ctx, admin, tenant, tc.sql, tc.arg); got != tc.want {
			t.Errorf("%s: report=%+v, want %+v", tc.name, got, tc.want)
		}
	}
}
