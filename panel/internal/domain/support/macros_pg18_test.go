// [INPUT]: 依赖 platform/pg18test 打开 support 域的一次性库，依赖 macros.go 的 ListMacros / SaveMacro / DeleteMacro
// [OUTPUT]: 对外提供 TestTicketMacrosPG18
// [POS]: domain/support 的 PG18 测试：快捷回复增改删、排序、租户隔离、审计与 00078 的长度约束
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestTicketMacrosPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SUPPORT", DatabasePrefix: "pandora_node_preview_",
		MarkerTable: "pandora_support_test_marker", CommentTag: "pandora-node-preview-pg18",
	})
	const (
		tenantA = "80000000-0000-4000-8000-000000000001"
		tenantB = "80000000-0000-4000-8000-000000000002"
		agentA  = "80000000-0000-4000-8000-000000000011"
		agentB  = "80000000-0000-4000-8000-000000000012"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenantA + `','macros-a','Macros A','USD'),('` + tenantB + `','macros-b','Macros B','USD')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + agentA + `','` + tenantA + `','agent@macros-a.invalid','Agent A','active')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + agentB + `','` + tenantB + `','agent@macros-b.invalid','Agent B','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed macros fixture: %v", err)
		}
	}
	svc := NewService(app)
	isCode := func(err error, code httpx.Code) bool {
		var httpErr *httpx.Error
		return errors.As(err, &httpErr) && httpErr.Code == code
	}

	restart, err := svc.SaveMacro(ctx, tenantA, agentA, "", MacroInput{Title: "重启客户端", Body: "请先完全退出客户端再重新打开。", SortOrder: 2})
	if err != nil {
		t.Fatalf("create macro: %v", err)
	}
	refund, err := svc.SaveMacro(ctx, tenantA, agentA, "", MacroInput{Title: "退款说明", Body: "退款将在 3 个工作日内原路退回。", SortOrder: 1})
	if err != nil {
		t.Fatalf("create second macro: %v", err)
	}
	macros, err := svc.ListMacros(ctx, tenantA)
	if err != nil || len(macros) != 2 || macros[0].ID != refund || macros[1].ID != restart {
		t.Fatalf("list order by sort_order: %+v err=%v", macros, err)
	}

	// 编辑：覆盖三列；另一个租户的客服看不到也改不了
	if _, err := svc.SaveMacro(ctx, tenantA, agentA, restart, MacroInput{Title: "重启", Body: "请重启设备后再试。", SortOrder: 0}); err != nil {
		t.Fatalf("update macro: %v", err)
	}
	if macros, _ := svc.ListMacros(ctx, tenantA); len(macros) != 2 || macros[0].ID != restart || macros[0].Body != "请重启设备后再试。" {
		t.Fatalf("update not visible: %+v", macros)
	}
	if other, _ := svc.ListMacros(ctx, tenantB); len(other) != 0 {
		t.Fatalf("tenant B sees tenant A macros: %+v", other)
	}
	if _, err := svc.SaveMacro(ctx, tenantB, agentB, restart, MacroInput{Title: "x", Body: "y"}); !isCode(err, httpx.CodeNotFound) {
		t.Fatalf("cross-tenant update: %v, want 404", err)
	}
	if err := svc.DeleteMacro(ctx, tenantB, agentB, restart); !isCode(err, httpx.CodeNotFound) {
		t.Fatalf("cross-tenant delete: %v, want 404", err)
	}
	if _, err := svc.SaveMacro(ctx, tenantA, agentA, "not-a-uuid", MacroInput{Title: "x", Body: "y"}); !isCode(err, httpx.CodeNotFound) {
		t.Fatalf("malformed id: %v, want 404", err)
	}
	if _, err := svc.SaveMacro(ctx, tenantA, agentA, "", MacroInput{Title: strings.Repeat("长", 21), Body: "y"}); !isCode(err, httpx.CodeValidationFailed) {
		t.Fatalf("long title: %v, want 422", err)
	}

	if err := svc.DeleteMacro(ctx, tenantA, agentA, refund); err != nil {
		t.Fatalf("delete macro: %v", err)
	}
	if err := svc.DeleteMacro(ctx, tenantA, agentA, refund); !isCode(err, httpx.CodeNotFound) {
		t.Fatalf("second delete: %v, want 404", err)
	}
	if macros, _ := svc.ListMacros(ctx, tenantA); len(macros) != 1 || macros[0].ID != restart {
		t.Fatalf("after delete: %+v", macros)
	}

	var saved, deleted int
	if err := admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='ticket_macro.saved'),
		count(*) FILTER (WHERE action='ticket_macro.deleted') FROM audit_events WHERE tenant_id=$1`, tenantA).
		Scan(&saved, &deleted); err != nil || saved != 3 || deleted != 1 {
		t.Fatalf("audit saved=%d deleted=%d err=%v, want 3/1", saved, deleted, err)
	}
	// 约束落在库里：绕过 Go 校验直接写也过不去
	if _, err := admin.Exec(ctx, `INSERT INTO ticket_macros(tenant_id,title,body) VALUES($1,$2,'x')`,
		tenantA, strings.Repeat("长", 21)); err == nil {
		t.Fatal("ticket_macros accepted a 21-character title")
	}
}
