package giftcard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const giftcardPG18Fixture = "disposable-v1"

// TestGiftCardBatchesPG18 证明迁移 00069 与批次用例：存量回填（D-C-4 从严）、
// 生码写批次、列表只给掩码、一次性导出、批次行不可改写、租户隔离。
// 由 deploy/run-pg18-gates.sh 的 giftcard 域驱动；未配置时跳过。
func TestGiftCardBatchesPG18(t *testing.T) {
	ctx, admin, app := openGiftcardPG18(t)

	const (
		tenantID = "74000000-0000-7000-8000-000000000001"
		decoyID  = "74000000-0000-7000-8000-000000000002"
		actorID  = "74000000-0000-7000-8000-000000000011"
		email    = "giftcard-batches-admin@example.test"
		tmplID   = "74000000-0000-7000-8000-000000000021"
		legacyID = "74000000-0000-7000-8000-000000000031"
	)
	mustAdmin := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	mustAdmin(`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES
		($1, 'giftcard-batches-pg18', 'Giftcard Batches PG18', 'CNY'),
		($2, 'giftcard-batches-decoy', 'Giftcard Batches Decoy', 'CNY')`, tenantID, decoyID)
	mustAdmin(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, $3, 'Giftcard Admin', 'active')`, actorID, tenantID, email)
	mustAdmin(`INSERT INTO gift_card_templates (id, tenant_id, name, type, rewards)
		VALUES ($1, $2, 'Batch PG18', 'general', '{"balance":100}'::jsonb)`, tmplID, tenantID)

	// 1) 存量回填。00069 之前的码只有裸 batch_id；这里用超级用户关掉触发器
	// （包括新加的批次外键）塞进三张旧码和当时的生码审计，再原样执行迁移里的
	// 回填语句。
	legacyCodes := []string{"OLDABCDEFGHJKMN", "OLDPQRSTUVWXYZ2", "OLD3456789ABCDE"}
	mustAdmin(`SET session_replication_role = replica`)
	for _, code := range legacyCodes {
		mustAdmin(`INSERT INTO gift_card_codes (tenant_id, template_id, code, batch_id, created_at)
			VALUES ($1, $2, $3, $4, now() - interval '30 days')`, tenantID, tmplID, code, legacyID)
	}
	mustAdmin(`INSERT INTO audit_events (tenant_id, actor_kind, actor_id, action, after_digest)
		VALUES ($1, 'admin', $2, 'gift_card.codes_generated', jsonb_build_object('batch_id', $3::text))`,
		tenantID, actorID, legacyID)
	mustAdmin(`SET session_replication_role = origin`)
	mustAdmin(giftcardPG18BackfillSQL(t))

	var legacyPrefix string
	var legacyCount int
	var legacyExported bool
	var legacyExporter, legacyCreator *string
	if err := admin.QueryRow(ctx, `SELECT prefix, count, exported_at IS NOT NULL,
			exported_by::text, created_by::text
		FROM gift_card_batches WHERE id = $1`, legacyID).Scan(
		&legacyPrefix, &legacyCount, &legacyExported, &legacyExporter, &legacyCreator); err != nil {
		t.Fatalf("read backfilled batch: %v", err)
	}
	if legacyPrefix != "OLD" || legacyCount != 3 || !legacyExported || legacyExporter != nil ||
		legacyCreator == nil || *legacyCreator != actorID {
		t.Fatalf("backfilled batch prefix=%q count=%d exported=%v exporter=%v creator=%v",
			legacyPrefix, legacyCount, legacyExported, legacyExporter, legacyCreator)
	}
	t.Log("marker=giftcard_pg18_legacy_backfill_ok")

	// 2) 生码：批次行与码在同一事务，响应只带 4 张明文样例。
	svc := New(app, nil, nil)
	gen, err := svc.GenerateCodes(ctx, tenantID, GenerateInput{
		TemplateID: tmplID, Count: 6, Prefix: "GC", ActorID: actorID,
	})
	if err != nil {
		t.Fatalf("generate codes: %v", err)
	}
	if len(gen.Sample) != 4 || gen.Count != 6 || gen.Batch.Count != 6 ||
		gen.Batch.Prefix != "GC" || gen.Batch.ExportedAt != nil ||
		gen.Batch.CreatedByEmail == nil || *gen.Batch.CreatedByEmail != email {
		t.Fatalf("generate output=%+v", gen)
	}
	for _, code := range gen.Sample {
		if !strings.HasPrefix(code, "GC") || len(code) != 14 {
			t.Fatalf("sample code %q has the wrong shape", code)
		}
	}

	codes, total, err := svc.ListCodes(ctx, tenantID, ListCodesInput{BatchID: gen.BatchID})
	if err != nil || total != 6 || len(codes) != 6 {
		t.Fatalf("list batch codes total=%d len=%d err=%v", total, len(codes), err)
	}
	masked := map[string]bool{}
	for _, c := range codes {
		if !strings.HasSuffix(c.CodeMasked, "••••••••") {
			t.Fatalf("code list leaked plaintext: %q", c.CodeMasked)
		}
		masked[c.CodeMasked] = true
	}
	if !masked[MaskCode(gen.Sample[0])] {
		t.Fatalf("masked list does not contain the mask of sample %q", gen.Sample[0])
	}

	batches, batchTotal, err := svc.ListBatches(ctx, tenantID, ListBatchesInput{})
	if err != nil || batchTotal != 2 || len(batches) != 2 || batches[0].ID != gen.BatchID ||
		batches[1].ID != legacyID || batches[1].ExportedAt == nil {
		t.Fatalf("list batches total=%d items=%+v err=%v", batchTotal, batches, err)
	}
	t.Log("marker=giftcard_pg18_generate_and_mask_ok")

	// 3) 一次性导出：第一次拿到全部明文并打标记，第二次 409；存量批次一律 409。
	export, err := svc.ExportBatch(ctx, tenantID, gen.BatchID, actorID)
	if err != nil {
		t.Fatalf("export batch: %v", err)
	}
	exported := map[string]bool{}
	for _, row := range export.Rows {
		exported[row.Code] = true
		if row.TemplateName != "Batch PG18" {
			t.Fatalf("export row template=%q", row.TemplateName)
		}
	}
	if len(export.Rows) != 6 || export.Batch.ExportedAt == nil ||
		export.Batch.ExportedByEmail == nil || *export.Batch.ExportedByEmail != email {
		t.Fatalf("export rows=%d batch=%+v", len(export.Rows), export.Batch)
	}
	for _, code := range gen.Sample {
		if !exported[code] {
			t.Fatalf("export is missing sample code %q", code)
		}
	}
	if _, err := svc.ExportBatch(ctx, tenantID, gen.BatchID, actorID); !errors.Is(err, ErrBatchAlreadyExported) {
		t.Fatalf("second export err=%v, want ErrBatchAlreadyExported", err)
	}
	if _, err := svc.ExportBatch(ctx, tenantID, legacyID, actorID); !errors.Is(err, ErrBatchAlreadyExported) {
		t.Fatalf("legacy batch export err=%v, want ErrBatchAlreadyExported (D-C-4)", err)
	}
	if _, err := svc.ExportBatch(ctx, decoyID, gen.BatchID, actorID); !isNotFound(err) {
		t.Fatalf("cross-tenant export err=%v, want not_found", err)
	}

	var audits int
	var digest string
	if err := admin.QueryRow(ctx, `SELECT count(*)::int, coalesce(string_agg(after_digest::text, ' '), '')
		FROM audit_events WHERE tenant_id = $1 AND action = 'gift_card.batch_exported'
		  AND resource_id = $2::uuid`, tenantID, gen.BatchID).Scan(&audits, &digest); err != nil {
		t.Fatalf("read export audit: %v", err)
	}
	if audits != 1 {
		t.Fatalf("export audits=%d want 1", audits)
	}
	for code := range exported {
		if strings.Contains(digest, code) {
			t.Fatalf("export audit leaked plaintext code %q", code)
		}
	}
	t.Log("marker=giftcard_pg18_one_time_export_ok")

	// 4) 批次行只能打一次导出标记，不能删、不能改；码的批次外键生效。
	for name, stmt := range map[string]string{
		"clear export mark": `UPDATE gift_card_batches SET exported_at = NULL, exported_by = NULL WHERE id = $1::uuid`,
		"rewrite count":     `UPDATE gift_card_batches SET count = count + 1 WHERE id = $1::uuid`,
		"delete batch":      `DELETE FROM gift_card_batches WHERE id = $1::uuid`,
	} {
		err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, gen.BatchID)
			return err
		})
		if state := giftcardPG18SQLState(err); state != "23514" && state != "42501" {
			t.Fatalf("%s SQLSTATE=%q err=%v, want guard or privilege rejection", name, state, err)
		}
	}
	orphanErr := app.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO gift_card_codes (tenant_id, template_id, code, batch_id)
			VALUES ($1, $2, 'ORPHANABCDEFGHJK', gen_random_uuid())`, tenantID, tmplID)
		return err
	})
	if state := giftcardPG18SQLState(orphanErr); state != "23503" {
		t.Fatalf("orphan batch_id SQLSTATE=%q err=%v, want foreign key violation", state, orphanErr)
	}
	t.Log("marker=giftcard_pg18_batch_guards_ok")
}

// giftcardPG18BackfillSQL 从迁移文件里取出回填语句原文：测的是真正会上线的那条 SQL。
func giftcardPG18BackfillSQL(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00069_gift_card_batches.sql"))
	if err != nil {
		t.Fatalf("read migration 00069: %v", err)
	}
	src := string(body)
	start := strings.Index(src, "INSERT INTO gift_card_batches\n")
	if start < 0 {
		t.Fatal("migration 00069 backfill statement not found")
	}
	end := strings.Index(src[start:], ";\n")
	if end < 0 {
		t.Fatal("migration 00069 backfill statement is not terminated")
	}
	return src[start : start+end]
}

func isNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == httpx.CodeNotFound
}

func giftcardPG18SQLState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// openGiftcardPG18 连上 run-pg18-gates.sh 为 giftcard 域准备的一次性库，逐项
// 证明它确实是那一次性库（库名、标记、注释、run ID）、应用连接确实是受 RLS
// 约束的 aegis_app。admin 是单连接：回填前要在同一会话里切 replica 角色。
func openGiftcardPG18(t *testing.T) (context.Context, *pgx.Conn, *platformdb.Pool) {
	t.Helper()
	fixture := strings.TrimSpace(os.Getenv("AEGIS_GIFTCARD_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_GIFTCARD_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_GIFTCARD_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_GIFTCARD_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_GIFTCARD_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("giftcard PostgreSQL 18 fixture is not configured")
	}
	if fixture != giftcardPG18Fixture || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_giftcard_") ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{15,80}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", expectedDatabase, runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture administrator connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open real aegis_app pool: %v", err)
	}
	t.Cleanup(app.Close)

	identity := func(query interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}) string {
		var database, marker, comment string
		var version int
		if err := query.QueryRow(ctx, `
			SELECT current_database(), current_setting('server_version_num')::int,
			       (SELECT run_id FROM public.pandora_giftcard_test_marker),
			       shobj_description(oid, 'pg_database')
			  FROM pg_database WHERE datname = current_database()`).Scan(
			&database, &version, &marker, &comment); err != nil {
			t.Fatalf("identify PostgreSQL fixture: %v", err)
		}
		if database != expectedDatabase || version/10000 != 18 || marker != runID ||
			comment != "pandora-giftcard-pg18:"+runID {
			t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q comment=%q",
				database, version, marker, comment)
		}
		return database + "/" + marker
	}
	if identity(admin) != identity(app) {
		t.Fatal("refusing mismatched application fixture")
	}
	var role, rowSecurity string
	var super, bypass bool
	if err := app.QueryRow(ctx, `SELECT current_user, r.rolsuper, r.rolbypassrls,
			current_setting('row_security')
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&role, &super, &bypass, &rowSecurity); err != nil {
		t.Fatalf("prove application role: %v", err)
	}
	if role != "aegis_app" || super || bypass || rowSecurity != "on" {
		t.Fatalf("unsafe application role %q super=%v bypass=%v row_security=%q",
			role, super, bypass, rowSecurity)
	}
	return ctx, admin, app
}
