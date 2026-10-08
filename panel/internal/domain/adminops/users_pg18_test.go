package adminops

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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

	t.Run("subscription_labels_and_packs", func(t *testing.T) {
		userDetailSubscriptionPacks(t, ctx, admin, svc, tenant, loner)
	})
}

// userDetailSubscriptionPacks 钉住购买模型统一后后台用户详情的订阅卡字段（D 路按这两个名字解析）：
// 每份带备注名 label（没起为 null）与挂在这一份上的流量包余量 pack_remaining_bytes，
// 顶层带还没加到任何一份的余量 unattached_pack_bytes。
func userDetailSubscriptionPacks(t *testing.T, ctx context.Context, admin interface {
	Begin(context.Context) (pgx.Tx, error)
}, svc *Service, tenant, user string) {
	const (
		plan    = "7a000000-0000-4000-8000-000000000031"
		planVer = "7a000000-0000-4000-8000-000000000032"
		named   = "7a000000-0000-4000-8000-000000000041"
		plain   = "7a000000-0000-4000-8000-000000000042"
	)
	// 连接池上的 SET 不跨连接：整段夹具放进一个事务，用 SET LOCAL
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		// 套餐背后的产品与本测试无关：replica 模式跳过外键，只写读路径要的列
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES('` + plan + `','` + tenant + `',gen_random_uuid(),'detail-plan','基础版','active')`,
		`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES('` + planVer + `','` + tenant + `','` + plan + `',1)`,
		`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end,label,created_at)
		 VALUES('` + named + `','` + tenant + `','` + user + `','` + plan + `','` + planVer + `','active','CNY',0,now()+interval '20 days','妈妈的 iPad',now()-interval '1 day'),
		       ('` + plain + `','` + tenant + `','` + user + `','` + plan + `','` + planVer + `','active','CNY',0,now()+interval '20 days',NULL,now())`,
		`SET LOCAL session_replication_role = origin`,
		// named 上 100 用了 40、另一笔已用光；plain 上没有；另有 25 还没加到任何一份
		`INSERT INTO traffic_pack_grants(tenant_id,user_id,subscription_id,source,source_id,granted_bytes,consumed_bytes) VALUES
		   ('` + tenant + `','` + user + `','` + named + `','migration',gen_random_uuid(),100,40),
		   ('` + tenant + `','` + user + `','` + named + `','migration',gen_random_uuid(),50,50),
		   ('` + tenant + `','` + user + `',NULL,'migration',gen_random_uuid(),25,0)`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	d, err := svc.GetUser(ctx, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Subscriptions) != 2 || d.Subscriptions[0].ID != plain || d.Subscriptions[1].ID != named ||
		d.Subscriptions[0].Label != nil || d.Subscriptions[0].PackRemainingBytes != 0 ||
		d.Subscriptions[1].Label == nil || *d.Subscriptions[1].Label != "妈妈的 iPad" ||
		d.Subscriptions[1].PackRemainingBytes != 60 || d.UnattachedPackBytes != 25 {
		t.Fatalf("user detail subscriptions=%+v unattached=%d", d.Subscriptions, d.UnattachedPackBytes)
	}
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"label":null`, `"label":"妈妈的 iPad"`, `"pack_remaining_bytes":60`, `"unattached_pack_bytes":25`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("user detail JSON lacks %s: %s", want, body)
		}
	}
	t.Log("marker=catalog_sales_pg18_user_detail_packs_per_subscription_ok")
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
