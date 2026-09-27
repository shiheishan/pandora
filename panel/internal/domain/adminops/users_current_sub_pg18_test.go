// [INPUT]: 依赖 platform/pg18test 打开 catalog_sales 域的一次性库，依赖 users.go 的 ListUsers、bulk_users.go 的 PreviewBulk / ExportUsers、service.go 的 ListPlans
// [OUTPUT]: 对外提供 TestCurrentSubscriptionContractPG18
// [POS]: domain/adminops 的 PG18 测试（契约后台-03 订阅态口径 R118）：宽限期、欠费、只有过期、两条在用、别的租户五种用户在列表 active_plan 与 current_subscription、sub_state、批量 has_active_sub、导出订阅数与套餐列表 active_subscriptions 上口径一致
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"sort"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestCurrentSubscriptionContractPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant  = "7a118000-0000-4000-8000-000000000001"
		other   = "7a118000-0000-4000-8000-000000000002"
		planPro = "7a118000-0000-4000-8000-000000000004"
		planOld = "7a118000-0000-4000-8000-000000000005"
		planMax = "7a118000-0000-4000-8000-000000000006"
		planOut = "7a118000-0000-4000-8000-000000000007"
		grace   = "7a118000-0000-4000-8000-000000000011" // 宽限期，另有一条更新创建的已过期订阅
		pastDue = "7a118000-0000-4000-8000-000000000012" // 欠费中
		expired = "7a118000-0000-4000-8000-000000000013" // 只有过期订阅，另一租户里同 user_id 有在用订阅
		twoLive = "7a118000-0000-4000-8000-000000000014" // 两条在用：新建的先到期，老的后到期
		never   = "7a118000-0000-4000-8000-000000000015" // 没有订阅
		subGr   = "7a118000-0000-4000-8000-000000000021"
		subPd   = "7a118000-0000-4000-8000-000000000022"
		subEx   = "7a118000-0000-4000-8000-000000000023"
		subLate = "7a118000-0000-4000-8000-000000000024"
	)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'r118-current','R118','CNY'),($2,'r118-other','Other','CNY')`,
			[]any{tenant, other}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
			($2,$1,'grace@r118.invalid','Grace','active'),($3,$1,'pastdue@r118.invalid','PastDue','active'),
			($4,$1,'expired@r118.invalid','Expired','active'),($5,$1,'twolive@r118.invalid','TwoLive','active'),
			($6,$1,'never@r118.invalid','Never','active')`, []any{tenant, grace, pastDue, expired, twoLive, never}},
		// 一个产品只挂一个套餐（plans_tenant_product_unique），套餐 id 兼作产品 id
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES
			($2,$1,'r118-pro','R118 Pro','active'),($3,$1,'r118-old','R118 Old','active'),
			($4,$1,'r118-max','R118 Max','active'),($5,$6,'r118-out','R118 Out','active')`,
			[]any{tenant, planPro, planOld, planMax, planOut, other}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES
			($2,$1,$2,'pro','Pro 月付','active'),($3,$1,$3,'old','旧套餐','active'),
			($4,$1,$4,'max','Max 年付','active'),($5,$6,$5,'out','别家套餐','active')`,
			[]any{tenant, planPro, planOld, planMax, planOut, other}},
		{`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
			current_period_end,created_at) VALUES
			($2,$1,$3,$4,gen_random_uuid(),'grace','CNY',0,now()+interval '2 days',now()-interval '40 days'),
			(gen_random_uuid(),$1,$3,$5,gen_random_uuid(),'expired','CNY',0,now()-interval '1 day',now()-interval '1 day'),
			($6,$1,$7,$4,gen_random_uuid(),'past_due','CNY',0,now()+interval '1 day',now()-interval '29 days'),
			($8,$1,$9,$5,gen_random_uuid(),'expired','CNY',0,now()-interval '3 days',now()-interval '33 days'),
			(gen_random_uuid(),$10,$9,$11,gen_random_uuid(),'active','CNY',0,now()+interval '300 days',now()),
			(gen_random_uuid(),$1,$12,$4,gen_random_uuid(),'active','CNY',0,now()+interval '10 days',now()-interval '1 day'),
			($13,$1,$12,$14,gen_random_uuid(),'trialing','CNY',0,now()+interval '40 days',now()-interval '20 days')`,
			[]any{tenant, subGr, grace, planPro, planOld, subPd, pastDue, subEx, expired, other, planOut, twoLive, subLate, planMax}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	svc := NewService(app)
	rows, _, err := svc.ListUsers(ctx, tenant, ListUsersInput{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]UserRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	// 在用时 active_plan 与 current_subscription 指向同一条
	for user, want := range map[string]struct{ sub, plan, status string }{
		grace:   {subGr, "Pro 月付", "grace"},
		pastDue: {subPd, "Pro 月付", "past_due"},
		twoLive: {subLate, "Max 年付", "trialing"},
	} {
		r := byID[user]
		cs := r.CurrentSubscription
		if cs == nil || cs.ID != want.sub || cs.Status != want.status || cs.PlanName != want.plan ||
			r.ActiveSub == nil || *r.ActiveSub != cs.PlanName {
			t.Errorf("%s: active_plan=%v current=%+v, want %s/%s", r.Email, r.ActiveSub, cs, want.plan, want.sub)
		}
	}
	// 只有过期订阅：active_plan 为 null，当前订阅就是那条过期的；别的租户的在用订阅不算
	if r := byID[expired]; r.ActiveSub != nil || r.CurrentSubscription == nil || r.CurrentSubscription.ID != subEx ||
		r.CurrentSubscription.Status != "expired" || r.SubCount != 1 {
		t.Errorf("expired-only: active_plan=%v current=%+v sub_count=%d", r.ActiveSub, r.CurrentSubscription, r.SubCount)
	}
	if r := byID[never]; r.ActiveSub != nil || r.CurrentSubscription != nil {
		t.Errorf("never: %+v", r)
	}

	// sub_state=active 与批量 has_active_sub=true 是同一个人群；群发与导出复用同一筛选
	liveWant := []string{"grace@r118.invalid", "pastdue@r118.invalid", "twolive@r118.invalid"}
	emails := func(in ListUsersInput) []string {
		t.Helper()
		in.Limit = 50
		rows, _, err := svc.ListUsers(ctx, tenant, in)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Email)
		}
		sort.Strings(out)
		return out
	}
	yes, no := true, false
	preview := func(f BulkFilter) []string {
		t.Helper()
		p, err := svc.PreviewBulk(ctx, tenant, f)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(p.Samples)
		return p.Samples
	}
	if got := emails(ListUsersInput{SubState: "active"}); !equalStrings(got, liveWant) {
		t.Errorf("sub_state=active: %v, want %v", got, liveWant)
	}
	if got := preview(BulkFilter{HasActiveSub: &yes}); !equalStrings(got, liveWant) {
		t.Errorf("has_active_sub=true: %v, want %v", got, liveWant)
	}
	if got := preview(BulkFilter{HasActiveSub: &no}); !equalStrings(got, []string{"expired@r118.invalid", "never@r118.invalid"}) {
		t.Errorf("has_active_sub=false: %v", got)
	}

	// 导出的订阅数按在用计：两条在用的算 2，宽限期里带一条过期的算 1
	export, err := svc.ExportUsers(ctx, tenant, BulkFilter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range export {
		counts[r.Email] = r.ActiveSubs
	}
	for email, want := range map[string]int{"grace@r118.invalid": 1, "pastdue@r118.invalid": 1, "twolive@r118.invalid": 2,
		"expired@r118.invalid": 0, "never@r118.invalid": 0} {
		if counts[email] != want {
			t.Errorf("export %s live subscriptions = %d, want %d", email, counts[email], want)
		}
	}

	// 套餐列表的 active_subscriptions 按在用计：Pro 有宽限期、欠费、在用各一条，
	// 旧套餐只有过期的，别的租户的套餐不出现
	plans, err := svc.ListPlans(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]int{}
	for _, p := range plans {
		live[p.ID] = p.ActiveSubs
	}
	if len(plans) != 3 || live[planPro] != 3 || live[planOld] != 0 || live[planMax] != 1 {
		t.Errorf("plan active_subscriptions = %v, want pro 3 / old 0 / max 1 across 3 plans", live)
	}
}
