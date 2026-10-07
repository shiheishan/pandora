package adminops

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 原后台用户列表 SQL（f364a62 的 users.go 原文，只把局部变量 where 换成常量名），
// 只作 PG18 对照：先取页再拼读模型之后，同一份数据、同一组筛选得到的行与总数必须相同。
var legacyListUsersWhereSQL = `u.tenant_id = $1
		AND ($2 = '' OR lower(u.email) LIKE $2 OR lower(coalesce(u.display_name,'')) LIKE $2
		     OR u.id::text = $3
		     OR EXISTS (SELECT 1 FROM subscription_credentials sc
		                 WHERE sc.tenant_id = u.tenant_id AND sc.user_id = u.id
		                   AND sc.token_hash = $4 AND sc.status IN ('active','grace')))
		AND (cardinality($5::text[]) = 0 OR u.status::text = ANY($5::text[]))
		AND ($6 = '' OR ($6 = 'none' AND u.user_group_id IS NULL) OR u.user_group_id::text = $6)
		AND ($7 = '' OR ` + subStateSQL("$7") + `)`

var legacyListUsersSQL = `
			SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
			       u.created_at, u.last_login_at,
			       (SELECT count(*) FROM subscriptions s WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id),
			       CASE WHEN cs.status IN ` + subscription.LiveStatusesSQL + ` THEN cs.plan_name END,
			       coalesce((SELECT -la.balance_signed FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 0),
			       coalesce((SELECT la.currency FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 'CNY'),
			       coalesce(g.name, ''), u.user_group_id::text,
			       cs.id::text, cs.plan_name, cs.status, cs.current_period_end,
			       tq.limit_value, coalesce(tq.consumed, 0), coalesce(cs.device_limit, 0),
			       coalesce(cs.online_devices, 0)
			  FROM users u
			  LEFT JOIN user_groups g ON g.tenant_id = u.tenant_id AND g.id = u.user_group_id
			  LEFT JOIN LATERAL (
			        SELECT s.id, pl.name AS plan_name, s.status, s.current_period_end,
			               coalesce(s.device_limit, pv.max_devices, 0) AS device_limit,
			               coalesce(od.device_count, 0)::int AS online_devices
			          FROM subscriptions s
			          LEFT JOIN plans pl ON pl.id = s.plan_id
			          LEFT JOIN plan_versions pv ON pv.id = s.plan_version_id
			          LEFT JOIN subscription_online_devices od
			                 ON od.tenant_id = s.tenant_id AND od.subscription_id = s.id
			         WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id
			         ORDER BY ` + subscription.CurrentOrderSQL + `
			         LIMIT 1) cs ON true
			  LEFT JOIN LATERAL (
			        SELECT q.limit_value, q.consumed FROM quota_balances q
			         WHERE q.tenant_id = u.tenant_id AND q.subscription_id = cs.id
			           AND q.metric = 'traffic.bytes'
			         ORDER BY q.period_start DESC LIMIT 1) tq ON true
			 WHERE ` + legacyListUsersWhereSQL + `
			 ORDER BY u.created_at DESC
			 LIMIT $8 OFFSET $9`

// TestAdminUserListMatchesLegacyPG18 钉住后台用户列表改成「先取页再拼读模型」后语义不变：
// 每组筛选（状态、用户组、订阅状态、邮箱片段、id、订阅令牌与整条订阅地址）下，与原 SQL
// 的行（逐字段，含当前订阅、配额、余额、在线设备）与总数相同；排序是 created_at 倒序、
// 同一时刻按 id 倒序，按 4 条一页翻完与一次取全相同、不重不漏。
func TestAdminUserListMatchesLegacyPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant  = "7e000000-0000-4000-8000-000000000001"
		group   = "7e000000-0000-4000-8000-000000000002"
		product = "7e000000-0000-4000-8000-000000000003"
		plan    = "7e000000-0000-4000-8000-000000000004"
		version = "7e000000-0000-4000-8000-000000000005"
		nodeA   = "7e000000-0000-4000-8000-000000000006"
		nodeB   = "7e000000-0000-4000-8000-000000000007"
		token   = "legacy-list-token-7e00000000000000000000"
	)
	userID := func(i int) string { return fmt.Sprintf("7e000000-0000-4000-8000-%012d", 100+i) }
	subID := func(i int) string { return fmt.Sprintf("7e000000-0000-4000-8000-%012d", 200+i) }

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	exec(`SET LOCAL session_replication_role = replica`)
	exec(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'admin-users-legacy','Legacy','CNY')`, tenant)
	exec(`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'gold','黄金组')`, tenant, group)
	exec(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'lp','Legacy Product','active')`, tenant, product)
	exec(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'lp','Legacy 月付','active')`, tenant, product, plan)
	exec(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,max_devices) VALUES($3,$1,$2,1,2)`, tenant, plan, version)
	exec(`INSERT INTO nodes(id,tenant_id,name,status) VALUES($2,$1,'legacy-a','active'),($3,$1,'legacy-b','active')`, tenant, nodeA, nodeB)
	// 12 个用户：前 5 个同一时刻建（批量生成），其余逐小时；状态与分组交错
	statuses := []string{"active", "suspended", "banned"}
	for i := 0; i < 12; i++ {
		var groupID *string
		if i%2 == 0 {
			g := group
			groupID = &g
		}
		created := "now() - interval '1 day'"
		if i >= 5 {
			created = fmt.Sprintf("now() - interval '%d hours'", i)
		}
		exec(`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id,created_at)
		      VALUES($1,$2,$3,$4,$5,$6,`+created+`)`,
			userID(i), tenant, fmt.Sprintf("legacy-user-%d@list.invalid", i), fmt.Sprintf("Legacy %d", i), statuses[i%3], groupID)
	}
	sub := func(i, owner int, status, end string, created string, deviceLimit any) {
		t.Helper()
		exec(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
		        current_period_start,current_period_end,created_at,device_limit)
		      VALUES($1,$2,$3,$4,$5,$6,'CNY',3000,now()-interval '5 days',`+end+`,`+created+`,$7)`,
			subID(i), tenant, userID(owner), plan, version, status, deviceLimit)
	}
	sub(0, 0, "active", "now()+interval '25 days'", "now()-interval '5 days'", 5) // 设备上限覆盖 5
	sub(1, 1, "expired", "now()-interval '1 day'", "now()-interval '40 days'", nil)
	sub(2, 2, "active", "now()+interval '10 days'", "now()-interval '2 days'", nil)
	sub(3, 2, "active", "now()+interval '40 days'", "now()-interval '20 days'", nil) // 后到期的才是当前订阅
	sub(4, 4, "grace", "now()+interval '2 days'", "now()-interval '30 days'", nil)
	sub(5, 5, "past_due", "now()+interval '1 day'", "now()-interval '29 days'", nil)
	sub(6, 5, "expired", "now()-interval '2 days'", "now()-interval '1 day'", nil)
	sub(7, 6, "active", "now()+interval '3 days'", "now()-interval '3 days'", nil)
	sub(8, 7, "trialing", "now()+interval '7 days'", "now()-interval '1 day'", nil)
	exec(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
	      VALUES($1,$2,'traffic.bytes','cycle',now()-interval '5 days',now()+interval '25 days',100,100,40),
	            ($1,$2,'traffic.bytes','cycle',now()-interval '35 days',now()-interval '5 days',50,50,50),
	            ($1,$3,'traffic.bytes','cycle',now()-interval '20 days',now()+interval '40 days',900,NULL,77)`,
		tenant, subID(0), subID(3))
	exec(`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,status)
	      VALUES($1,$2,$3,$4,$5,'grace')`, tenant, subID(4), userID(4), crypto.HashToken(token), token[:8])
	exec(`INSERT INTO ledger_accounts(tenant_id,account_type,normal_balance,currency,owner_user_id,balance_signed)
	      VALUES($1,'user_balance','credit','CNY',$2,-1234),($1,'user_balance','credit','USD',$3,-50)`,
		tenant, userID(0), userID(2))
	// 在线：同一 IP 在两个节点上算一台，窗口（缺省 5 分钟）外的不算
	exec(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
	      VALUES($1,$2,$4,'\x01',now()-interval '1 minute'),($1,$3,$4,'\x01',now()-interval '2 minutes'),
	            ($1,$2,$4,'\x02',now()-interval '3 minutes'),($1,$2,$4,'\x03',now()-interval '8 minutes')`,
		tenant, nodeA, nodeB, subID(0))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	svc := NewService(app)
	legacy := func(in ListUsersInput) ([]UserRow, int64) {
		t.Helper()
		in, args, err := listUsersArgs(tenant, in)
		if err != nil {
			t.Fatal(err)
		}
		var rows []UserRow
		var total int64
		if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+legacyListUsersWhereSQL, args...).Scan(&total); err != nil {
				return err
			}
			r, err := tx.Query(ctx, legacyListUsersSQL, append(args, in.Limit, in.Offset)...)
			if err != nil {
				return err
			}
			rows, err = scanUserRows(r)
			return err
		}); err != nil {
			t.Fatalf("legacy list %+v: %v", in, err)
		}
		return rows, total
	}
	asJSON := func(rows []UserRow) map[string]string {
		out := map[string]string{}
		for _, r := range rows {
			b, _ := json.Marshal(r)
			out[r.ID] = string(b)
		}
		return out
	}
	for _, in := range []ListUsersInput{
		{}, {Status: "active"}, {Status: "suspended, banned"}, {GroupID: group}, {GroupID: "none"},
		{SubState: "active"}, {SubState: "expired"}, {SubState: "none"},
		{Query: "LEGACY-USER-1"}, {Query: userID(3)}, {Query: token}, {Query: "https://sub.example/s/" + token + "?flag=clash"},
		{Query: "nobody-matches"}, {SubState: "active", GroupID: group, Status: "active"},
	} {
		in.Limit = 100
		got, total, err := svc.ListUsers(ctx, tenant, in)
		if err != nil {
			t.Fatalf("list %+v: %v", in, err)
		}
		want, wantTotal := legacy(in)
		g, w := asJSON(got), asJSON(want)
		if total != wantTotal || len(got) != len(want) || len(g) != len(w) {
			t.Fatalf("list %+v: rows=%d total=%d, legacy rows=%d total=%d", in, len(got), total, len(want), wantTotal)
		}
		for id, row := range w {
			if g[id] != row {
				t.Fatalf("list %+v: user %s differs\nnew:    %s\nlegacy: %s", in, id, g[id], row)
			}
		}
		for i := 1; i < len(got); i++ {
			a, b := got[i-1], got[i]
			if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID < b.ID) {
				t.Fatalf("list %+v: order breaks at %d: %s %v then %s %v", in, i, a.ID, a.CreatedAt, b.ID, b.CreatedAt)
			}
		}
	}

	full, total, err := svc.ListUsers(ctx, tenant, ListUsersInput{Limit: 100})
	if err != nil || total != 12 || len(full) != 12 {
		t.Fatalf("full list rows=%d total=%d err=%v", len(full), total, err)
	}
	var paged []string
	for off := 0; off < 16; off += 4 {
		page, pageTotal, err := svc.ListUsers(ctx, tenant, ListUsersInput{Limit: 4, Offset: off})
		if err != nil || pageTotal != 12 {
			t.Fatalf("page offset %d: total=%d err=%v", off, pageTotal, err)
		}
		for _, r := range page {
			paged = append(paged, r.ID)
		}
	}
	for i := range full {
		if i >= len(paged) || paged[i] != full[i].ID {
			t.Fatalf("paging over same-instant users skips or repeats rows: paged=%v", paged)
		}
	}
	if len(paged) != len(full) {
		t.Fatalf("paged %d rows, want %d", len(paged), len(full))
	}

	// 不是两边都空：用户 0 的读模型字段都有值
	var u0 *UserRow
	for i := range full {
		if full[i].ID == userID(0) {
			u0 = &full[i]
		}
	}
	if u0 == nil || u0.CurrentSubscription == nil {
		t.Fatalf("user 0 missing or has no current subscription: %+v", u0)
	}
	if cs := u0.CurrentSubscription; u0.Balance != 1234 || u0.Currency != "CNY" || u0.GroupName != "黄金组" ||
		cs.OnlineDevices != 2 || cs.DeviceLimit != 5 || cs.Traffic.Limit == nil || *cs.Traffic.Limit != 100 ||
		cs.Traffic.Consumed != 40 {
		t.Fatalf("user 0 row = %+v / %+v", *u0, *cs)
	}

	// 用户详情：配额一次读完后按订阅分回，每条订阅内按指标、周期倒序；在线设备按单条订阅计
	d0, err := svc.GetUser(ctx, tenant, userID(0))
	if err != nil || len(d0.Subscriptions) != 1 {
		t.Fatalf("user 0 detail = %+v err=%v", d0, err)
	}
	if s0 := d0.Subscriptions[0]; s0.OnlineDevices != 2 || len(s0.Quotas) != 2 || s0.Quotas[0].Consumed != 40 || s0.Quotas[1].Consumed != 50 {
		t.Fatalf("user 0 subscription detail = %+v", s0)
	}
	d2, err := svc.GetUser(ctx, tenant, userID(2))
	if err != nil || len(d2.Subscriptions) != 2 {
		t.Fatalf("user 2 detail = %+v err=%v", d2, err)
	}
	// 按创建时间倒序：sub(2) 较新、没有配额；sub(3) 较老、一条不限量配额
	if a, b := d2.Subscriptions[0], d2.Subscriptions[1]; a.ID != subID(2) || len(a.Quotas) != 0 ||
		b.ID != subID(3) || len(b.Quotas) != 1 || b.Quotas[0].Limit != nil || b.Quotas[0].Consumed != 77 || a.OnlineDevices != 0 {
		t.Fatalf("user 2 subscriptions = %+v", d2.Subscriptions)
	}
	t.Log("marker=admin_user_list_matches_legacy_ok")
}
