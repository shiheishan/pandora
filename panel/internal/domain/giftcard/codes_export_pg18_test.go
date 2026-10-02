// [INPUT]: 依赖 codes_export.go 的 ExportCodes / CodesExportMax、codes.go 的 ListCodes 与 CodeFilter、giftcard.go 的 GenerateCodes、batches.go 的 ExportBatch / MaskCode，依赖 batches_pg18_test.go 的 openGiftcardPG18 夹具
// [OUTPUT]: 对外提供 TestGiftCardCodesReportPG18（run-pg18-gates.sh 的 giftcard 域）
// [POS]: giftcard 掩码报表导出的 PG18 集成门禁：与列表同筛选同行数、SQL 掩码与 MaskCode 逐行一致、任何明文都不出现、兑换人与时间、行数上限 422、每次导出一条不含码的审计、租户隔离；不影响批次的一次性明文导出
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestGiftCardCodesReportPG18(t *testing.T) {
	ctx, admin, app := openGiftcardPG18(t)

	const (
		tenantID = "74200000-0000-7000-8000-000000000001"
		decoyID  = "74200000-0000-7000-8000-000000000002"
		actorID  = "74200000-0000-7000-8000-000000000011"
		userID   = "74200000-0000-7000-8000-000000000012"
		tmplA    = "74200000-0000-7000-8000-000000000021"
		tmplB    = "74200000-0000-7000-8000-000000000022"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES
		($1, 'giftcard-report-pg18', 'Giftcard Report PG18', 'CNY'),
		($2, 'giftcard-report-decoy', 'Giftcard Report Decoy', 'CNY')`, tenantID, decoyID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES
		($1, $3, 'giftcard-report-admin@example.test', 'Report Admin', 'active'),
		($2, $3, 'giftcard-report-user@example.test', 'Report User', 'active')`, actorID, userID, tenantID)
	must(`INSERT INTO gift_card_templates (id, tenant_id, name, type, rewards) VALUES
		($1, $3, '对账甲', 'general', '{"balance":100}'::jsonb),
		($2, $3, '对账乙', 'general', '{"balance":200}'::jsonb)`, tmplA, tmplB, tenantID)

	svc := New(app, nil, nil)
	genA, err := svc.GenerateCodes(ctx, tenantID, GenerateInput{TemplateID: tmplA, Count: 5, Prefix: "RA", ActorID: actorID})
	if err != nil {
		t.Fatalf("generate A: %v", err)
	}
	genB, err := svc.GenerateCodes(ctx, tenantID, GenerateInput{TemplateID: tmplB, Count: 3, Prefix: "RB", ActorID: actorID})
	if err != nil {
		t.Fatalf("generate B: %v", err)
	}
	// 拿到全部明文（批次一次性导出），用来证明报表里一张都不出现
	plain := map[string]bool{}
	for _, id := range []string{genA.BatchID, genB.BatchID} {
		exp, err := svc.ExportBatch(ctx, tenantID, id, actorID)
		if err != nil {
			t.Fatalf("export batch %s: %v", id, err)
		}
		for _, r := range exp.Rows {
			plain[r.Code] = true
		}
	}
	if len(plain) != 8 {
		t.Fatalf("plaintext codes = %d, want 8", len(plain))
	}
	// 一张 A 卡被兑换：直接改库造出 used 状态（兑换流程不是这里要测的）
	var usedPlain string
	for code := range plain {
		if strings.HasPrefix(code, "RA") {
			usedPlain = code
			break
		}
	}
	must(`UPDATE gift_card_codes SET status='used', used_by=$3, used_at=now()
		WHERE tenant_id=$1 AND code=$2`, tenantID, usedPlain, userID)

	// 1) 不带筛选：全部 8 行，掩码与 MaskCode 逐行一致，明文一张都不出现
	rows, err := svc.ExportCodes(ctx, tenantID, actorID, CodeFilter{})
	if err != nil || len(rows) != 8 {
		t.Fatalf("export all rows=%d err=%v", len(rows), err)
	}
	masks := map[string]bool{}
	for code := range plain {
		masks[MaskCode(code)] = true
	}
	for _, r := range rows {
		if !masks[r.CodeMasked] {
			t.Fatalf("report mask %q does not match MaskCode of any generated code", r.CodeMasked)
		}
		if plain[r.CodeMasked] || !strings.HasSuffix(r.CodeMasked, "••••••••") {
			t.Fatalf("report leaked plaintext: %q", r.CodeMasked)
		}
		if r.TemplateName != "对账甲" && r.TemplateName != "对账乙" || r.BatchID == nil || r.CreatedAt.IsZero() {
			t.Fatalf("report row lacks template/batch/created: %+v", r)
		}
		if r.CodeMasked == MaskCode(usedPlain) {
			if r.Status != "used" || r.UsedEmail != "giftcard-report-user@example.test" || r.UsedAt == nil {
				t.Fatalf("used row = %+v", r)
			}
		}
	}
	t.Log("marker=giftcard_pg18_codes_report_masked_ok")

	// 2) 与列表同一筛选口径：每种筛选下行数等于列表的 total
	for name, f := range map[string]CodeFilter{
		"模板": {TemplateID: tmplB},
		"批次": {BatchID: genA.BatchID},
		"状态": {Status: "used"},
		"组合": {TemplateID: tmplA, Status: "unused"},
	} {
		got, err := svc.ExportCodes(ctx, tenantID, actorID, f)
		if err != nil {
			t.Fatalf("%s export: %v", name, err)
		}
		_, total, err := svc.ListCodes(ctx, tenantID, ListCodesInput{CodeFilter: f})
		if err != nil || int64(len(got)) != total {
			t.Fatalf("%s: export %d rows, list total %d err=%v", name, len(got), total, err)
		}
	}
	if got, _ := svc.ExportCodes(ctx, tenantID, actorID, CodeFilter{Status: "used"}); len(got) != 1 {
		t.Fatalf("used filter rows=%d want 1", len(got))
	}
	t.Log("marker=giftcard_pg18_codes_report_filter_ok")

	// 3) 租户隔离：别的租户导出是空表
	if got, err := svc.ExportCodes(ctx, decoyID, actorID, CodeFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("decoy tenant export rows=%d err=%v", len(got), err)
	}

	// 4) 每次导出一条审计，记筛选与行数，不含任何码
	var audits int
	var digest string
	if err := admin.QueryRow(ctx, `SELECT count(*)::int, coalesce(string_agg(after_digest::text, ' '), '')
		FROM audit_events WHERE tenant_id = $1 AND action = 'gift_card.codes_report_exported'`,
		tenantID).Scan(&audits, &digest); err != nil {
		t.Fatalf("read report audit: %v", err)
	}
	if audits != 6 || !strings.Contains(digest, `"rows": 8`) || !strings.Contains(digest, tmplB) {
		t.Fatalf("report audits=%d digest=%s", audits, digest)
	}
	for code := range plain {
		if strings.Contains(digest, code) || strings.Contains(digest, MaskCode(code)) {
			t.Fatalf("report audit carries a code: %s", digest)
		}
	}
	t.Log("marker=giftcard_pg18_codes_report_audit_ok")

	// 5) 行数上限：超过 CodesExportMax 回 422，不写审计。用 generate_series 直接灌库，
	// 码由序号拼成、满足大写与长度 CHECK。
	must(`INSERT INTO gift_card_codes (tenant_id, template_id, code, batch_id)
		SELECT $1, $2, 'BULK' || lpad(g::text, 8, '0'), $3
		  FROM generate_series(1, $4::int) g`, tenantID, tmplA, genA.BatchID, CodesExportMax)
	_, err = svc.ExportCodes(ctx, tenantID, actorID, CodeFilter{})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || !strings.Contains(he.Message, "缩小筛选范围") {
		t.Fatalf("over-limit export err=%v, want 422 asking to narrow the filter", err)
	}
	if got, err := svc.ExportCodes(ctx, tenantID, actorID, CodeFilter{TemplateID: tmplB}); err != nil || len(got) != 3 {
		t.Fatalf("narrowed export rows=%d err=%v", len(got), err)
	}
	var after int
	if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM audit_events
		WHERE tenant_id = $1 AND action = 'gift_card.codes_report_exported'`, tenantID).Scan(&after); err != nil || after != 7 {
		t.Fatalf("audits after over-limit=%d err=%v, want 7 (rejected export writes none)", after, err)
	}
	t.Log("marker=giftcard_pg18_codes_report_limit_ok")
}
