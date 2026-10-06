// [INPUT]: 依赖 platform/pg18test 打开 support 域的一次性库，依赖 user_tickets.go 的 Create、agent_tickets.go 的 ListForAgent / GetForAgent，依赖 domain/adminops 的 ListUsers 作对照
// [OUTPUT]: 对外提供 TestTicketUserActivePlanMatchesUserListPG18
// [POS]: domain/support 的 PG18 测试（契约后台-02 / 后台-03 订阅态口径 R118）：工单队列与详情的 user_active_plan 与后台用户列表的 active_plan 逐人一致（宽限期、只有过期、另一租户的订阅）

package support

import (
	"testing"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestTicketUserActivePlanMatchesUserListPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SUPPORT", DatabasePrefix: "pandora_node_preview_",
		MarkerTable: "pandora_support_test_marker", CommentTag: "pandora-node-preview-pg18",
	})
	const (
		tenant  = "79118000-0000-4000-8000-000000000001"
		other   = "79118000-0000-4000-8000-000000000002"
		planPro = "79118000-0000-4000-8000-000000000004"
		planOld = "79118000-0000-4000-8000-000000000005"
		planOut = "79118000-0000-4000-8000-000000000006"
		grace   = "79118000-0000-4000-8000-000000000011" // 宽限期，另有一条更新创建的已过期订阅
		expired = "79118000-0000-4000-8000-000000000012" // 只有过期订阅，另一租户里同 user_id 有在用订阅
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
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'r118-tickets','R118 Tickets','CNY'),($2,'r118-tickets-other','Other','CNY')`,
			[]any{tenant, other}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
			($2,$1,'grace@r118-tickets.invalid','Grace','active'),($3,$1,'expired@r118-tickets.invalid','Expired','active')`,
			[]any{tenant, grace, expired}},
		// 一个产品只挂一个套餐（plans_tenant_product_unique），套餐 id 兼作产品 id
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES
			($2,$1,'r118t-pro','R118 Pro','active'),($3,$1,'r118t-old','R118 Old','active'),($4,$5,'r118t-out','R118 Out','active')`,
			[]any{tenant, planPro, planOld, planOut, other}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES
			($2,$1,$2,'pro','Pro 月付','active'),($3,$1,$3,'old','旧套餐','active'),($4,$5,$4,'out','别家套餐','active')`,
			[]any{tenant, planPro, planOld, planOut, other}},
		{`INSERT INTO subscriptions(tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
			current_period_end,created_at) VALUES
			($1,$2,$3,gen_random_uuid(),'grace','CNY',0,now()+interval '2 days',now()-interval '40 days'),
			($1,$2,$4,gen_random_uuid(),'expired','CNY',0,now()-interval '1 day',now()-interval '1 day'),
			($1,$5,$4,gen_random_uuid(),'expired','CNY',0,now()-interval '3 days',now()-interval '33 days'),
			($6,$5,$7,gen_random_uuid(),'active','CNY',0,now()+interval '300 days',now())`,
			[]any{tenant, grace, planPro, planOld, expired, other, planOut}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	users, _, err := adminops.NewService(app).ListUsers(ctx, tenant, adminops.ListUsersInput{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	listPlan := map[string]*string{}
	for _, u := range users {
		listPlan[u.ID] = u.ActiveSub
	}
	if p := listPlan[grace]; p == nil || *p != "Pro 月付" {
		t.Fatalf("user list active_plan for grace = %v", p)
	}
	if listPlan[expired] != nil {
		t.Fatalf("user list active_plan for expired-only = %v", *listPlan[expired])
	}

	svc := NewService(app)
	ticketOf := map[string]string{}
	for _, user := range []string{grace, expired} {
		tk, err := svc.Create(ctx, tenant, CreateInput{UserID: user, Body: "节点连不上了，麻烦帮忙看一下具体原因。"})
		if err != nil {
			t.Fatalf("create ticket: %v", err)
		}
		ticketOf[user] = tk.ID
	}
	queue, _, err := svc.ListForAgent(ctx, tenant, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	queuePlan := map[string]*string{} // 工单 id → 队列行的 user_active_plan
	for _, tk := range queue {
		queuePlan[tk.ID] = tk.UserActivePlan
	}
	same := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, user := range []string{grace, expired} {
		detail, err := svc.GetForAgent(ctx, tenant, ticketOf[user])
		if err != nil {
			t.Fatal(err)
		}
		if len(queue) != 2 || !same(detail.UserActivePlan, listPlan[user]) || !same(queuePlan[detail.ID], listPlan[user]) {
			t.Errorf("user %s: detail=%v queue=%v, user list=%v", user, detail.UserActivePlan, queuePlan[detail.ID], listPlan[user])
		}
	}
}
