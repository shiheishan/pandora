// [INPUT]: 依赖 platform/pg18test 打开 catalog_sales 域的一次性库，依赖 service.go 的 ListUsers / GetUser / ListOrders
// [OUTPUT]: 对外提供 TestAdminUsersPG18
// [POS]: domain/adminops 的 PG18 测试：用户列表带出用户组名（缺陷 8），用户详情的 recent_orders 带出首项套餐快照与项数且与订单列表同形（缺陷 9）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestAdminUsersPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})

	const (
		tenant  = "7a000000-0000-4000-8000-000000000001"
		group   = "7a000000-0000-4000-8000-000000000002"
		member  = "7a000000-0000-4000-8000-000000000011"
		loner   = "7a000000-0000-4000-8000-000000000012"
		orderID = "7a000000-0000-4000-8000-000000000021"
	)
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'admin-users-pg18','Admin Users PG18','CNY')`, []any{tenant}},
		{`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'vip','贵宾组')`, []any{tenant, group}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id) VALUES($2,$1,'member@admin-users.invalid','Member','active',$3)`, []any{tenant, member, group}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'loner@admin-users.invalid','Loner','active')`, []any{tenant, loner}},
	} {
		if _, err := admin.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed admin users fixture: %v\nSQL: %s", err, row.sql)
		}
	}
	plantTwoItemOrder(t, ctx, admin, tenant, member, orderID)

	svc := NewService(app)

	// 缺陷 8：列表里的用户组名
	rows, total, err := svc.ListUsers(ctx, tenant, ListUsersInput{Limit: 10})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("list users rows=%d total=%d err=%v", len(rows), total, err)
	}
	groups := map[string]string{}
	for _, r := range rows {
		groups[r.ID] = r.GroupName
	}
	if groups[member] != "贵宾组" || groups[loner] != "" {
		t.Fatalf("group names = %v", groups)
	}

	// 缺陷 9：详情里的最近订单带首项快照与项数
	detail, err := svc.GetUser(ctx, tenant, member)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if detail.GroupName != "贵宾组" || len(detail.Orders) != 1 {
		t.Fatalf("detail group=%q orders=%d", detail.GroupName, len(detail.Orders))
	}
	got := detail.Orders[0]
	if got.PlanName != "首项套餐" || got.Interval != "month" || got.IntervalCount != 3 ||
		got.ItemCount != 2 || got.UserEmail != "member@admin-users.invalid" {
		t.Fatalf("recent order = %+v", got)
	}
	// 与订单列表是同一份查询：同一张订单两处看到的必须逐字段一致
	listed, _, err := svc.ListOrders(ctx, tenant, ListOrdersInput{Query: "ADMIN-USERS-PG18", Limit: 10})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list orders rows=%d err=%v", len(listed), err)
	}
	if !reflect.DeepEqual(listed[0], got) {
		t.Fatalf("user detail order differs from order list\ndetail: %+v\nlist:   %+v", got, listed[0])
	}
}

// plantTwoItemOrder 直接写一张两项的订单。
//
// 走下单主路径要带齐幂等键、预留、价格版本那一整套，与这里要验的「读出来的
// 形状」无关；这个一次性库里用 replica 模式跳过触发器与外键，只写读路径
// 需要的列。CHECK 约束照常生效。
func plantTwoItemOrder(t *testing.T, ctx context.Context, admin interface {
	Begin(context.Context) (pgx.Tx, error)
}, tenant, user, orderID string) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO orders
		   (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
		    discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
		    expires_at,business_request_id)
		 VALUES ('` + orderID + `','` + tenant + `','ADMIN-USERS-PG18','` + user + `','new','pending_payment',
		    'CNY',3000,0,0,3000,0,3000,now()+interval '30 minutes',gen_random_uuid())`,
		`INSERT INTO order_items
		   (tenant_id,order_id,product_id,price_id,plan_id,plan_version_id,
		    snapshot_product_name,snapshot_plan_name,snapshot_plan_version,
		    snapshot_interval,snapshot_interval_count,quantity,unit_amount,line_amount,currency,created_at)
		 VALUES
		   ('` + tenant + `','` + orderID + `',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
		    '首项产品','首项套餐',1,'month',3,1,2000,2000,'CNY',now()-interval '1 second'),
		   ('` + tenant + `','` + orderID + `',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
		    '次项产品','次项套餐',1,'month',1,1,1000,1000,'CNY',now())`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("plant order: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit planted order: %v", err)
	}
}
