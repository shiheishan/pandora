// [INPUT]: 依赖 catalog.go 的 CreatePlan / UpdatePlan / GetPlan、service.go 的 ListPlans、两个向导用例，依赖 catalog_sales_pg18_test.go 的 openCatalogSalesPG18 与 catalog_sales_capability_test.go 的 staticSalesCapability
// [OUTPUT]: 对外提供 TestPlanHighlightsPG18（run-pg18-gates.sh 的 catalog_sales 域）
// [POS]: adminops 卖点与推荐（R100，迁移 00088）的 PG18 门禁：四个写入口的写入语义、两个后台读模型、422 不落库与数据库兜底约束
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"errors"
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestPlanHighlightsPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)
	const (
		tenant = "8c000000-0000-7000-8000-000000000201"
		actor  = "8c000000-0000-7000-8000-000000000211"
	)
	for _, sql := range []string{
		`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ('` + tenant + `', 'highlights-r100-pg18', 'Highlights R100', 'CNY')`,
		`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ('` + actor + `', '` + tenant + `', 'highlights-r100@example.test', 'Highlights', 'active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	svc := NewService(app, staticSalesCapability(true))
	stored := func(planID string) ([]string, bool) {
		t.Helper()
		var hl []string
		var rec bool
		if err := admin.QueryRow(ctx, `SELECT highlights, recommended FROM plans WHERE id=$1`, planID).Scan(&hl, &rec); err != nil {
			t.Fatal(err)
		}
		return hl, rec
	}
	expect := func(planID string, wantHL []string, wantRec bool) {
		t.Helper()
		if hl, rec := stored(planID); !slices.Equal(hl, wantHL) || rec != wantRec {
			t.Fatalf("stored highlights=%q recommended=%v want %q %v", hl, rec, wantHL, wantRec)
		}
	}

	// 1) 只建壳：卖点去首尾空白、保序，推荐落库；详情与列表都带出来。
	detail, err := svc.CreatePlan(ctx, tenant, CreatePlanInput{ActorID: actor, Code: "hl-shell", Name: "HL Shell",
		Highlights: []string{"  高速专线 ", "不限设备"}, Recommended: true})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	shell := detail.ID
	expect(shell, []string{"高速专线", "不限设备"}, true)
	if !slices.Equal(detail.Highlights, []string{"高速专线", "不限设备"}) || !detail.Recommended {
		t.Fatalf("detail highlights=%q recommended=%v", detail.Highlights, detail.Recommended)
	}
	list, err := svc.ListPlans(ctx, tenant)
	if err != nil || len(list) != 1 || !slices.Equal(list[0].Highlights, []string{"高速专线", "不限设备"}) || !list[0].Recommended {
		t.Fatalf("list=%+v err=%v", list, err)
	}

	// 2) 缺省新建：空列表（不是 NULL）与 false。
	plain, err := svc.CreatePlan(ctx, tenant, CreatePlanInput{ActorID: actor, Code: "hl-plain", Name: "HL Plain"})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	expect(plain.ID, []string{}, false)
	if plain.Highlights == nil {
		t.Fatal("detail highlights must be [] not null")
	}

	// 3) 改资料整体覆盖：给空列表与 false 就清掉；非法值 422 且不落库。
	rv := detail.RowVersion
	if rv, err = svc.UpdatePlan(ctx, tenant, shell, UpdatePlanInput{ActorID: actor, ExpectedRowVersion: rv,
		Code: "hl-shell", Name: "HL Shell", Visibility: "public", AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true,
		Highlights: []string{"一条"}, Recommended: false}); err != nil {
		t.Fatalf("update plan: %v", err)
	}
	expect(shell, []string{"一条"}, false)
	_, err = svc.UpdatePlan(ctx, tenant, shell, UpdatePlanInput{ActorID: actor, ExpectedRowVersion: rv,
		Code: "hl-shell", Name: "HL Shell", Visibility: "public", AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true,
		Highlights: []string{"1", "2", "3", "4", "5", "6"}, Recommended: true})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["highlights"] == "" {
		t.Fatalf("six highlights err=%v", err)
	}
	expect(shell, []string{"一条"}, false)

	// 4) 向导一次改完：缺省不动；给了就替换；推荐单独给也只改推荐。
	update := func(hl *[]string, rec *bool) {
		t.Helper()
		var cur int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM plans WHERE id=$1`, shell).Scan(&cur); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UpdatePlanComplete(ctx, tenant, shell, UpdatePlanCompleteInput{ActorID: actor, ExpectedRowVersion: cur,
			Code: "hl-shell", Name: "HL Shell", Visibility: "public", Highlights: hl, Recommended: rec}); err != nil {
			t.Fatalf("wizard update: %v", err)
		}
	}
	update(nil, nil)
	expect(shell, []string{"一条"}, false)
	yes := true
	update(nil, &yes)
	expect(shell, []string{"一条"}, true)
	two := []string{" 甲 ", "乙"}
	update(&two, nil)
	expect(shell, []string{"甲", "乙"}, true)

	// 5) 向导一次建成：可选字段照写。
	created, err := svc.CreatePlanComplete(ctx, tenant, CreatePlanCompleteInput{ActorID: actor, Code: "hl-wizard", Name: "HL Wizard",
		Highlights: []string{"赠送流量"}, Recommended: true})
	if err != nil {
		t.Fatalf("wizard create: %v", err)
	}
	expect(created.Plan.ID, []string{"赠送流量"}, true)

	// 6) 数据库兜底：绕过应用写第 6 条或 NULL 元素都被约束拒绝。
	for _, bad := range []string{
		`UPDATE plans SET highlights = ARRAY['1','2','3','4','5','6'] WHERE id='` + shell + `'`,
		`UPDATE plans SET highlights = ARRAY['a', NULL] WHERE id='` + shell + `'`,
	} {
		if _, err := admin.Exec(ctx, bad); err == nil {
			t.Fatalf("constraint let through: %s", bad)
		}
	}
}
